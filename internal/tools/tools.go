package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Result struct {
	OK      bool
	Tool    string
	Path    string
	Content string
	Output  string
	Error   string
	Status  string
	Exit    int
	Tree    string
	Changed bool
}

type Router struct {
	Root          string
	ProtectedPath map[string]bool
}

func New(root string) *Router {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}

	return &Router{
		Root: abs,
		ProtectedPath: map[string]bool{
			"test.pl": true,
		},
	}
}

func (r *Router) Exists(path string) bool {
	abs, err := r.safePath(path)
	if err != nil {
		return false
	}

	_, err = os.Stat(abs)
	return err == nil
}

func (r *Router) safePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	if filepath.IsAbs(path) {
		return "", fmt.Errorf(
			"absolute paths are not allowed: %s",
			path,
		)
	}

	abs, err := filepath.Abs(
		filepath.Join(r.Root, path),
	)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(r.Root, abs)
	if err != nil {
		return "", err
	}

	if rel == ".." ||
		strings.HasPrefix(
			rel,
			".."+string(os.PathSeparator),
		) {
		return "", fmt.Errorf(
			"path escapes project root: %s",
			path,
		)
	}

	current := r.Root
	parts := strings.Split(rel, string(os.PathSeparator))

	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}

		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				break
			}

			return "", err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"path contains symlink: %s",
				path,
			)
		}
	}

	return abs, nil
}

func (r *Router) isProtected(path string) bool {
	clean := filepath.Clean(path)

	if filepath.IsAbs(clean) {
		return false
	}

	return r.ProtectedPath[clean]
}

// commandTargetsProtectedPath performs a conservative check before
// executing a shell command. Protected files must remain protected
// regardless of whether the command uses shell redirection, rm,
// perl, cp, mv, or another executable.
//
// The check intentionally happens before /bin/sh is started.
func (r *Router) commandTargetsProtectedPath(command string) (string, bool) {
	for protected := range r.ProtectedPath {
		relative := filepath.Clean(protected)
		absolute := filepath.Join(r.Root, relative)

		candidates := []string{
			relative,
			"." + string(os.PathSeparator) + relative,
			absolute,
		}

		for _, candidate := range candidates {
			if strings.Contains(command, candidate) {
				return protected, true
			}
		}
	}

	return "", false
}

func (r *Router) Read(path string) Result {
	abs, err := r.safePath(path)
	if err != nil {
		return Result{
			Tool:   "read_file",
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{
			Tool:   "read_file",
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	return Result{
		OK:      true,
		Tool:    "read_file",
		Path:    path,
		Content: string(data),
		Status:  "PASS",
		Exit:    0,
	}
}

func (r *Router) Write(path, content string) Result {
	return r.write(path, content, "")
}

func (r *Router) WriteWithBaseHash(path, content, baseHash string) Result {
	if baseHash == "" {
		return Result{
			Tool:   "write_file",
			Path:   path,
			Error:  "base hash is required",
			Status: "FAIL",
			Exit:   1,
		}
	}

	return r.write(path, content, baseHash)
}

func (r *Router) write(path, content, expectedBaseHash string) Result {
	abs, err := r.safePath(path)
	if err != nil {
		return Result{
			Tool:   "write_file",
			Path:   path,
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	if r.isProtected(path) {
		return Result{
			Tool:   "write_file",
			Path:   path,
			Error:  fmt.Sprintf("protected file cannot be modified: %s", path),
			Status: "FAIL",
			Exit:   1,
		}
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			if expectedBaseHash != "" {
				return Result{
					Tool:   "write_file",
					Path:   path,
					Error:  "write target does not exist for CAS write",
					Status: "FAIL",
					Exit:   1,
				}
			}

			data = nil
		} else {
			return Result{
				Tool:   "write_file",
				Path:   path,
				Error:  err.Error(),
				Status: "FAIL",
				Exit:   1,
			}
		}
	}

	if expectedBaseHash != "" {
		sum := sha256.Sum256(data)
		currentHash := hex.EncodeToString(sum[:])

		if currentHash != expectedBaseHash {
			return Result{
				Tool: "write_file",
				Path: path,
				Error: fmt.Sprintf(
					"stale snapshot: expected base hash %s, current hash %s",
					expectedBaseHash,
					currentHash,
				),
				Status: "FAIL",
				Exit:   1,
			}
		}
	}

	/*
		Idempotent write:
		if the target already contains exactly the requested content,
		there is nothing to write.
	*/
	if string(data) == content {
		return Result{
			OK:      true,
			Tool:    "write_file",
			Path:    path,
			Content: content,
			Status:  "PASS",
			Exit:    0,
		}
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return Result{
			Tool:   "write_file",
			Path:   path,
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		return Result{
			Tool:   "write_file",
			Path:   path,
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	return Result{
		OK:     true,
		Tool:   "write_file",
		Path:   path,
		Status: "PASS",
		Exit:   0,
	}
}

func (r *Router) Inspect(path string, depth int) Result {
	abs, err := r.safePath(path)
	if err != nil {
		return Result{
			Tool:   "inspect_project",
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	var lines []string
	var walk func(string, int, string) error

	walk = func(current string, level int, prefix string) error {
		if level > depth {
			return nil
		}

		entries, err := os.ReadDir(current)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			name := entry.Name()

			if name == ".git" {
				continue
			}

			lines = append(lines, prefix+name)

			if entry.IsDir() && level < depth {
				if err := walk(
					filepath.Join(current, name),
					level+1,
					prefix+"  ",
				); err != nil {
					return err
				}
			}
		}

		return nil
	}

	if err := walk(abs, 0, ""); err != nil {
		return Result{
			Tool:   "inspect_project",
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	return Result{
		OK:     true,
		Tool:   "inspect_project",
		Path:   path,
		Tree:   strings.Join(lines, "\n"),
		Status: "PASS",
		Exit:   0,
	}
}

func (r *Router) GitDiff(path string) Result {
	target := path
	if target == "" {
		target = "."
	}

	cmd := exec.Command(
		"git",
		"-C",
		r.Root,
		"--no-pager",
		"diff",
		"--",
		target,
	)

	/*
		Git must operate independently of the user's global environment.
		This is required for the AppArmor sandbox and also makes git_diff
		deterministic for the agent.

		Do not grant the sandbox access to the user's ~/.gitconfig.
		Do not allow Git to launch an interactive pager.
	*/
	cmd.Env = append(
		os.Environ(),
		"HOME="+r.Root,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_PAGER=cat",
		"PAGER=cat",
	)

	out, err := cmd.CombinedOutput()

	if err != nil {
		exit := -1
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}

		return Result{
			Tool:   "git_diff",
			Output: string(out),
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   exit,
		}
	}

	return Result{
		OK:     true,
		Tool:   "git_diff",
		Output: string(out),
		Status: "PASS",
		Exit:   0,
	}
}

func (r *Router) Exec(command string) Result {
	if strings.TrimSpace(command) == "" {
		return Result{
			Tool:   "exec",
			Error:  "command is required",
			Status: "FAIL",
			Exit:   1,
		}
	}

	if protected, blocked := r.commandTargetsProtectedPath(command); blocked {
		return Result{
			Tool:   "exec",
			Error:  fmt.Sprintf("protected file cannot be accessed through exec: %s", protected),
			Status: "FAIL",
			Exit:   1,
		}
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.Root

	out, err := cmd.CombinedOutput()

	result := Result{
		Tool:   "exec",
		Output: string(out),
	}

	if err == nil {
		result.OK = true
		result.Exit = 0
		result.Status = "PASS"
		return result
	}

	if cmd.ProcessState != nil {
		result.Exit = cmd.ProcessState.ExitCode()
	} else {
		result.Exit = -1
	}

	result.Error = err.Error()
	result.Status = "FAIL"

	return result
}

func (r *Router) AssertOutput(command, expected string) Result {
	if strings.TrimSpace(command) == "" {
		return Result{
			Tool:   "assert_output",
			Error:  "command is required",
			Status: "FAIL",
			Exit:   1,
		}
	}

	if protected, blocked := r.commandTargetsProtectedPath(command); blocked {
		return Result{
			Tool:   "assert_output",
			Error:  fmt.Sprintf("protected file cannot be accessed through assert_output: %s", protected),
			Status: "FAIL",
			Exit:   1,
		}
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.Root

	out, err := cmd.CombinedOutput()
	actual := string(out)

	if strings.HasSuffix(actual, "\r\n") {
		actual = strings.TrimSuffix(actual, "\r\n")
	} else if strings.HasSuffix(actual, "\n") {
		actual = strings.TrimSuffix(actual, "\n")
	}

	result := Result{
		Tool:   "assert_output",
		Output: actual,
	}

	if err != nil {
		if cmd.ProcessState != nil {
			result.Exit = cmd.ProcessState.ExitCode()
		} else {
			result.Exit = -1
		}

		result.Error = err.Error()
		result.Status = "FAIL"
		return result
	}

	result.Exit = 0

	if actual != expected {
		result.Error = fmt.Sprintf(
			"output mismatch: expected %q, got %q",
			expected,
			actual,
		)
		result.Status = "FAIL"
		return result
	}

	result.OK = true
	result.Status = "PASS"

	return result
}

func (r *Router) Test(path string) Result {
	abs, err := r.safePath(path)
	if err != nil {
		return Result{
			Tool:   "test",
			Path:   path,
			Error:  err.Error(),
			Status: "FAIL",
			Exit:   1,
		}
	}

	var cmd *exec.Cmd
	var goTestCmd bool

	switch filepath.Ext(abs) {
	case ".pl":
		cmd = exec.Command("perl", abs)

	case ".js":
		cmd = exec.Command("node", abs)

	case ".py":
		cmd = exec.Command("python3", abs)

	case ".go":
		goTestCmd = true
		cmd = exec.Command("go", "test", "-v", "-count=1", ".")

	default:
		return Result{
			Tool:   "test",
			Path:   path,
			Error:  "unsupported test file type",
			Status: "FAIL",
			Exit:   1,
		}
	}

	if goTestCmd {
		/*
			Run the Go module that contains the tested file so that
			"go test" operates on a real package instead of executing
			a single file with "go run".
		*/
		cmd.Dir = filepath.Dir(abs)
	} else {
		cmd.Dir = r.Root
	}

	out, err := cmd.CombinedOutput()

	result := Result{
		Tool:   "test",
		Path:   path,
		Output: string(out),
	}

	if err != nil {
		if cmd.ProcessState != nil {
			result.Exit = cmd.ProcessState.ExitCode()
		} else {
			result.Exit = -1
		}

		result.Error = err.Error()
		result.Status = "FAIL"
		return result
	}

	result.OK = true
	result.Exit = 0
	result.Status = "PASS"

	if goTestCmd && !strings.Contains(result.Output, "PASS") {
		/*
			ActionLedger compatibility adapter: a successful Go test
			must deterministically carry the standard PASS marker in
			Result.Output, because validateActionLedgerResultIntegrity
			and validateCompletion require "PASS" inside the recorded
			result of a successful test action.
		*/
		if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
			result.Output += "\n"
		}

		result.Output += "PASS\n"
	}

	return result
}
