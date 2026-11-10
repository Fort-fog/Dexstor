package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dexster/internal/llm"
)

const brokenMath = "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
const fixedMath = "package main\n\nfunc Add(a, b int) int {\n\treturn 10\n}\n"
const oracleTest = `package main

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 10 {
		t.Fatalf("Add(2,3) = %d, want 10", Add(2, 3))
	}
}
`

// mockServer serves scripted model responses; the responder selects from the
// prompt content (evidence-driven), so tests do not depend on exact request
// numbering.
func mockServer(t *testing.T, respond func(prompt string) string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"response": respond(req.Prompt), "done": true})
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func newFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "math.go"), []byte(brokenMath), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "math_test.go"), []byte(oracleTest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newAgent(t *testing.T, root, url string, max int) *Agent {
	t.Helper()
	a, err := New(root, "test-model", llm.New(url), max)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// evidenceDrivenMock plays the canonical repair strategy: read -> write ->
// git_diff -> run_tests, selected from the evidence line in the prompt.
func evidenceDrivenMock() func(prompt string) string {
	return func(prompt string) string {
		switch {
		case strings.Contains(prompt, "wrote=-"):
			if strings.Contains(prompt, "reads=[math.go]") {
				return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
			}
			return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
		case strings.Contains(prompt, "diff_after_write=no"):
			return "TOOL_CALL\nname: git_diff\nEND_TOOL_CALL"
		default:
			return "TOOL_CALL\nname: run_tests\nEND_TOOL_CALL"
		}
	}
}

func TestFullRepairFlowToDone(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, root, server.URL, 10)

	if err := a.Run("Fix math.go so Add(2, 3) returns 10"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if *requests != 4 {
		t.Fatalf("expected exactly 4 model requests (read, write, diff, test), got %d", *requests)
	}

	data, err := os.ReadFile(filepath.Join(root, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "return 10") {
		t.Fatalf("math.go not repaired:\n%s", data)
	}
	if !a.State.Done {
		t.Fatal("state must be Done")
	}

	wantOrder := []string{"read_file", "write_file", "git_diff", "run_tests"}
	for i, want := range wantOrder {
		if a.State.Journal[i].Tool != want || !a.State.Journal[i].OK {
			t.Fatalf("journal[%d] = %+v, want tool %s ok", i, a.State.Journal[i], want)
		}
	}

	st, _, err := loadState(root, "Fix math.go so Add(2, 3) returns 10")
	if err != nil || st == nil || !st.Done {
		t.Fatalf("persisted state not Done: %+v err=%v", st, err)
	}
}

func TestCorruptedStateRecovery(t *testing.T) {
	root := newFixture(t)
	dir := filepath.Join(root, ".dexster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := `{"version":1,"task":"`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	server, requests := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, root, server.URL, 10)
	if err := a.Run("Fix math.go so Add(2, 3) returns 10"); err != nil {
		t.Fatalf("run after corrupt state failed: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	var quarantined bool
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatalf("corrupt state was not quarantined: %v", entries)
	}
	if *requests != 4 {
		t.Fatalf("expected fresh run with 4 requests, got %d", *requests)
	}
	data, _ := os.ReadFile(filepath.Join(root, "math.go"))
	if !strings.Contains(string(data), "return 10") {
		t.Fatal("task not completed after recovery")
	}
}

func TestNoInfiniteLoopOnRepeatedReads(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, func(prompt string) string {
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 5)

	err := a.Run("Fix math.go so Add(2, 3) returns 10")
	if err == nil {
		t.Fatal("expected the run to stop on budget exhaustion")
	}
	if !strings.Contains(err.Error(), "without completion evidence") {
		t.Fatalf("unexpected error: %v", err)
	}
	if *requests != 5 {
		t.Fatalf("run must be bounded by Max iterations, got %d requests", *requests)
	}
	st, _, _ := loadState(root, "Fix math.go so Add(2, 3) returns 10")
	if st == nil || st.Done {
		t.Fatal("state must not be Done when the gate never passed")
	}
}

func TestCompletionGateRejectsWithoutVerification(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "wrote=-") && strings.Contains(prompt, "reads=[math.go]") {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 6)

	err := a.Run("Fix math.go so Add(2, 3) returns 10")
	if err == nil || !strings.Contains(err.Error(), "without completion evidence") {
		t.Fatalf("gate must reject unverified work, got: %v", err)
	}
	if *requests != 6 {
		t.Fatalf("expected 6 requests, got %d", *requests)
	}
	st, _, _ := loadState(root, "Fix math.go so Add(2, 3) returns 10")
	if st == nil || st.Done {
		t.Fatal("state must not be Done without diff+test evidence")
	}
}

func TestIdempotentRerunAfterDone(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, root, server.URL, 10)
	if err := a.Run("Fix math.go so Add(2, 3) returns 10"); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	first := *requests

	a2 := newAgent(t, root, server.URL, 10)
	if err := a2.Run("Fix math.go so Add(2, 3) returns 10"); err != nil {
		t.Fatalf("rerun of completed task failed: %v", err)
	}
	if *requests != first {
		t.Fatalf("completed rerun must not call the model: %d -> %d requests", first, *requests)
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	root := newFixture(t)
	task := "Fix math.go so Add(2, 3) returns 10"

	// Phase 1: a run that reads and writes but is interrupted before
	// verification (budget of 2).
	server1, _ := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "wrote=-") && strings.Contains(prompt, "reads=[math.go]") {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a1 := newAgent(t, root, server1.URL, 2)
	if err := a1.Run(task); err == nil {
		t.Fatal("phase 1 must exhaust its budget")
	}

	// Phase 2: resume with the verification strategy; the persisted read
	// snapshot must still authorize the CAS write path and the gate must
	// complete without re-reading.
	server2, requests := mockServer(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, "diff_after_write=no"):
			return "TOOL_CALL\nname: git_diff\nEND_TOOL_CALL"
		default:
			return "TOOL_CALL\nname: run_tests\nEND_TOOL_CALL"
		}
	})
	a2 := newAgent(t, root, server2.URL, 10)
	if err := a2.Run(task); err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}
	if *requests != 2 {
		t.Fatalf("resume should need only diff+test (2 requests), got %d", *requests)
	}
	st, resumed, _ := loadState(root, task)
	if !resumed || st == nil || !st.Done {
		t.Fatalf("final state must be restored and Done: %+v resumed=%v", st, resumed)
	}
}

func TestDataFileTaskCompletesViaDeclareDone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("old value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	server, requests := mockServer(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, "wrote=-"):
			if strings.Contains(prompt, "reads=[notes.txt]") {
				return "TOOL_CALL\nname: write_file\npath: notes.txt\ncontent: new value\nEND_TOOL_CALL"
			}
			return "TOOL_CALL\nname: read_file\npath: notes.txt\nEND_TOOL_CALL"
		case strings.Contains(prompt, "diff_after_write=no"):
			return "TOOL_CALL\nname: git_diff\nEND_TOOL_CALL"
		default:
			return "TOOL_CALL\nname: declare_done\nEND_TOOL_CALL"
		}
	})
	a := newAgent(t, root, server.URL, 10)
	if err := a.Run("Update notes.txt to say 'new value'"); err != nil {
		t.Fatalf("data-file task failed: %v", err)
	}
	if *requests != 4 {
		t.Fatalf("expected 4 requests (read, write, diff, declare_done), got %d", *requests)
	}
	data, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	// The parser strips the protocol separator before END_TOOL_CALL, so the
	// file holds exactly what the model sent (no trailing newline).
	if string(data) != "new value" {
		t.Fatalf("file not updated: %q", data)
	}
	if !a.State.Done {
		t.Fatal("state must be Done")
	}
	last := a.State.Journal[len(a.State.Journal)-1]
	if last.Tool != "declare_done" || !last.OK {
		t.Fatalf("final action must be declare_done: %+v", last)
	}
}
