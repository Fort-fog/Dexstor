package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoTestModule creates a minimal self-contained Go module inside dir:
// a trivial library plus a passing test. It is used to verify that the
// Go branch of Router.Test produces output compatible with the ActionLedger
// PASS contract.
func writeGoTestModule(t *testing.T, dir string) {
	t.Helper()

	goMod := "module autograder_fixture\n\ngo 1.22\n"

	lib := "package solution\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"

	testSrc := "package solution\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2,3) != 5\")\n\t}\n}\n"

	files := map[string]string{
		"go.mod":           goMod,
		"solution.go":      lib,
		"solution_test.go": testSrc,
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestRouterTestGoSuccessOutputContainsPASS(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	root := t.TempDir()

	writeGoTestModule(t, root)

	r := New(root)

	result := r.Test("solution.go")

	if result.Error != "" {
		t.Fatalf("go test failed: %s (output=%q)", result.Error, result.Output)
	}

	if !result.OK {
		t.Fatal("successful go test must produce OK result")
	}

	if result.Exit != 0 {
		t.Fatalf("successful go test must have zero exit, got %d", result.Exit)
	}

	if result.Status != "PASS" {
		t.Fatalf("successful go test must have PASS status, got %q", result.Status)
	}

	if !strings.Contains(result.Output, "PASS") {
		t.Fatalf("successful go test output must contain PASS marker, got %q", result.Output)
	}
}

func TestRouterTestGoFailureKeepsContract(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	root := t.TempDir()

	goMod := "module autograder_fixture\n\ngo 1.22\n"

	lib := "package solution\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"

	failingTest := "package solution\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) == 5 {\n\t\tt.Fatal(\"forced failure\")\n\t}\n}\n"

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "solution.go"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "solution_test.go"), []byte(failingTest), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(root)

	result := r.Test("solution.go")

	if result.OK {
		t.Fatal("failing go test must not be marked OK")
	}

	if result.Status != "FAIL" {
		t.Fatalf("failing go test must have FAIL status, got %q", result.Status)
	}

	if result.Exit == 0 {
		t.Fatal("failing go test must have non-zero exit")
	}

	/*
		A failed test must NOT carry the injected PASS marker; the adapter
		applies only to successful runs.
	*/
	if strings.Contains(result.Output, "PASS\n") && !strings.Contains(result.Output, "--- FAIL") {
		t.Fatalf("failed test output must not gain PASS marker: %q", result.Output)
	}
}
