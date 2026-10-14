package tools

import (
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
	if _, violated := r.verifyProtectedIntegrity(r.snapshotProtectedFiles()); violated {
		t.Fatal("empty project snapshot should track nothing")
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
