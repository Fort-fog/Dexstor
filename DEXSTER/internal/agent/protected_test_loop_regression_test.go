package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This reproduces the live build_new loop: several different tools target
// the same immutable test oracle, with benign reads between rejected edits.
func TestProtectedTestLoopBuildNewStopsAcrossTools(t *testing.T) {
	root := t.TempDir()
	oracle := []byte("package main\nfunc TestBroken(\n")
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), oracle, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 35)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{TaskMode: taskModeBuild, Journal: []ActionRecord{
		{Iteration: 1, Tool: "write_file", Path: "main_test.go", Summary: "protected", OK: false},
		{Iteration: 2, Tool: "read_file", Path: "main_test.go", OK: true},
		{Iteration: 3, Tool: "replace_in_file", Path: "main_test.go", Summary: "protected", OK: false},
		{Iteration: 4, Tool: "declare_done", Summary: "verification pending", OK: false},
		{Iteration: 5, Tool: "delete_path", Path: "main_test.go", Summary: "protected", OK: false},
	}}
	msg, stop := a.repeatedFailure()
	if !stop || !strings.Contains(msg, "protected test main_test.go") || !strings.Contains(msg, "3 rejected") {
		t.Fatalf("must stop protected test loop, got stop=%v msg=%q", stop, msg)
	}
	if a.State.Done {
		t.Fatal("rejected test edits must not set DONE")
	}
	got, err := os.ReadFile(filepath.Join(root, "main_test.go"))
	if err != nil || string(got) != string(oracle) {
		t.Fatalf("protected oracle changed: %q %v", got, err)
	}
}

func TestProtectedTestLoopAllowsRealSourceRepair(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 35)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{TaskMode: taskModeBuild, Journal: []ActionRecord{
		{Iteration: 1, Tool: "write_file", Path: "main_test.go", OK: false},
		{Iteration: 2, Tool: "replace_in_file", Path: "main_test.go", OK: false},
		{Iteration: 3, Tool: "write_file", Path: "main.go", OK: true},
		{Iteration: 4, Tool: "write_file", Path: "main_test.go", OK: false},
	}}
	if msg, stop := a.repeatedFailure(); stop {
		t.Fatalf("valid source repair must reset the loop counter: %s", msg)
	}
	// Merely re-reading or failing to declare DONE never resets the counter.
	a.State.Journal = append(a.State.Journal,
		ActionRecord{Iteration: 5, Tool: "read_file", Path: "main_test.go", OK: false},
		ActionRecord{Iteration: 6, Tool: "replace_in_file", Path: "main_test.go", OK: false},
		ActionRecord{Iteration: 7, Tool: "delete_path", Path: "main_test.go", OK: false},
	)
	if msg, stop := a.repeatedFailure(); !stop || !strings.Contains(msg, "protected test") {
		t.Fatalf("repeated protected test edits not stopped: stop=%v msg=%q", stop, msg)
	}
}

func TestProtectedTestLoopDoesNotBlockOrdinaryRepair(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 35)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{TaskMode: taskModeBuild, Journal: []ActionRecord{
		{Iteration: 1, Tool: "write_file", Path: "main.go", OK: false},
		{Iteration: 2, Tool: "read_file", Path: "main.go", OK: true},
		{Iteration: 3, Tool: "replace_in_file", Path: "main.go", OK: false},
		{Iteration: 4, Tool: "delete_path", Path: "junk.tmp", OK: false},
	}}
	if msg, stop := a.repeatedFailure(); stop {
		t.Fatalf("non-protected targets should retain existing controller policy: %s", msg)
	}
}

func TestProtectedTestLoopAfterResumeStops(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 35)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{TaskMode: taskModeBuild, Journal: []ActionRecord{
		{Iteration: 9, Tool: "write_file", Path: "main_test.go", OK: false},
		{Iteration: 10, Tool: "read_file", Path: "main_test.go", OK: false},
		{Iteration: 11, Tool: "replace_in_file", Path: "main_test.go", OK: false},
	}}
	if _, stop := a.repeatedFailure(); stop {
		t.Fatal("two rejected attempts must still allow one corrective decision")
	}
	// As in a resumed state.json, the historical journal stays intact.
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 12, Tool: "write_file", Path: "main_test.go", OK: false})
	if _, stop := a.repeatedFailure(); !stop {
		t.Fatal("resumed protected test loop should stop after the third refusal")
	}
}

// Integration: in build_new, a real failing go test can tempt the model to
// rewrite the protected oracle. Stop deterministically without changing it.
func TestProtectedTestLoopEndToEndBuildNewStopsBeforeBudget(t *testing.T) {
	root := newFixture(t)
	original := []byte("package main\nfunc TestInvalid(\n")
	if err := os.WriteFile(filepath.Join(root, "math_test.go"), original, 0600); err != nil {
		t.Fatal(err)
	}
	gitBaseline(t, root)
	calls := 0
	server, _ := mockServer(t, func(prompt string) string {
		calls++
		switch calls {
		case 1:
			return `{"name":"read_file","path":"math.go"}`
		case 2:
			return `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int {return a+b+1}\n"}`
		case 3:
			return `{"name":"write_file","path":"math_test.go","content":"package main\n"}`
		case 4:
			return `{"name":"read_file","path":"math_test.go"}`
		case 5:
			return `{"name":"replace_in_file","path":"math_test.go","old":"func TestInvalid(","new":"func TestValid() {}"}`
		default:
			return `{"name":"delete_path","path":"math_test.go"}`
		}
	})
	a := newAgent(t, root, server.URL, 35)
	a.AutoVerify = true
	err := a.Run("Create and test a new Go program")
	if err == nil || !strings.Contains(err.Error(), "protected test math_test.go") {
		t.Fatalf("expected explicit protected test stop, got %v", err)
	}
	if calls > 6 || a.State.Iteration >= 35 || a.State.Done {
		t.Fatalf("unexpected result: calls=%d iterations=%d done=%v", calls, a.State.Iteration, a.State.Done)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "math_test.go"))
	if readErr != nil || string(data) != string(original) {
		t.Fatalf("original test oracle changed: %q %v", data, readErr)
	}
}
