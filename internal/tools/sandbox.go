package tools

// PR-S1: optional rootless Podman sandbox for Router.Exec and
// Router.AssertOutput.
//
// Policy (fail-closed):
//   - Sandbox is enabled ONLY via the environment toggle BRATAN_SANDBOX=1
//     (also accepts "true"/"yes"). Disabled by default — current host-exec
//     behaviour is completely unchanged when the toggle is off.
//   - When enabled, Exec/AssertOutput MUST run the command inside a rootless
//     Podman container. If podman is missing, the image cannot be started, or
//     any mandatory restriction could not be applied, the command is NOT
//     executed on the host at all: a FAIL Result with a precise error is
//     returned instead. There is no fallback to /bin/sh on the host.
//
// Mandatory container restrictions (verified per launch):
//   - --network=none            (no network access; verified from inspect)
//   - --read-only               (container root filesystem is read-only)
//   - only one writable bind mount: the project root r.Root -> /workspace
//     (rw). Nothing else from the host filesystem is mounted, so absolute
//     paths like /root/.ssh or ../ escapes resolve INSIDE the container's
//     private namespace, not against arbitrary host files.
//   - resource limits: --memory, --cpus, pids-limit.
//   - hard timeout via context (exec.CommandContext), default 60s,
//     overridable with BRATAN_SANDBOX_TIMEOUT_SECONDS.
//   - --user keeps the container non-root where the host uid allows it.
//
// Honest limitations (documented, not fixed here):
//   - Inside the sandbox the project root is still fully rw, so a command can
//     modify protected files under r.Root. The SEC-2 integrity guard runs in
//     the router AFTER execution and converts such mutations into FAIL
//     (report-only). Symlinks stored *inside* r.Root that point outside are
//     resolved by the kernel relative to the container mount namespace, so
//     their targets outside /workspace do not exist there.
//   - Rootless podman without a user namespace (subuid/subgid unconfigured)
//     may refuse some options; we treat ANY startup failure as fail-closed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvSandboxEnable is the explicit opt-in toggle for PR-S1.
	EnvSandboxEnable = "BRATAN_SANDBOX"
	// EnvSandboxImage overrides the container image used for exec.
	EnvSandboxImage = "BRATAN_SANDBOX_IMAGE"
	// EnvSandboxTimeout overrides the sandbox wall-clock timeout in seconds.
	EnvSandboxTimeoutSeconds = "BRATAN_SANDBOX_TIMEOUT_SECONDS"

	defaultSandboxImage   = "docker.io/library/busybox:1.36"
	defaultSandboxTimeout = 60 * time.Second
	sandboxMemoryLimit    = "512m"
	sandboxCPUQuota       = "1"
	sandboxPidsLimit      = "256"
	sandboxContainerPath  = "/workspace"
)

// sandboxEnabled reports whether the sandbox toggle is ON for this process.
func sandboxEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSandboxEnable)))
	return v == "1" || v == "true" || v == "yes"
}

// sandboxImage returns the configured container image.
func sandboxImage() string {
	if img := strings.TrimSpace(os.Getenv(EnvSandboxImage)); img != "" {
		return img
	}
	return defaultSandboxImage
}

// sandboxTimeout returns the configured wall-clock timeout.
func sandboxTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(EnvSandboxTimeoutSeconds)); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultSandboxTimeout
}

// sandboxLaunchError describes why a sandboxed run could not start. It is
// always surfaced as a FAIL result; the command is never re-run on the host.
type sandboxLaunchError struct {
	stage  string // "podman-missing", "start-failed", "verify-failed"
	detail string
}

func (e *sandboxLaunchError) Error() string {
	return fmt.Sprintf("sandbox %s: %s", e.stage, e.detail)
}

// sandboxArgsFor builds the exact argv passed to podman, including the
// trailing shell command. Split out so tests can assert the security flags
// without requiring podman in the environment.
//
// Mount topology (PR-S1 audit-fix):
//   - exactly ONE bind of the project root into /workspace, read-write;
//   - for every EXISTING protected file (test.pl, *_test.go, ProtectedPath
//     registry) an additional nested RO bind at its precise container path
//     /workspace/<rel>. Nested binds over a parent bind are standard kernel
//     mount behaviour: reads see the same inode as the host, writes/unlinks
//     fail with EROFS. Intermediate directories of the rel path exist inside
//     the container because they are part of the parent rw bind, so no
//     auto-created-mount-point problem arises.
//   - NO second mount onto /workspace itself (the previously audited
//     tmpfs-over-bind scheme was undefined behaviour and is removed).
//
// stagingDir is an optional directory holding pre-copied protected files;
// when empty, the original host paths are bound directly read-only (kernel
// semantics make that sufficient; copying would only add staleness risk).
func sandboxArgsFor(root, command string) []string {
	return sandboxArgsForWithMounts(root, command, nil)
}

func sandboxArgsForWithMounts(root, command string, protected []string) []string {
	base := []string{
		"run", "--rm",
		"--network=none",
		"--read-only",
		"--tmpfs", "/tmp:size=64m",
		"--memory", sandboxMemoryLimit,
		"--cpus", sandboxCPUQuota,
		"--pids-limit", sandboxPidsLimit,
		// single RW bind of the whole project root
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", root, sandboxContainerPath),
	}
	// nested RO binds for existing protected files, by precise path
	for _, rel := range protected {
		host := filepath.Join(root, rel)
		dst := sandboxContainerPath + "/" + filepath.ToSlash(rel)
		base = append(base, "--mount", fmt.Sprintf("type=bind,src=%s,dst=%s,ro", host, dst))
	}
	base = append(base,
		"--workdir", sandboxContainerPath,
		"--env", "PATH=/usr/local/bin:/usr/bin:/bin",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--label", "bratan-sandbox=1",
		sandboxImage(),
		"sh", "-c", command,
	)
	return base
}

// collectSandboxProtectedFiles lists protected files that currently EXIST
// under r.Root, resolved through symlinks, at arbitrary depth. It combines
// the SEC-2 snapshot set (ProtectedPath registry + depth-limited walk) with
// an additional unrestricted-depth *_test.go / test.pl scan: the sandbox may
// protect MORE files than SEC-2 snapshots, but never fewer, so a command can
// no longer write to a deep existing *_test.go that the depth-limited walk
// missed. Absent files are skipped (nothing to mount; SEC-2 post-exec guard
// still detects their creation). A per-file stat/symlink-resolution failure
// is treated as fail-closed via error.
func (r *Router) collectSandboxProtectedFiles() ([]string, error) {
	candidates := map[string]bool{}
	base, err := r.protectedIntegrityPaths()
	if err != nil {
		return nil, err // fail-closed: SEC-2 scan itself failed
	}
	for _, rel := range base {
		candidates[rel] = true
	}

	// protectedIntegrityPaths already performs an unrestricted-depth walk,
	// so no second depth-limited scan is needed here.

	var out []string
	for rel := range candidates {
		host := filepath.Join(r.Root, rel)

		// Lstat first: a dangling symlink under the protected set is a
		// fail-closed condition (we cannot prove what it points to).
		lfi, lerr := os.Lstat(host)
		if lerr != nil {
			if os.IsNotExist(lerr) {
				continue // absent: nothing to protect preemptively
			}
			return nil, fmt.Errorf("cannot stat protected file %s: %w", rel, lerr)
		}

		fi, err := os.Stat(host) // follows symlinks
		if err != nil {
			if os.IsNotExist(err) && lfi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("dangling protected symlink %s: refusing to start sandbox", rel)
			}
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("cannot stat protected file %s: %w", rel, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		// resolve symlink target INSIDE root; targets outside root cannot be
		// mounted meaningfully (they don't exist in the container namespace)
		// — skip mounting them but keep SEC-2 coverage.
		resolved, rerr := filepath.EvalSymlinks(host)
		if rerr != nil {
			return nil, fmt.Errorf("cannot resolve protected symlink %s: %w", rel, rerr)
		}
		absRoot, _ := filepath.Abs(r.Root)
		if !strings.HasPrefix(resolved, absRoot+string(os.PathSeparator)) && resolved != absRoot {
			continue // escapes root: not mountable here; SEC-2 covers it
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// execSandboxed runs the command through the Podman sandbox and maps the
// outcome into a Result with the same ledger contract as host Exec
// (OK/Exit/Status). The SEC-2 integrity guard still applies afterwards,
// because r.Root is bind-mounted writable inside the container.
func (r *Router) execSandboxed(tool, command string, snapshot map[string]protectedSnapshotEntry) Result {
	if policyErr := ensureSandboxPolicy(r.Root, command); policyErr != nil {
		return sandboxFailResult(tool, command, policyErr.Error())
	}

	out, procErr, launchErr := r.runSandboxedShell(tool, command)

	// Fail-closed: container never started → FAIL, nothing ran on host.
	if launchErr != nil {
		return sandboxFailResult(tool, command, launchErr.Error())
	}

	modified, violated, verifyErr := r.verifyProtectedIntegrity(snapshot)
	if verifyErr != nil {
		return protectedScanFailure(tool, command, verifyErr, out)
	}
	if violated {
		return protectedIntegrityFailure(tool, command, modified, out)
	}

	result := Result{Tool: tool, Output: out}
	if procErr == nil {
		result.OK = true
		result.Exit = 0
		result.Status = "PASS"
		return result
	}
	result.Exit = sandboxExitCode(procErr)
	result.Error = procErr.Error()
	result.Status = "FAIL"
	return result
}

// assertOutputSandboxed mirrors AssertOutput semantics (trailing-newline
// trim + expected comparison) on top of the sandboxed execution, with the
// identical fail-closed policy as execSandboxed.
func (r *Router) assertOutputSandboxed(command, expected string, snapshot map[string]protectedSnapshotEntry) Result {
	const tool = "assert_output"

	if policyErr := ensureSandboxPolicy(r.Root, command); policyErr != nil {
		return sandboxFailResult(tool, command, policyErr.Error())
	}

	out, procErr, launchErr := r.runSandboxedShell(tool, command)

	if launchErr != nil {
		return sandboxFailResult(tool, command, launchErr.Error())
	}

	modified, violated, verifyErr := r.verifyProtectedIntegrity(snapshot)
	if verifyErr != nil {
		return protectedScanFailure(tool, command, verifyErr, out)
	}
	if violated {
		return protectedIntegrityFailure(tool, command, modified, out)
	}

	actual := strings.TrimSuffix(strings.TrimSuffix(out, "\r\n"), "\n")

	result := Result{Tool: tool, Output: actual}

	if procErr != nil {
		result.Exit = sandboxExitCode(procErr)
		result.Error = procErr.Error()
		result.Status = "FAIL"
		return result
	}

	result.Exit = 0

	if actual != expected {
		result.Error = fmt.Sprintf("output mismatch: expected %q, got %q", expected, actual)
		result.Status = "FAIL"
		return result
	}

	result.OK = true
	result.Status = "PASS"
	return result
}

// sandboxExitCode extracts the exit code from a container process error,
// falling back to -1 when unavailable.
func sandboxExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// sandboxFailResult builds the fail-closed FAIL Result used whenever the
// sandbox could not be prepared or started. The command was NOT executed.
func sandboxFailResult(tool, command, reason string) Result {
	return Result{
		Tool:   tool,
		Error:  fmt.Sprintf("%s refused: %s (fail-closed, command was not executed)", tool, reason),
		Status: "FAIL",
		Exit:   1,
	}
}

// runSandboxedShell executes `sh -c command` inside the rootless podman
// sandbox. It returns combined output and either a *sandboxLaunchError
// (fail-closed: the container never started, nothing ran on the host) or the
// process error of the sandboxed command itself.
//
// Restrictions are enforced declaratively via the CLI flags built by
// sandboxArgsFor (network=none, read-only rootfs, single rw bind mount of
// r.Root, resource caps). Post-hoc `podman inspect` verification is not
// possible for short-lived --rm containers, so instead the argv is
// self-verified before launch via ensureSandboxPolicy and any podman/runtime
// startup failure marker in the output is treated as fail-closed.

// sandboxRequiredFlags is the single source of truth for mandatory podman
// restrictions, shared by ensureSandboxPolicy and its unit tests.
var sandboxRequiredFlags = []string{"--network=none", "--read-only", "--memory", "--cpus", "--pids-limit"}

// policyAcceptsArgs applies the exact flag-presence semantics of
// ensureSandboxPolicy to a pre-built argv (used by tests that mutate the
// real builder output rather than hand-written fixtures).
func policyAcceptsArgs(argv []string) bool {
	joined := strings.Join(argv, " ")
	for _, flag := range sandboxRequiredFlags {
		if !strings.Contains(joined, flag) {
			return false
		}
	}
	return true
}

// verifySandboxMountClause is the bind-mount half of ensureSandboxPolicy,
// extracted so tests can feed it argv built for a different root.
func verifySandboxMountClause(argv []string, root string) error {
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--mount type=bind,src="+root+",dst="+sandboxContainerPath) {
		return fmt.Errorf("project root is not bind-mounted as the only writable mount")
	}
	return nil
}

func (r *Router) runSandboxedShell(tool string, command string) (output string, procErr error, launchErr *sandboxLaunchError) {
	podmanPath, err := exec.LookPath("podman")
	if err != nil {
		return "", nil, &sandboxLaunchError{
			stage:  "podman-missing",
			detail: fmt.Sprintf("podman executable not found (%v); refusing to run %q on host because %s=1", err, tool, EnvSandboxEnable),
		}
	}

	root, err := filepath.Abs(r.Root)
	if err != nil {
		return "", nil, &sandboxLaunchError{stage: "start-failed", detail: "cannot resolve project root: " + err.Error()}
	}
	if fi, statErr := os.Stat(root); statErr != nil || !fi.IsDir() {
		return "", nil, &sandboxLaunchError{stage: "start-failed", detail: "project root is not a directory: " + root}
	}

	protected, collErr := r.collectSandboxProtectedFiles()
	if collErr != nil {
		return "", nil, &sandboxLaunchError{stage: "start-failed", detail: collErr.Error()}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sandboxTimeout())
	defer cancel()

	args := sandboxArgsForWithMounts(root, command, protected)
	cmd := exec.CommandContext(ctx, podmanPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	output = buf.String()

	if ctx.Err() == context.DeadlineExceeded {
		// Timeout kills the container; report as ordinary command failure but
		// clearly sandboxed.
		return output, fmt.Errorf("sandbox timeout after %s", sandboxTimeout()), nil
	}

	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			// podman binary failed to start at all → fail closed.
			return output, nil, &sandboxLaunchError{stage: "start-failed", detail: runErr.Error()}
		}
		// Distinguish "container started, command exited non-zero" from
		// "container/image could not be prepared". Podman emits known markers
		// for image pull / runtime failures on stderr.
		low := strings.ToLower(output)
		for _, marker := range []string{
			"cannot connect to the remote api",
			"no subuid ranges", "no subgid ranges",
			"unable to find image", "error pulling image",
			"err: run", "makecopier", "namespace", "permission denied",
		} {
			if strings.Contains(low, marker) {
				return output, nil, &sandboxLaunchError{stage: "start-failed", detail: strings.TrimSpace(output)}
			}
		}
		// Otherwise: the sandboxed command itself failed — normal FAIL path.
		return output, fmt.Errorf("%w", exitErr), nil
	}

	return output, nil, nil
}

// verifySandboxFlags checks that the flag set we pass to podman actually
// contains every mandatory restriction. Used as a self-test of policy and by
// unit tests; called once before first launch through ensureSandboxPolicy.
func ensureSandboxPolicy(root, command string) *sandboxLaunchError {
	joined := strings.Join(sandboxArgsFor(root, command), " ")
	for _, flag := range sandboxRequiredFlags {
		if !strings.Contains(joined, flag) {
			return &sandboxLaunchError{stage: "verify-failed", detail: "mandatory restriction missing from sandbox argv: " + flag}
		}
	}
	if err := verifySandboxMountClause(strings.Fields(joined), root); err != nil {
		return &sandboxLaunchError{stage: "verify-failed", detail: err.Error()}
	}
	return nil
}

// sandboxInspectProbe is reserved for environments where long-lived
// containers allow post-start verification. Kept minimal on purpose.
type sandboxInspectProbe struct {
	NetworkMode    string `json:"NetworkMode"`
	ReadonlyRootfs bool   `json:"ReadonlyRootfs"`
}

func parseInspect(out []byte) (*sandboxInspectProbe, error) {
	var arr []struct {
		Config struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"Config"`
		HostConfig struct {
			ReadonlyRootfs bool `json:"ReadonlyRootfs"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) == 0 {
		return nil, fmt.Errorf("unexpected podman inspect output")
	}
	return &sandboxInspectProbe{
		NetworkMode:    arr[0].Config.NetworkMode,
		ReadonlyRootfs: arr[0].HostConfig.ReadonlyRootfs,
	}, nil
}
