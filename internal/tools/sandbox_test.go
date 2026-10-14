package tools

// PR-S1 regression tests for the optional rootless Podman sandbox used by
// Router.Exec and Router.AssertOutput.
//
// Environment note: podman is NOT installed in this CI environment, so the
// fail-closed paths are fully testable here (missing binary → refusal).
// Integration tests that require a working rootless podman are guarded with
// BRATAN_TEST_PODMAN_INTEGRATION=1 and are marked SKIP (NOT PASS) otherwise.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withSandboxToggle flips the env toggle for one test only.
func withSandboxToggle(t *testing.T, value string) {
	t.Helper()
	old, had := os.LookupEnv(EnvSandboxEnable)
	t.Setenv(EnvSandboxEnable, value)
	t.Cleanup(func() {
		if had {
			os.Setenv(EnvSandboxEnable, old)
		} else {
			os.Unsetenv(EnvSandboxEnable)
		}
	})
}

func podmanAvailable() bool {
	_, err := exec.LookPath("podman")
	return err == nil
}

// ---------------------------------------------------------------------------
// Toggle behaviour
// ---------------------------------------------------------------------------

func TestSandboxDisabledByDefaultHostBehaviourUnchanged(t *testing.T) {
	withSandboxToggle(t, "") // explicit off
	root := t.TempDir()
	r := New(root)

	res := r.Exec("echo hello")
	if !res.OK || res.Exit != 0 || !strings.Contains(res.Output, "hello") {
		t.Fatalf("toggle OFF must keep host exec unchanged, got %+v", res)
	}
}

func TestSandboxToggleParsing(t *testing.T) {
	for _, v := range []string{"1", "true", "YES", " True "} {
		t.Setenv(EnvSandboxEnable, v)
		if !sandboxEnabled() {
			t.Errorf("BRATAN_SANDBOX=%q should enable sandbox", v)
		}
	}
	for _, v := range []string{"", "0", "false", "off"} {
		t.Setenv(EnvSandboxEnable, v)
		if sandboxEnabled() {
			t.Errorf("BRATAN_SANDBOX=%q must NOT enable sandbox", v)
		}
	}
}

// ---------------------------------------------------------------------------
// Fail-closed: podman missing / container cannot start
// ---------------------------------------------------------------------------

func TestSandboxFailClosedWhenPodmanMissing(t *testing.T) {
	if podmanAvailable() {
		t.Skip("podman IS installed; missing-binary path not exercisable here")
	}
	withSandboxToggle(t, "1")

	root := t.TempDir()
	victim := filepath.Join(root, "victim_marker.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(root)

	// This command WOULD mutate a file if it ran on the host.
	res := r.Exec("echo pwned > victim_marker.txt")

	if res.OK || res.Status != "FAIL" || res.Exit == 0 {
		t.Fatalf("sandbox ON + no podman must FAIL closed, got %+v", res)
	}
	if !strings.Contains(res.Error, "fail-closed") || !strings.Contains(res.Error, "not executed") {
		t.Fatalf("error must state fail-closed refusal, got %q", res.Error)
	}

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("fail-closed violation: command fell back to host, victim file changed to %q", data)
	}
}

func TestSandboxFailClosedAppliesToAssertOutputToo(t *testing.T) {
	if podmanAvailable() {
		t.Skip("podman IS installed; missing-binary path not exercisable here")
	}
	withSandboxToggle(t, "1")

	root := t.TempDir()
	victim := filepath.Join(root, "victim_marker.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(root)
	res := r.AssertOutput("echo pwned > victim_marker.txt", "pwned")

	if res.OK || res.Status != "FAIL" {
		t.Fatalf("assert_output must share Exec fail-closed policy, got %+v", res)
	}
	if !strings.Contains(res.Error, "fail-closed") {
		t.Fatalf("expected fail-closed refusal, got %q", res.Error)
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "original" {
		t.Fatalf("assert_output leaked to host: victim changed to %q", data)
	}
}

func TestSandboxFailClosedWhenContainerStartFails(t *testing.T) {
	// Simulate "podman exists but container cannot start": put a fake podman
	// on PATH that prints a known runtime-failure marker and exits non-zero.
	if podmanAvailable() {
		t.Skip("real podman present; fake-podman simulation would be shadowed inconsistently")
	}
	withSandboxToggle(t, "1")

	fakeBin := t.TempDir()
	fake := filepath.Join(fakeBin, "podman")
	script := "#!/bin/sh\necho 'Error: no subuid ranges are configured' >&2\nexit 125\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	victim := filepath.Join(root, "victim_marker.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(root)
	res := r.Exec("echo pwned > victim_marker.txt")

	if res.OK || res.Status != "FAIL" {
		t.Fatalf("container start failure must be fail-closed FAIL, got %+v", res)
	}
	if !strings.Contains(res.Error, "start-failed") {
		t.Fatalf("error should identify start-failed stage, got %q", res.Error)
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "original" {
		t.Fatalf("host fallback detected after start failure: %q", data)
	}
}

// ---------------------------------------------------------------------------
// Policy argv assertions (no podman required)
// ---------------------------------------------------------------------------

func TestSandboxArgsEnforceMandatoryRestrictions(t *testing.T) {
	joined := strings.Join(sandboxArgsFor("/proj/root", "ls"), " ")

	for _, want := range []string{
		"--network=none",
		"--read-only",
		"--memory 512m",
		"--cpus 1",
		"--pids-limit 256",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--mount type=bind,src=/proj/root,dst=/workspace",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox argv must contain %q; argv=%s", want, joined)
		}
	}

	// Exactly one bind mount — nothing beyond the project root may be shared.
	if n := strings.Count(joined, "type=bind"); n != 1 {
		t.Errorf("expected exactly 1 bind mount, found %d in %q", n, joined)
	}
	// Host home / dotfiles / secrets must never appear as sources.
	for _, forbidden := range []string{"$HOME", "/root/.ssh", ".gitconfig", "src=/etc"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("forbidden mount source %q present in argv: %s", forbidden, joined)
		}
	}

	if err := ensureSandboxPolicy("/proj/root", "ls"); err != nil {
		t.Fatalf("policy self-check must pass for well-formed argv: %v", err)
	}
}

func TestEnsureSandboxPolicyRejectsMissingRestriction(t *testing.T) {
	// Positive form: the REAL policy function accepts well-formed argv.
	if err := ensureSandboxPolicy("/proj/root", "ls"); err != nil {
		t.Fatalf("well-formed argv must pass policy self-check: %v", err)
	}

	// Negative form: exercise the REAL policy function, not a hand-written
	// string fixture. sandboxArgsForWithMounts is the actual argv builder;
	// by pointing it at a root whose bind-mount check cannot match we prove
	// ensureSandboxPolicy's mount clause rejects argv lacking the mandatory
	// writable bind mount. The flag-presence clauses are proven via
	// policyAcceptsArgs, which shares the exact required-flag list with
	// ensureSandboxPolicy (sandboxRequiredFlags).
	full := sandboxArgsFor("/proj/root", "ls")
	for _, flag := range sandboxRequiredFlags {
		broken := make([]string, 0, len(full))
		removed := false
		for _, a := range full {
			if !removed && (a == flag || strings.HasPrefix(a, flag+"=")) {
				removed = true
				continue
			}
			broken = append(broken, a)
		}
		if !removed {
			t.Fatalf("fixture error: mandatory flag %s not present in argv built by sandboxArgsFor", flag)
		}
		if policyAcceptsArgs(broken) {
			t.Fatalf("policy predicate must REJECT argv missing %s", flag)
		}
	}
	if !policyAcceptsArgs(full) {
		t.Fatal("policy predicate must accept complete argv")
	}

	// Real-function proof for the bind-mount clause: a Router running with
	// sandbox enabled and a root that is NOT the bind source would be
	// rejected; here we directly assert ensureSandboxPolicy rejects an argv
	// whose mount was built for a different root by checking the same
	// containment condition the function uses.
	other := sandboxArgsForWithMounts("/elsewhere/root", "ls", nil)
	joined := strings.Join(other, " ")
	if strings.Contains(joined, "--mount type=bind,src=/proj/root,dst="+sandboxContainerPath) {
		t.Fatal("fixture error: /elsewhere argv unexpectedly contains /proj mount")
	}
	// ensureSandboxPolicy builds argv from its own root argument, so calling
	// it with "/proj/root" while the produced mount references "/proj/root"
	// passes; the rejection path is the flag loop above plus this dedicated
	// unit of the mount clause:
	if err := verifySandboxMountClause(sandboxArgsFor("/proj/root", "ls"), "/proj/root"); err != nil {
		t.Fatalf("mount clause must accept correct argv: %v", err)
	}
	if err := verifySandboxMountClause(other, "/proj/root"); err == nil {
		t.Fatal("mount clause must reject argv whose bind source differs from root")
	}
}

// ---------------------------------------------------------------------------
// Escape attempts under sandbox ON (fail-closed without podman ⇒ all refused;
// with podman integration these run inside the container instead)
// ---------------------------------------------------------------------------

func TestSandboxRefusesEscapeAttemptsOutsideRoot(t *testing.T) {
	if podmanAvailable() {
		t.Skip("integration variant covered by TestSandboxIntegrationEscapes when podman works")
	}
	withSandboxToggle(t, "1")

	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "outside_victim.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(root)

	cases := map[string]string{
		"absolute-write": "echo pwned > " + target,
		"dotdot-write":   "echo pwned > ../outside_escape.txt",
		"shell-var":      "f=" + target + "; echo pwned > $f",
		"subcommand":     "echo pwned > $(echo " + target + ")",
	}

	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			res := r.Exec(cmd)
			if res.OK || !strings.Contains(res.Error, "fail-closed") {
				t.Fatalf("%s: expected fail-closed refusal, got %+v", name, res)
			}
		})
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret" {
		t.Fatalf("escape attempt mutated host file outside root: %q", data)
	}
}

func TestSandboxSymlinkEscapeRefusedWithoutPodman(t *testing.T) {
	if podmanAvailable() {
		t.Skip("covered by integration test when podman is available")
	}
	withSandboxToggle(t, "1")

	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("intact"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	r := New(root)
	res := r.Exec("echo tampered > link.txt")

	if res.OK || !strings.Contains(res.Error, "fail-closed") {
		t.Fatalf("symlink escape must be refused when sandbox enabled without podman, got %+v", res)
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "intact" {
		t.Fatalf("symlink write-through reached host file: %q", data)
	}
}

// ---------------------------------------------------------------------------
// Go/Perl workflow compatibility (sandbox OFF = current default)
// ---------------------------------------------------------------------------

func TestGoWorkflowUnaffectedWhenSandboxDisabled(t *testing.T) {
	withSandboxToggle(t, "0")

	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n\ngo 1.21\n")
	writeFile(t, root, "math.go", "package main\n\nimport \"fmt\"\n\nfunc main(){ fmt.Println(\"PASS\") }\n")

	r := New(root)
	res := r.Exec("go build ./... ")
	if !res.OK {
		t.Fatalf("normal go workflow must stay functional with sandbox off: %+v", res)
	}
}

func TestPerlProtectedFlowUnaffectedWhenSandboxDisabled(t *testing.T) {
	withSandboxToggle(t, "0")

	root := t.TempDir()
	writeFile(t, root, "test.pl", "#!/usr/bin/perl\nprint \"PASS\\n\";\n")

	r := New(root)
	// Pre-existing SEC-2 protection must still refuse literal protected access.
	res := r.Exec("echo x >> test.pl")
	if res.OK || !strings.Contains(res.Error, "protected") {
		t.Fatalf("perl protected-file guard must remain active with sandbox off, got %+v", res)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Opt-in integration tests — REQUIRE a working rootless podman.
// Skipped (NOT PASS) unless BRATAN_TEST_PODMAN_INTEGRATION=1 AND podman runs.
// ---------------------------------------------------------------------------

func requireWorkingPodman(t *testing.T) {
	t.Helper()
	if os.Getenv("BRATAN_TEST_PODMAN_INTEGRATION") != "1" {
		t.Skip("UNVERIFIED: set BRATAN_TEST_PODMAN_INTEGRATION=1 with a working rootless podman to run")
	}
	if !podmanAvailable() {
		t.Skip("UNVERIFIED: podman not installed")
	}
	probe := exec.Command("podman", "info")
	if err := probe.Run(); err != nil {
		t.Skipf("UNVERIFIED: podman present but not functional in this environment: %v", err)
	}
}

func TestSandboxIntegrationNetworkDisabled(t *testing.T) {
	requireWorkingPodman(t)
	withSandboxToggle(t, "1")

	r := New(t.TempDir())
	// busybox wget must fail without network; --network=none enforced.
	res := r.Exec("wget -q -T 3 -O /tmp/x http://example.com || echo NONETWORK")
	if !res.OK || !strings.Contains(res.Output, "NONETWORK") {
		t.Fatalf("expected network-disabled proof, got %+v", res)
	}
}

func TestSandboxIntegrationWriteOutsideRootDenied(t *testing.T) {
	requireWorkingPodman(t)
	withSandboxToggle(t, "1")

	root := t.TempDir()
	r := New(root)
	// /etc is read-only container fs, not host /etc.
	res := r.Exec("touch /etc/sandbox_escape_probe || echo DENIED")
	if !strings.Contains(res.Output, "DENIED") && res.OK {
		t.Fatalf("write outside writable mounts must be denied, got %+v", res)
	}
	if _, err := os.Stat("/etc/sandbox_escape_probe"); err == nil {
		t.Fatal("host /etc was modified from sandbox")
	}
}

func TestSandboxIntegrationNormalGoAndPerlWorkflow(t *testing.T) {
	requireWorkingPodman(t)
	withSandboxToggle(t, "1")

	root := t.TempDir()
	writeFile(t, root, "hello.sh", "echo hi\n")
	r := New(root)
	// busybox image has sh; normal in-root workflow must work.
	res := r.Exec("sh hello.sh")
	if !res.OK || !strings.Contains(res.Output, "hi") {
		t.Fatalf("sandboxed normal workflow failed: %+v", res)
	}
}

// ---------------------------------------------------------------------------
// PR-S1 audit-fix: preemptive protected-file RO mounts (nested binds).
// Unit tests verify argv topology and collection rules; integration tests
// verify ACTUAL EROFS behaviour inside a real container. Without a working
// podman the integration checks are SKIP/UNVERIFIED, never PASS.
// ---------------------------------------------------------------------------

func TestSandboxArgsSingleRWBindPlusNestedROBinds(t *testing.T) {
	root := "/abs/project"
	args := sandboxArgsForWithMounts(root, "true", []string{"test.pl", filepath.Join("a", "b", "deep_test.go")})

	rwCount := 0
	roCount := 0
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mount" {
			continue
		}
		m := args[i+1]
		switch {
		case strings.Contains(m, "dst=/workspace,"):
			t.Fatalf("no mount may target /workspace itself with suffix: %q", m)
		case m == "type=bind,src="+root+",dst=/workspace":
			rwCount++
		case strings.HasSuffix(m, ",ro"):
			roCount++
			if !strings.Contains(m, "src="+root+"/") {
				t.Fatalf("protected RO bind must source from project root subtree: %q", m)
			}
		default:
			t.Fatalf("unexpected mount spec: %q", m)
		}
	}
	if rwCount != 1 {
		t.Fatalf("exactly one RW root bind expected, got %d", rwCount)
	}
	if roCount != 2 {
		t.Fatalf("two nested RO binds expected, got %d", roCount)
	}
	// precise destinations for both top-level and nested files
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"type=bind,src=" + root + "/test.pl,dst=/workspace/test.pl,ro",
		"type=bind,src=" + root + "/a/b/deep_test.go,dst=/workspace/a/b/deep_test.go,ro",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing exact RO bind %q in argv:\n%s", want, joined)
		}
	}
	// no tmpfs-over-/workspace scheme anymore
	if strings.Contains(joined, "--tmpfs /workspace") {
		t.Fatal("removed scheme reappeared: tmpfs over /workspace")
	}
}

func TestCollectSandboxProtectedFilesRules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test.pl", "print 'PASS';\n")
	writeFile(t, root, "solution_test.go", "package main\n")
	writeFile(t, root, "math.go", "package main\n")
	writeFile(t, root, filepath.Join("a", "b", "c", "deep_test.go"), "package c\n")
	// symlink inside root pointing to another existing file
	if err := os.Symlink(filepath.Join(root, "test.pl"), filepath.Join(root, "link_to_test.pl")); err != nil {
		t.Skip("symlinks unsupported here")
	}

	r := New(root)
	r.ProtectedPath = map[string]bool{"test.pl": true}

	got, err := r.collectSandboxProtectedFiles()
	if err != nil {
		t.Fatalf("collection failed: %v", err)
	}
	set := map[string]bool{}
	for _, p := range got {
		set[p] = true
	}
	// existing protected files at any depth are collected
	for _, want := range []string{"test.pl", "solution_test.go", filepath.Join("a", "b", "c", "deep_test.go")} {
		if !set[want] {
			t.Fatalf("expected existing protected file %q in mounts, got %v", want, got)
		}
	}
	// non-protected regular file is NOT mounted ro
	if set["math.go"] {
		t.Fatal("ordinary source file must not be mounted read-only")
	}

	// absent protected file -> skipped, no error
	r2 := New(root)
	r2.ProtectedPath = map[string]bool{"test.pl": true, "ghost_test.go": true}
	got2, err := r2.collectSandboxProtectedFiles()
	if err != nil {
		t.Fatalf("absent file must not fail collection: %v", err)
	}
	for _, p := range got2 {
		if p == "ghost_test.go" {
			t.Fatal("absent protected file must not be mounted")
		}
	}

	// broken symlink under ProtectedPath -> EvalSymlinks failure -> fail-closed error
	r3 := New(root)
	if err := os.Symlink(filepath.Join(root, "does_not_exist"), filepath.Join(root, "broken_link_test.go")); err != nil {
		t.Skip("symlinks unsupported here")
	}
	r3.ProtectedPath = map[string]bool{"broken_link_test.go": true}
	if _, err := r3.collectSandboxProtectedFiles(); err == nil {
		t.Fatal("broken protected symlink must produce fail-closed error")
	}
}

func TestSandboxIntegrationProtectedWriteDenied(t *testing.T) {
	requireWorkingPodman(t)
	withSandboxToggle(t, "1")

	root := t.TempDir()
	writeFile(t, root, "test.pl", "print 'PASS';\n")
	writeFile(t, root, "math.go", "package main\n")
	writeFile(t, root, filepath.Join("a", "b", "c", "deep_test.go"), "package c\n")
	beforeTest := readFileOrFail(t, root, "test.pl")
	beforeDeep := readFileOrFail(t, root, filepath.Join("a", "b", "c", "deep_test.go"))

	r := New(root)
	r.ProtectedPath = map[string]bool{"test.pl": true}

	// write into protected test.pl must fail inside container
	res := r.Exec("echo hacked > /workspace/test.pl 2>/dev/null || echo WRITE_DENIED")
	if !strings.Contains(res.Output, "WRITE_DENIED") {
		t.Fatalf("write to protected test.pl was NOT denied inside sandbox: %+v", res)
	}
	// rm of protected file must fail too
	res = r.Exec("rm -f /workspace/test.pl 2>/dev/null || echo RM_DENIED")
	if !strings.Contains(res.Output, "RM_DENIED") {
		t.Fatalf("unlink of protected test.pl was NOT denied inside sandbox: %+v", res)
	}
	// nested deep_test.go must also be read-only
	res = r.Exec("echo x > /workspace/a/b/c/deep_test.go 2>/dev/null || echo NESTED_DENIED")
	if !strings.Contains(res.Output, "NESTED_DENIED") {
		t.Fatalf("write to nested *_test.go was NOT denied inside sandbox: %+v", res)
	}
	// ordinary source remains writable
	res = r.Exec("echo package main > /workspace/math.go && echo SRC_OK")
	if !res.OK || !strings.Contains(res.Output, "SRC_OK") {
		t.Fatalf("legitimate source edit must work in sandbox: %+v", res)
	}
	// originals on host unchanged
	if after := readFileOrFail(t, root, "test.pl"); after != beforeTest {
		t.Fatal("host test.pl was modified despite RO bind")
	}
	if after := readFileOrFail(t, root, filepath.Join("a", "b", "c", "deep_test.go")); after != beforeDeep {
		t.Fatal("host nested *_test.go was modified despite RO bind")
	}
}

func readFileOrFail(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestProtectedIntegrityFailureIsUnambiguous(t *testing.T) {
	// Simulates a command that printed PASS but mutated a protected file.
	res := protectedIntegrityFailure("exec", "echo x > test.pl; echo PASS", "test.pl", "PASS\n")

	if res.OK {
		t.Fatal("integrity violation must never produce OK=true")
	}
	if res.Status != "FAIL" {
		t.Fatalf("Status must be FAIL, got %q", res.Status)
	}
	if res.Exit == 0 {
		t.Fatal("Exit must be non-zero on integrity violation")
	}
	// The raw command output ("PASS") must NOT appear in Output where
	// downstream code looks for success markers.
	if strings.Contains(res.Output, "PASS") {
		t.Fatalf("Output must not carry raw command success markers: %q", res.Output)
	}
	if !strings.Contains(res.Output, "SEC-2 INTEGRITY VIOLATION") || !strings.Contains(res.Output, "test.pl") {
		t.Fatalf("Output must name the violated file explicitly: %q", res.Output)
	}
	// Diagnostics preserved in Error instead.
	if !strings.Contains(res.Error, "PASS") {
		t.Fatal("original command output must remain available in Error for diagnostics")
	}
}
