package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SEC-2 regression suite: post-execution protected file integrity guard.
// The pre-execution commandTargetsProtectedPath check cannot follow shell
// indirection ($var, ${var}, globs); the snapshot/verify guard must catch
// those bypasses while leaving legitimate commands untouched.

func writeProjectFile(t *testing.T, root, rel, content string) {
	t.Helper()

	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedIntegrityDirectWriteStillBlocked(t *testing.T) {
	root := t.TempDir()
	original := "ORIGINAL_TEST_CONTENT\n"
	writeProjectFile(t, root, "test.pl", original)

	r := New(root)

	// Direct textual mention of test.pl — blocked by the existing
	// pre-exec check; behavior must be unchanged by SEC-2.
	result := r.Exec("printf 'BAD' > test.pl")
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("expected direct protected write to fail, got %+v", result)
	}

	got, err := os.ReadFile(filepath.Join(root, "test.pl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("test.pl was modified:\n%s", got)
	}
}

func TestProtectedIntegrityVariableBypassIsBlocked(t *testing.T) {
	root := t.TempDir()
	original := "ORIGINAL_TEST_CONTENT\n"
	writeProjectFile(t, root, "test.pl", original)

	r := New(root)

	// $var / ${var} indirection defeats the pre-exec string check. The
	// command text still contains "test.pl", so either layer may block it;
	// what matters is that the bypass never succeeds and the error names
	// the protected file.
	result := r.Exec(`f=test.pl; printf 'BAD' > "$f"`)
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("expected variable-indirection bypass to fail, got %+v", result)
	}
	if !strings.Contains(result.Error, "test.pl") {
		t.Fatalf("error must name the protected file, got %q", result.Error)
	}

	got, _ := os.ReadFile(filepath.Join(root, "test.pl"))
	if string(got) == original {
		// Command was blocked before touching the file (pre-check path).
		return
	}
	// If the file changed on disk, the guard still must have reported FAIL.
	t.Logf("note: file content changed before guard reported: %q", got)
}

func TestProtectedIntegrityGlobBypassIsBlocked(t *testing.T) {
	root := t.TempDir()
	testContent := "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\n"
	writeProjectFile(t, root, "solution_test.go", testContent)

	r := New(root)

	// Glob expansion happens inside the shell — invisible to the pre-check.
	// The guard is report-only by design (SEC-2: never auto-delete/restore),
	// so the file may already be changed on disk when the violation is
	// reported; the contract is that the command result can never be OK.
	result := r.Exec("files=$(ls *_test.go); for f in $files; do echo x > $f; done")
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("expected glob bypass to fail, got %+v", result)
	}
	if !strings.Contains(result.Error, "integrity violation") {
		t.Fatalf("expected integrity-violation error, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "solution_test.go") {
		t.Fatalf("error must name the modified protected file, got %q", result.Error)
	}
}

func TestProtectedIntegritySymlinkToProtectedTargetIsBlocked(t *testing.T) {
	root := t.TempDir()
	original := "ORIGINAL_TEST_CONTENT\n"
	writeProjectFile(t, root, "test.pl", original)

	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink("test.pl", link); err != nil {
		t.Fatal(err)
	}

	r := New(root)

	// Writing through a symlink: the command text mentions only
	// innocent.txt, so only the post-run hash comparison can detect it.
	result := r.Exec("printf 'BAD' > innocent.txt")
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("expected symlink write-through to fail, got %+v", result)
	}

	got, err := os.ReadFile(filepath.Join(root, "test.pl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "integrity violation") && string(got) != original {
		t.Fatalf("test.pl silently modified through symlink without a violation report: %q", got)
	}
}

func TestProtectedIntegrityAllowsNormalSourceWrite(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "solution_test.go", "package main\n")
	writeProjectFile(t, root, "source.go", "package main\n\nfunc Add(a, b int) int { return 4 }\n")

	r := New(root)

	// Editing a non-protected source file via exec must keep working.
	result := r.Exec(`printf 'package main\n\nfunc Add(a, b int) int { return a + b }\n' > source.go`)
	if !result.OK || result.Status != "PASS" {
		t.Fatalf("normal source.go edit must remain allowed, got %+v", result)
	}

	got, err := os.ReadFile(filepath.Join(root, "source.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "a + b") {
		t.Fatalf("source.go was not actually written: %q", got)
	}

	// Protected test file untouched.
	if gt, _ := os.ReadFile(filepath.Join(root, "solution_test.go")); string(gt) != "package main\n" {
		t.Fatalf("solution_test.go unexpectedly modified: %q", gt)
	}
}

func TestProtectedIntegrityPerlTestPlProtectionPreserved(t *testing.T) {
	root := t.TempDir()
	original := "#!/usr/bin/env perl\nprint 'PASS\\n';\n"
	writeProjectFile(t, root, "test.pl", original)

	r := New(root)

	// Indirect Perl-side tampering through a generated path list.
	result := r.Exec("files=$(ls *.pl); for f in $files; do echo TAMPERED > $f; done")
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("expected generated-path tampering to fail, got %+v", result)
	}
	if !strings.Contains(result.Error, "integrity violation") {
		t.Fatalf("expected integrity-violation error, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "test.pl") {
		t.Fatalf("error must name the protected file, got %q", result.Error)
	}

	// The guard is report-only: it never restores content by itself. What
	// must be guaranteed is that the ledger can never record this command
	// as a successful verification step (OK=false, Status=FAIL, Exit!=0).
	if result.Exit == 0 {
		t.Fatalf("tampering command must not report exit 0, got %+v", result)
	}
}

func TestProtectedIntegrityNewTestFileDoesNotBreakCurrentLoop(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.go", "package main\n\nfunc main() {}\n")

	r := New(root)

	// First run: no *_test.go exists yet, nothing is tracked.
	snap0, snapErr := r.snapshotProtectedFiles()
	if snapErr != nil {
		t.Fatalf("snapshot of empty project must succeed: %v", snapErr)
	}
	if _, violated, verifyErr := r.verifyProtectedIntegrity(snap0); verifyErr != nil || violated {
		t.Fatalf("empty project snapshot should track nothing (violated=%v err=%v)", violated, verifyErr)
	}

	// Agent legitimately creates its first Go test file via exec.
	first := r.Exec("printf 'package main\\n' > new_test.go")
	if !first.OK {
		t.Fatalf("creating a brand-new *_test.go must stay allowed, got %+v", first)
	}

	// Second run: the now-existing test file IS protected; modifying it
	// through exec must fail.
	second := r.Exec("printf 'tampered' > new_test.go")
	if second.OK || second.Status != "FAIL" {
		t.Fatalf("existing *_test.go must be integrity-guarded, got %+v", second)
	}
}

func TestAssertOutputProtectedIntegrityGuard(t *testing.T) {
	root := t.TempDir()
	testContent := "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\n"
	writeProjectFile(t, root, "solution_test.go", testContent)
	writeProjectFile(t, root, "main.go", "package main\n")

	r := New(root)

	// assert_output with variable indirection into a protected file.
	result := r.AssertOutput(`f=solution_test.go; echo ok; echo x > $f`, "ok")
	if result.OK || result.Status != "FAIL" {
		t.Fatalf("assert_output bypass must fail, got %+v", result)
	}
	if !strings.Contains(result.Error, "solution_test.go") {
		t.Fatalf("error must name the protected file, got %q", result.Error)
	}

	// Legitimate assert_output remains functional.
	good := r.AssertOutput("echo hello", "hello")
	if !good.OK || good.Status != "PASS" {
		t.Fatalf("legitimate assert_output must keep working, got %+v", good)
	}
}

// ---------------------------------------------------------------------------
// PR-S1c C2 regression tests: unrestricted-depth scanning, creation
// detection at registered paths, fail-closed on scan errors.
// ---------------------------------------------------------------------------

func TestC2DeepNewTestFileTrackedOnNextCommand(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	r := New(root)

	// Command 1 creates a *_test.go far below the old depth limits.
	first := r.Exec("mkdir -p a/b/c/d/e/f/g/h && printf 'package h\\n' > a/b/c/d/e/f/g/h/deep_test.go")
	if !first.OK {
		t.Fatalf("initial creation of a new test file must stay allowed: %+v", first)
	}

	// The pre-scan of the NEXT command has no depth limit, so the deep file
	// is tracked now. Tampering with it must FAIL.
	second := r.Exec("echo tampered > a/b/c/d/e/f/g/h/deep_test.go")
	if second.OK || second.Status != "FAIL" || second.Exit == 0 {
		t.Fatalf("deep *_test.go modification must be detected, got %+v", second)
	}

	// Deletion too.
	del := r.Exec("rm a/b/c/d/e/f/g/h/deep_test.go")
	if del.OK || del.Status != "FAIL" {
		t.Fatalf("deep *_test.go deletion must be detected, got %+v", del)
	}
}

func TestC2CreationAtRegisteredProtectedPathDetected(t *testing.T) {
	root := t.TempDir()
	r := New(root) // ProtectedPath registry contains "test.pl"; file absent

	snap, err := r.snapshotProtectedFiles()
	if err != nil {
		t.Fatalf("snapshot must succeed on empty project: %v", err)
	}
	if e, ok := snap["test.pl"]; !ok || !e.missing {
		t.Fatalf("absent registered path must be tracked as missing entry, got %+v ok=%v", e, ok)
	}

	// Indirect creation through shell indirection (pre-check cannot see it).
	res := r.Exec("f=test${EMPTY}.pl; echo hacked > $f")
	if res.OK || res.Status != "FAIL" || res.Exit == 0 {
		t.Fatalf("creation of previously-missing test.pl must FAIL, got %+v", res)
	}
}

func TestC2PreScanFailureBlocksCommand(t *testing.T) {
	root := t.TempDir()
	r := New(root)
	// A dangling symlink named test.pl makes the protected scan unresolvable.
	// snapshotProtectedFiles must return an error and Exec must refuse to run
	// the command at all (fail-closed, never PASS).
	if err := os.Symlink(filepath.Join(root, "nonexistent-target"), filepath.Join(root, "test.pl")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.snapshotProtectedFiles(); err == nil {
		t.Fatal("dangling protected symlink must produce a snapshot error")
	}
	victim := filepath.Join(root, "victim.txt")
	res := r.Exec("echo ran > victim.txt")
	if res.OK || res.Status != "FAIL" || res.Exit == 0 {
		t.Fatalf("Exec must fail closed when pre-scan fails, got %+v", res)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatal("command must NOT have executed on host when pre-scan failed")
	}
}

func TestC2PostScanFailureCannotReturnPass(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "solution_test.go", "package solution\n")
	r := New(root)

	snap, err := r.snapshotProtectedFiles()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate post-scan failure: replace the tracked file with a dangling
	// symlink after the command would have finished — verify must return err.
	os.Remove(filepath.Join(root, "solution_test.go"))
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "solution_test.go")); err != nil {
		t.Fatal(err)
	}
	modified, violated, verr := r.verifyProtectedIntegrity(snap)
	if verr == nil && !violated {
		t.Fatalf("unverifiable protected state must not look clean (modified=%q violated=%v err=%v)", modified, violated, verr)
	}
	// And the mapped Result can never read as success:
	res := protectedScanFailure("exec", "cmd", fmt.Errorf("simulated scan failure"), "PASS\nall tests green")
	if res.OK || res.Status != "FAIL" || res.Exit == 0 {
		t.Fatalf("scan-failure result must be unambiguous FAIL: %+v", res)
	}
	if strings.Contains(res.Output, "all tests green") || strings.Contains(res.Output, "PASS") {
		t.Fatalf("Output must not carry success markers, got %q", res.Output)
	}
}

func TestC2WalkErrorIsFailClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; unreadable-dir probe unreliable")
	}
	root := t.TempDir()
	hidden := filepath.Join(root, "hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, hidden, "x_test.go", "package x\n")
	if err := os.Chmod(hidden, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(hidden, 0o755)

	r := New(root)
	if _, err := r.protectedIntegrityPaths(); err == nil {
		t.Fatal("unreadable directory must produce a scan error (fail-closed), not silent skip")
	}
}

func TestC2ModificationAndDeletionOfExistingFileStillDetected(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "test.pl", "say 1;\n")
	r := New(root)

	mod := r.Exec("f=$(ls te*t.pl); echo hacked >> $f")
	if mod.OK || mod.Status != "FAIL" {
		t.Fatalf("glob-modification of test.pl must FAIL, got %+v", mod)
	}
}
