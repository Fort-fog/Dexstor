// Package tools implements the sandboxed tool layer of DEXSTER: every file
// operation is confined to the project root, existing files require a trusted
// read snapshot (CAS), writes are atomic, and test artifacts are protected.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Result is the outcome of a single tool execution.
type Result struct {
	Tool   string
	Path   string
	Output string // read content / diff listing / test output
	Error  string
	OK     bool
	Exit   int
}

func fail(tool, path, msg string) Result {
	return Result{Tool: tool, Path: path, Error: msg, OK: false, Exit: 1}
}

// Router executes tools inside a single project root.
type Router struct {
	Root               string
	VerifyMode         string // auto (legacy) or http_server
	VerifyHTTPURL      string // loopback HTTP probe URL; default http://127.0.0.1:8080/
	VerifyHTTPContains string // optional response body text required for HTTP success
	// Protected holds exact relative paths and root-level glob patterns
	// (e.g. "*_test.go") that must never be modified by the agent.
	Protected map[string]bool
	// baseSnapshot maps relative path -> sha256 captured when the run
	// started; it backs git_diff in repositories without Git metadata.
	baseSnapshot map[string]string
}

// New creates a router confined to the given project directory.
// Go test files are ordinary project files, not protected oracles.
func New(root string) (*Router, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("dexster: resolve root: %w", err)
	}
	if err := refuseOwnSource(abs); err != nil {
		return nil, err
	}
	r := &Router{
		Root:         abs,
		Protected:    map[string]bool{},
		baseSnapshot: map[string]string{},
	}
	if err := r.snapshot(); err != nil {
		return nil, fmt.Errorf("dexster: baseline snapshot: %w", err)
	}
	return r, nil
}

// refuseOwnSource rejects editing this agent's own source tree even if a caller
// bypasses the web UI. Symlinks are canonicalized before overlap testing.
func refuseOwnSource(root string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	roots := []string{os.Getenv("DEXSTER_SELF_ROOT")}
	if _, file, _, ok := runtime.Caller(0); ok {
		roots = append(roots, filepath.Join(filepath.Dir(file), "..", ".."))
	}
	for _, item := range roots {
		if item == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(item)
		if err != nil {
			continue
		}
		if pathOverlap(realRoot, real) {
			return fmt.Errorf("protected DEXSTER source tree: cannot use %s as task root", root)
		}
	}
	for dir := realRoot; ; dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(data), "module dexster") {
			for _, target := range []string{filepath.Join(dir, "cmd", "dexster", "main.go"), filepath.Join(dir, "cmd", "dexster-web", "main.go")} {
				if _, e := os.Stat(target); e == nil {
					return fmt.Errorf("protected DEXSTER/WEB source tree: %s", root)
				}
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return nil
}
func pathOverlap(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Path sandboxing
// ---------------------------------------------------------------------------

// safePath resolves p inside the project root and rejects any escape.
func (r *Router) safePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", p)
	}
	abs := filepath.Join(r.Root, clean)
	rel, err := filepath.Rel(r.Root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes project root: %s", p)
	}
	// Refuse symlinked components: lexical containment does not prevent
	// traversal through a symlink to a directory outside the project.
	current := r.Root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			break
		} // new file: parent checked above
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path component is not allowed: %s", p)
		}
	}
	return abs, nil
}

// ValidatePath checks lexical containment and symlink components before
// checkpointing. The router rechecks independently when executing a write.
func (r *Router) ValidatePath(p string) error {
	_, err := r.safePath(p)
	return err
}

// IsProtected reports whether the relative path is protected.
func (r *Router) IsProtected(p string) bool {
	clean := filepath.Clean(p)
	if r.Protected[clean] {
		return true
	}
	if filepath.Dir(clean) != "." {
		return false
	}
	for pattern := range r.Protected {
		if ok, err := filepath.Match(pattern, clean); err == nil && ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// File tools
// ---------------------------------------------------------------------------

// Read returns the content of a file inside the project.
func (r *Router) Read(p string) Result {
	abs, err := r.safePath(p)
	if err != nil {
		return fail("read_file", p, err.Error())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fail("read_file", p, err.Error())
	}
	return Result{Tool: "read_file", Path: p, Output: string(data), OK: true, Exit: 0}
}

// Exists reports whether the relative path exists inside the project.
func (r *Router) Exists(p string) bool {
	abs, err := r.safePath(p)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

// Write applies a CAS-guarded atomic write:
//   - protected paths are rejected before anything else;
//   - an existing file requires baseHash to match its current sha256
//     (the trusted read snapshot); stale snapshots are rejected;
//   - a new file must be written with an empty baseHash;
//   - identical content is an idempotent no-op success.
func (r *Router) Write(p, content, baseHash string) Result {
	if r.IsProtected(p) {
		return fail("write_file", p, fmt.Sprintf("protected file cannot be modified: %s", p))
	}
	abs, err := r.safePath(p)
	if err != nil {
		return fail("write_file", p, err.Error())
	}

	data, statErr := os.ReadFile(abs)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fail("write_file", p, statErr.Error())
	}

	if exists {
		if baseHash == "" {
			return fail("write_file", p, "existing file requires a trusted read snapshot (base hash)")
		}
		sum := sha256.Sum256(data)
		current := hex.EncodeToString(sum[:])
		if current != baseHash {
			return fail("write_file", p, fmt.Sprintf(
				"stale snapshot: expected base hash %s, current hash %s", baseHash, current))
		}
	} else if baseHash != "" {
		return fail("write_file", p, "new file must be written without a base hash")
	}

	if exists && string(data) == content {
		return Result{Tool: "write_file", Path: p, Output: "content already matches (no-op)", OK: true, Exit: 0}
	}

	// Atomic write: temp file in the same directory + rename.
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".dexster-tmp-*")
	if err != nil {
		return fail("write_file", p, fmt.Sprintf("create temp file: %v", err))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fail("write_file", p, fmt.Sprintf("write temp file: %v", err))
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail("write_file", p, fmt.Sprintf("sync temp file: %v", err))
	}
	if err := tmp.Close(); err != nil {
		return fail("write_file", p, fmt.Sprintf("close temp file: %v", err))
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fail("write_file", p, fmt.Sprintf("chmod temp file: %v", err))
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return fail("write_file", p, fmt.Sprintf("rename into place: %v", err))
	}

	action := "wrote"
	if !exists {
		action = "created"
	}
	return Result{Tool: "write_file", Path: p, Output: action + " " + p, OK: true, Exit: 0}
}

// ListPath lists a directory. Relative paths are confined to the project root;
// absolute paths are read-only external sources (for example ~/Pictures) and
// are never used as mutation destinations.
func (r *Router) ListPath(p string) Result {
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	var base string
	var err error
	if filepath.IsAbs(filepath.Clean(p)) {
		base = filepath.Clean(p)
		if info, e := os.Stat(base); e != nil || !info.IsDir() {
			if e != nil {
				return fail("list_files", p, e.Error())
			}
			return fail("list_files", p, "not a directory")
		}
	} else {
		base, err = r.safePath(p)
		if err != nil {
			return fail("list_files", p, err.Error())
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return fail("list_files", p, err.Error())
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if name == ".git" || name == ".kraken" || strings.HasPrefix(name, ".dexster") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return Result{Tool: "list_files", Path: p, Output: strings.Join(out, "\n"), OK: true, Exit: 0}
}

// MakeDir creates a directory tree inside the project root.
func (r *Router) MakeDir(p string) Result {
	abs, err := r.safePath(p)
	if err != nil {
		return fail("make_dir", p, err.Error())
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fail("make_dir", p, err.Error())
	}
	return Result{Tool: "make_dir", Path: p, Output: "created directory " + p, OK: true, Exit: 0}
}

// Copy copies a regular file or directory tree into the project. The source may
// be an absolute read-only external path; destination is always sandboxed.
func (r *Router) Copy(source, dest string) Result {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(dest) == "" {
		return fail("copy_file", dest, "source and destination are required")
	}
	var src string
	var err error
	if filepath.IsAbs(filepath.Clean(source)) {
		src = filepath.Clean(source)
	} else {
		src, err = r.safePath(source)
		if err != nil {
			return fail("copy_file", dest, err.Error())
		}
	}
	dst, err := r.safePath(dest)
	if err != nil {
		return fail("copy_file", dest, err.Error())
	}
	info, err := os.Lstat(src)
	if err != nil {
		return fail("copy_file", dest, err.Error())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fail("copy_file", dest, "symlink sources are not allowed")
	}
	if info.IsDir() {
		if err := copyDirNoSymlinks(src, dst); err != nil {
			return fail("copy_file", dest, err.Error())
		}
	} else if info.Mode().IsRegular() {
		if err := copyRegular(src, dst, info.Mode().Perm()); err != nil {
			return fail("copy_file", dest, err.Error())
		}
	} else {
		return fail("copy_file", dest, "source is not a regular file or directory")
	}
	return Result{Tool: "copy_file", Path: dest, Output: "copied " + source + " -> " + dest, OK: true, Exit: 0}
}

func copyRegular(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dexster-copy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, dst)
}
func copyDirNoSymlinks(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink source is not allowed: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported source type: %s", path)
		}
		return copyRegular(path, target, info.Mode().Perm())
	})
}

// Move renames a project path to another project path. External sources are
// intentionally copy-only so DEXSTER cannot remove a user's original assets.
func (r *Router) Move(source, dest string) Result {
	src, err := r.safePath(source)
	if err != nil {
		return fail("move_file", dest, err.Error())
	}
	dst, err := r.safePath(dest)
	if err != nil {
		return fail("move_file", dest, err.Error())
	}
	if r.IsProtected(source) || r.IsProtected(dest) {
		return fail("move_file", dest, "protected path")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fail("move_file", dest, err.Error())
	}
	if err := os.Rename(src, dst); err != nil {
		return fail("move_file", dest, err.Error())
	}
	return Result{Tool: "move_file", Path: dest, Output: "moved " + source + " -> " + dest, OK: true, Exit: 0}
}

// Delete removes a file or directory inside the project root only.
func (r *Router) Delete(p string) Result {
	if p == "." || strings.TrimSpace(p) == "" {
		return fail("delete_path", p, "refusing to delete project root")
	}
	abs, err := r.safePath(p)
	if err != nil {
		return fail("delete_path", p, err.Error())
	}
	if r.IsProtected(p) {
		return fail("delete_path", p, "protected path")
	}
	if err := os.RemoveAll(abs); err != nil {
		return fail("delete_path", p, err.Error())
	}
	return Result{Tool: "delete_path", Path: p, Output: "deleted " + p, OK: true, Exit: 0}
}

// ListFiles returns all project files as relative paths (sorted).
func (r *Router) ListFiles() ([]string, error) {
	var out []string
	err := filepath.Walk(r.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".dexster") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(r.Root, path)
		if relErr != nil {
			return nil
		}
		// Runtime bookkeeping is not task source code.
		if rel == ".dexster-worker-staged" || rel == ".kraken" || strings.HasPrefix(rel, ".kraken"+string(os.PathSeparator)) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Verification tools
// ---------------------------------------------------------------------------

func (r *Router) snapshot() error {
	files, err := r.ListFiles()
	if err != nil {
		return err
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(r.Root, rel))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		r.baseSnapshot[rel] = hex.EncodeToString(sum[:])
	}
	return nil
}

// GitDiff reports project changes since the run started. With a real .git
// directory it uses git status; otherwise it falls back to comparing current
// file hashes against the baseline snapshot.
func (r *Router) GitDiff() Result {
	return r.gitDiffWithScope(nil)
}

// GitDiffForPaths validates only agent-mutated source paths against confusable
// identifiers while keeping Git status and the CAS baseline globally intact.
// A nil scope means the original whole-project validation (GitDiff).
func (r *Router) GitDiffForPaths(paths []string) Result {
	if paths == nil {
		paths = []string{}
	}
	return r.gitDiffWithScope(paths)
}

func (r *Router) gitDiffWithScope(paths []string) Result {
	var err error
	if paths == nil {
		err = r.checkChangedCodeIdentifiers()
	} else {
		err = r.checkCodeIdentifiersForPaths(paths)
	}
	if err != nil {
		return fail("git_diff", "", err.Error())
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".git")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", "-C", r.Root, "status", "--porcelain").CombinedOutput()
		if err != nil {
			return fail("git_diff", "", fmt.Sprintf("git status failed: %v: %s", err, out))
		}
		text := strings.TrimSpace(string(out))
		if text == "" {
			return Result{Tool: "git_diff", Output: "no project changes", OK: true, Exit: 0}
		}
		return Result{Tool: "git_diff", Output: text, OK: true, Exit: 0}
	}

	var lines []string
	files, _ := r.ListFiles()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(r.Root, rel))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		current := hex.EncodeToString(sum[:])
		base, known := r.baseSnapshot[rel]
		switch {
		case !known:
			lines = append(lines, "added: "+rel)
		case base != current:
			lines = append(lines, "modified: "+rel)
		}
	}
	if len(lines) == 0 {
		return Result{Tool: "git_diff", Output: "no project changes", OK: true, Exit: 0}
	}
	return Result{Tool: "git_diff", Output: strings.Join(lines, "\n"), OK: true, Exit: 0}
}

// HasTestTarget reports whether the project contains a runnable test target:
// any *_test.go file (go test) or at least one root-level .go program.
func (r *Router) HasTestTarget() bool {
	files, err := r.ListFiles()
	if err != nil {
		return false
	}
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			return true
		}
	}
	for _, rel := range files {
		if filepath.Dir(rel) == "." && strings.HasSuffix(rel, ".go") {
			return true
		}
	}
	return false
}

// ensureGoMod creates a minimal go.mod inside the project root when missing.
// It never walks above the root (stricter than bratan-go's bootstrap).
func (r *Router) ensureGoMod() error {
	modPath := filepath.Join(r.Root, "go.mod")
	if _, err := os.Stat(modPath); err == nil {
		return nil
	}
	content := fmt.Sprintf("module dexster.local/%s\n\ngo 1.21\n", filepath.Base(r.Root))
	return os.WriteFile(modPath, []byte(content), 0o644)
}

// RunTests executes the project's test target:
//   - *_test.go present  -> go test -count=1 ./...
//   - otherwise root .go -> build the package and run it as a program;
//     the program is expected to print PASS on success (fixture contract).
func (r *Router) RunTests() Result {
	if r.VerifyMode == "http_server" {
		return r.runHTTPServerCheck()
	}
	if r.VerifyMode != "" && r.VerifyMode != "auto" {
		return fail("run_tests", "", "unknown verification mode: "+r.VerifyMode)
	}

	files, err := r.ListFiles()
	if err != nil {
		return fail("run_tests", "", err.Error())
	}

	hasGoTest := false
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			hasGoTest = true
			break
		}
	}

	if hasGoTest {
		if err := r.ensureGoMod(); err != nil {
			return fail("run_tests", "", fmt.Sprintf("cannot initialize Go module: %v", err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "test", "-v", "-count=1", "./...")
		cmd.Dir = r.Root
		out, err := cmd.CombinedOutput()
		text := string(out)
		if ctx.Err() == context.DeadlineExceeded {
			return Result{Tool: "run_tests", Output: text, Error: "timeout", OK: false, Exit: 124}
		}
		if err != nil {
			return Result{Tool: "run_tests", Output: text, Error: err.Error(), OK: false, Exit: exitCode(err)}
		}
		return Result{Tool: "run_tests", Output: text, OK: true, Exit: 0}
	}

	rootGo := false
	for _, rel := range files {
		if filepath.Dir(rel) == "." && strings.HasSuffix(rel, ".go") {
			rootGo = true
			break
		}
	}
	if !rootGo {
		return fail("run_tests", "", "no test target: no *_test.go and no root-level .go program")
	}

	if err := r.ensureGoMod(); err != nil {
		return fail("run_tests", "", fmt.Sprintf("cannot initialize Go module: %v", err))
	}
	binName := filepath.Join(r.Root, ".dexster-test.bin")
	defer os.Remove(binName)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binName, ".")
	cmd.Dir = r.Root
	buildOut, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Tool: "run_tests", Output: string(buildOut), Error: "build failed: " + err.Error(), OK: false, Exit: exitCode(err)}
	}

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	cmd = exec.CommandContext(runCtx, binName)
	cmd.Dir = r.Root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if runCtx.Err() == context.DeadlineExceeded {
		return Result{Tool: "run_tests", Output: text, Error: "timeout", OK: false, Exit: 124}
	}
	if err != nil {
		return Result{Tool: "run_tests", Output: text, Error: err.Error(), OK: false, Exit: exitCode(err)}
	}
	return Result{Tool: "run_tests", Output: text, OK: true, Exit: 0}
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

// runHTTPServerCheck verifies a long-running HTTP application without expecting
// its process to exit. Only an explicitly configured loopback URL is probed.
func (r *Router) runHTTPServerCheck() Result {
	target := r.VerifyHTTPURL
	if target == "" {
		target = "http://127.0.0.1:8080/"
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return fail("run_tests", "", "HTTP verification requires a valid http:// loopback URL")
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fail("run_tests", "", "HTTP verification URL must use localhost or loopback")
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(host, "80")
	}
	// Refuse to accept a response from another server already listening here.
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return fail("run_tests", "", "HTTP verification port already occupied: "+addr)
	}
	if err := r.ensureGoMod(); err != nil {
		return fail("run_tests", "", err.Error())
	}
	// The output executable stays outside the source tree.
	tmp, err := os.MkdirTemp("", "dexster-http-verify-")
	if err != nil {
		return fail("run_tests", "", err.Error())
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "app")
	buildCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", bin, ".")
	build.Dir = r.Root
	out, err := build.CombinedOutput()
	if buildCtx.Err() == context.DeadlineExceeded {
		return Result{Tool: "run_tests", Output: string(out), Error: "build timeout", Exit: 124}
	}
	if err != nil {
		return Result{Tool: "run_tests", Output: string(out), Error: "build failed: " + err.Error(), Exit: exitCode(err)}
	}
	// Run existing tests as well; a successful HTTP response must not hide failures.
	files, err := r.ListFiles()
	if err != nil {
		return fail("run_tests", "", err.Error())
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			testCtx, tcancel := context.WithTimeout(context.Background(), 120*time.Second)
			test := exec.CommandContext(testCtx, "go", "test", "-count=1", "./...")
			test.Dir = r.Root
			testOut, testErr := test.CombinedOutput()
			expired := testCtx.Err() == context.DeadlineExceeded
			tcancel()
			if expired {
				return Result{Tool: "run_tests", Output: string(testOut), Error: "test timeout", Exit: 124}
			}
			if testErr != nil {
				return Result{Tool: "run_tests", Output: string(testOut), Error: "tests failed: " + testErr.Error(), Exit: exitCode(testErr)}
			}
			break
		}
	}
	cmd := exec.Command(bin)
	cmd.Dir = r.Root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var log strings.Builder
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		return fail("run_tests", "", "server start failed: "+err.Error())
	}
	defer func() {
		// Terminate the entire spawned process group, including child processes.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	client := &http.Client{Timeout: 750 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(8 * time.Second)
	var lastErr string
	for time.Now().Before(deadline) {
		resp, e := client.Get(target)
		if e == nil {
			// Bound the response read: a misbehaving server must not exhaust memory.
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 && readErr == nil {
				if r.VerifyHTTPContains == "" || strings.Contains(string(body), r.VerifyHTTPContains) {
					return Result{Tool: "run_tests", Output: fmt.Sprintf("PASS HTTP %d %s", resp.StatusCode, target), OK: true, Exit: 0}
				}
				lastErr = fmt.Sprintf("HTTP response missing required text %q", r.VerifyHTTPContains)
			} else if readErr != nil {
				lastErr = "HTTP body read: " + readErr.Error()
			} else {
				lastErr = fmt.Sprintf("HTTP status %d", resp.StatusCode)
			}
		} else {
			lastErr = e.Error()
		}
		time.Sleep(150 * time.Millisecond)
	}
	// Include bounded server stdout/stderr so a program that bound a
	// different port (e.g. :0) is diagnosable without blind source edits.
	serverLog := strings.TrimSpace(log.String())
	if len(serverLog) > 800 {
		serverLog = serverLog[:800]
	}
	return Result{Tool: "run_tests", Output: serverLog, Error: fmt.Sprintf("HTTP verification failed at required URL %s: %s; confirm server listens on the required port (server output: %s)", target, lastErr, serverLog), OK: false, Exit: 1}
}
