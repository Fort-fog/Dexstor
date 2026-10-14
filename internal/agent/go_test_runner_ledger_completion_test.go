package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"bratan-go/internal/tools"
)

/*
PR-5 regression: a successful Go test executed through the real
GoTestRunner (tools.Router.Test) must produce Result.Output that is
compatible with the ActionLedger PASS contract:

  - Status == "PASS", OK == true, Exit == 0
  - validateActionLedgerResultIntegrity accepts the recorded result
  - validateCompletion sees the successful test PASS action
*/
func TestGoTestRunnerOutputPassesLedgerIntegrityAndCompletion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	root := t.TempDir()

	files := map[string]string{
		"go.mod":           "module autograder_fixture\n\ngo 1.22\n",
		"solution.go":      "package solution\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		"solution_test.go": "package solution\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2,3) != 5\")\n\t}\n}\n",
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	router := tools.New(root)

	result := router.Test("solution.go")

	if !result.OK || result.Exit != 0 || result.Status != "PASS" {
		t.Fatalf("expected successful PASS result, got ok=%v exit=%d status=%q err=%q",
			result.OK, result.Exit, result.Status, result.Error)
	}

	if !strings.Contains(result.Output, "PASS") {
		t.Fatalf("Result.Output must contain PASS marker, got %q", result.Output)
	}

	/*
		Record the real runner output into an ActionLedger test entry and
		run it through the untouched ledger validator.
	*/
	testRecord := resultAction(
		5,
		"test",
		result.Output,
		result.OK,
		result.Exit,
		"solution.go",
	)

	ledger := []ActionRecord{testRecord}

	if err := validateActionLedgerResultIntegrity(ledger); err != nil {
		t.Fatalf("Go test output must pass ledger result integrity: %v", err)
	}

	/*
		A full completion sequence ending with the real Go test PASS must
		satisfy validateCompletion without changing any validator logic.
	*/
	a := &Agent{
		ActionLedger: []ActionRecord{
			completionAction(1, "inspect_project", "files=3"),
			completionAction(2, "read_file", "source", "solution.go"),
			completionAction(3, "write_file", "write completed", "solution.go", "package solution\n"),
			completionAction(4, "git_diff", "modified:\n  solution.go\n"),
			testRecord,
		},
	}

	if err := a.validateCompletion(); err != nil {
		t.Fatalf("validateCompletion must accept the Go test PASS output: %v", err)
	}
}

/*
Guard: if the runner ever regresses to output without a PASS marker,
the ledger validator must reject it — proving the test above actually
depends on the fix.
*/
func TestGoTestRunnerLedgerRejectsOutputWithoutPASS(t *testing.T) {
	ledger := []ActionRecord{
		resultAction(1, "test", "ok  \tautograder_fixture\t0.001s", true, 0, "solution.go"),
	}

	if err := validateActionLedgerResultIntegrity(ledger); err == nil {
		t.Fatal("successful test without PASS must be rejected by ledger integrity")
	}
}
