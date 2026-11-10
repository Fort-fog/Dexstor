package agent

import (
	"dexster/internal/llm"
	"dexster/internal/tools"
	"strings"
	"testing"
)

func TestSmartRepairDiagnosticSurvivesCASRefresh(t *testing.T) {
	a := &Agent{State: &TaskState{TaskHash: taskHash("fix build"), Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"main.go": "package main\n"}}, Journal: []ActionRecord{
		{Iteration: 1, Tool: "write_file", Path: "main.go", OK: true},
		{Iteration: 2, Tool: "run_tests", OK: false, Summary: "# example\n./main.go:15: undefined: ctx"},
		{Iteration: 3, Tool: "write_file", Path: "main.go", OK: false, Summary: "stale snapshot"},
		{Iteration: 4, Tool: "read_file", Path: "main.go", OK: true},
	}}}
	root := newFixture(t)
	router, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	a.Root = root
	a.Router = router
	diagnostic := a.recentRepairDiagnostic()
	if !strings.Contains(diagnostic, "undefined: ctx") {
		t.Fatalf("lost compiler error: %q", diagnostic)
	}
	p := a.buildPrompt("fix build")
	if !strings.Contains(p, "SMART REPAIR") || !strings.Contains(p, "undefined: ctx") {
		t.Fatalf("missing diagnostic in prompt: %s", p)
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 5, Tool: "run_tests", OK: true})
	if got := a.recentRepairDiagnostic(); got != "" {
		t.Fatalf("stale failure after PASS: %q", got)
	}
}

func TestSmartRepairDiagnosticBoundsAndNoInventedResults(t *testing.T) {
	a := &Agent{State: &TaskState{Journal: []ActionRecord{{Iteration: 1, Tool: "read_file", OK: true}}}}
	if s := a.recentRepairDiagnostic(); s != "" {
		t.Fatalf("invented error: %s", s)
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 2, Tool: "go_test_extra", OK: false, Summary: strings.Repeat("x", 5000)})
	if len(a.recentRepairDiagnostic()) > 1100 {
		t.Fatal("diagnostic exceeds prompt bound")
	}
}

func TestSmartRepairRejectsIdenticalFailedRevision(t *testing.T) {
	root := newFixture(t)
	a, err := New(root, "mock", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	// The model knows the exact original content, which failed verification.
	content := brokenMath
	a.State = &TaskState{Evidence: Evidence{ReadFiles: map[string]string{"math.go": content}}, Journal: []ActionRecord{{Iteration: 3, Tool: "run_tests", OK: false, Summary: "assertion failed"}}}
	// This gate is initialized by Run in production.
	if err := a.initializeConstraints(nil); err != nil {
		t.Fatal(err)
	}
	result := a.execute(llm.Call{Name: "write_file", Args: map[string]string{"path": "math.go", "content": content}})
	if result.OK || !strings.Contains(result.Error, "SMART REPAIR") {
		t.Fatalf("no-op repair allowed: %+v", result)
	}
	// A genuinely changed candidate is not blocked by Smart Repair.
	result = a.execute(llm.Call{Name: "write_file", Args: map[string]string{"path": "math.go", "content": fixedMath}})
	if !result.OK {
		t.Fatalf("changed repair blocked: %+v", result)
	}
}
