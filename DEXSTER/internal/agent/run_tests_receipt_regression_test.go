package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
	"dexster/internal/tools"
	"kraken/memory"
)

// Regression: run_tests ok=true at step #31 was not sufficient if the
// executable didn't literally print PASS, so the model continued writing.
func TestRunTestsSuccessReceiptWithoutPASSCompletesGate(t *testing.T) {
	root := t.TempDir()
	a, err := New(root, "mock", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	a.AutoVerify = true
	a.State = &TaskState{
		Task:     "Create a Go program",
		Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{}},
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.applyEvidence(llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": "package main\nfunc main() {}\n"}}, tools.Result{OK: true, Exit: 0}, 1)
	a.applyEvidence(llm.Call{Name: "git_diff"}, tools.Result{OK: true, Exit: 0}, 2)
	a.applyEvidence(llm.Call{Name: "run_tests"}, tools.Result{Tool: "run_tests", Output: "", OK: true, Exit: 0}, 3)
	if a.State.Evidence.LastTestIter != 3 {
		t.Fatal("success receipt was discarded")
	}
	if !a.gateComplete() {
		t.Fatal("controller failed to complete despite exit 0")
	}
	if check, needed := a.nextVerification(); needed {
		t.Fatalf("unexpected check %s", check)
	}
}

func TestRunTestsFailureReceiptCannotCompleteGate(t *testing.T) {
	root := t.TempDir()
	a, err := New(root, "mock", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	a.AutoVerify = true
	a.State = &TaskState{
		Task:     "Create a Go program",
		Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{}},
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.applyEvidence(llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": "package main\nfunc main() {}\n"}}, tools.Result{OK: true, Exit: 0}, 1)
	a.applyEvidence(llm.Call{Name: "git_diff"}, tools.Result{OK: true, Exit: 0}, 2)
	a.applyEvidence(llm.Call{Name: "run_tests"}, tools.Result{Tool: "run_tests", Output: "PASS (but process failed)", OK: false, Exit: 1}, 3)
	if a.State.Evidence.LastTestIter != 0 || a.gateComplete() {
		t.Fatal("failed test was accepted")
	}
}

// The real Go runner can succeed with no text output. This must end the
// full agent loop at step #4 without a further model call or another write.
func TestRunTestsWithoutPASSCompletesEndToEnd(t *testing.T) {
	root := t.TempDir()
	calls := 0
	server, requests := mockServer(t, func(_ string) string {
		calls++
		switch calls {
		case 1:
			return `{"name":"list_files","path":"."}`
		case 2:
			action := map[string]string{"name": "write_file", "path": "main.go", "content": "package main\nfunc main() {}\n"}
			data, err := json.Marshal(action)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		default:
			t.Errorf("unexpected model request %d after successful Go verification", calls)
			return `{"name":"list_files","path":"."}`
		}
	})
	a := newAgent(t, root, server.URL, 4)
	a.AutoVerify = true
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory = store
	task := "Create a Go program that exits successfully"
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done || a.State.Iteration != 4 {
		t.Fatalf("not completed at iteration 4: %+v", a.State)
	}
	if *requests != 2 {
		t.Fatalf("expected two model calls, got %d", *requests)
	}
	journal := a.State.Journal
	if len(journal) != 4 || journal[3].Tool != "run_tests" || !journal[3].OK {
		t.Fatalf("verification sequence: %+v", journal)
	}
	if strings.Contains(journal[3].Summary, "PASS") {
		t.Fatalf("fixture unexpectedly emitted PASS: %s", journal[3].Summary)
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil || !manifest.Done || manifest.AcknowledgedIteration != 4 {
		t.Fatalf("KRAKEN acknowledgement mismatch: %+v, err=%v", manifest, err)
	}
}
