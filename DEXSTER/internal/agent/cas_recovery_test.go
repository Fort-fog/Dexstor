package agent

import (
	"strings"
	"testing"
)

func TestCASRecoveryReadsBeforeSecondWrite(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, func(prompt string) string {
		// The model incorrectly attempts to write on its first request.
		// After the controller's read, it can produce a grounded repair.
		if strings.Contains(prompt, "reads=[math.go]") {
			return `{"name":"write_file","path":"math.go","content":"package main\n\nfunc Add(a, b int) int { return 10 }\n"}`
		}
		return `{"name":"write_file","path":"math.go","content":"package main\n\nfunc Add(a, b int) int { return 10 }\n"}`
	})
	a := newAgent(t, root, server.URL, 9)
	a.AutoVerify = true
	if err := a.Run("Fix math.go Add(2,3) to 10"); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done {
		t.Fatal("expected DONE")
	}
	if *requests != 2 {
		t.Fatalf("expected 2 model requests, got %d", *requests)
	}
	want := []string{"write_file", "read_file", "write_file", "git_diff", "run_tests"}
	if len(a.State.Journal) != len(want) {
		t.Fatalf("journal: %#v", a.State.Journal)
	}
	for i, action := range want {
		if a.State.Journal[i].Tool != action {
			t.Fatalf("step %d: got %s, want %s", i, a.State.Journal[i].Tool, action)
		}
	}
	if a.State.Journal[0].OK || !a.State.Journal[1].OK {
		t.Fatal("first write must fail, recovery read must pass")
	}
}

func TestCASRecoveryOnlyForSnapshotFailures(t *testing.T) {
	a := &Agent{State: &TaskState{Journal: []ActionRecord{{Tool: "write_file", Path: "main.go", OK: false, Summary: "constraint violation: protected main.go:Multiply changed"}}}}
	if _, ok := a.pendingSnapshotRefresh(); ok {
		t.Fatal("must not auto-read on constraint failure")
	}
	a.State.Journal[0].Summary = "stale snapshot: expected base hash..."
	if path, ok := a.pendingSnapshotRefresh(); !ok || path != "main.go" {
		t.Fatal("expected stale snapshot recovery")
	}
}

func TestRepeatedFailureStopsAfterThreeIdenticalRejections(t *testing.T) {
	a := &Agent{State: &TaskState{Journal: []ActionRecord{
		{Tool: "write_file", Path: "main.go", OK: false, Summary: "constraint violation"},
		{Tool: "write_file", Path: "main.go", OK: false, Summary: "constraint violation"},
		{Tool: "write_file", Path: "main.go", OK: false, Summary: "constraint violation"},
	}}}
	if _, ok := a.repeatedFailure(); !ok {
		t.Fatal("expected circuit breaker")
	}
	a.State.Journal[1] = ActionRecord{Tool: "read_file", Path: "main.go", OK: true}
	if _, ok := a.repeatedFailure(); ok {
		t.Fatal("new information must reset circuit breaker")
	}
}
