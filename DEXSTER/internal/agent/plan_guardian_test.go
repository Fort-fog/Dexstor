package agent

import (
	"strings"
	"testing"
)

func TestPlanGuardianFreezesWriteScopeAcrossResume(t *testing.T) {
	a := newAgent(t, newFixture(t), "http://localhost:1", 10)
	a.WriteScope = []string{"math.go"}
	if err := a.initializeWriteScope(nil); err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{WriteScope: append([]string(nil), a.WriteScope...), Task: "fix existing math", TaskHash: taskHash("fix existing math"), Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"math.go": "package main"}}}
	if !a.writeAllowed("math.go") {
		t.Fatal("authorized path rejected")
	}
	for _, p := range []string{"other.go", "math.go/../other.go", "../outside.go"} {
		if a.writeAllowed(p) {
			t.Fatalf("unauthorized path accepted: %s", p)
		}
	}
	if !strings.Contains(a.buildPrompt("fix existing math"), "FROZEN WRITE SCOPE: math.go") {
		t.Fatal("scope missing from plan")
	}
	b := newAgent(t, a.Root, "http://localhost:1", 10)
	b.WriteScope = []string{"other.go"}
	if err := b.initializeWriteScope(a.State); err == nil {
		t.Fatal("changed plan accepted")
	}
	c := newAgent(t, a.Root, "http://localhost:1", 10)
	c.WriteScope = []string{"math.go"}
	if err := c.initializeWriteScope(a.State); err != nil {
		t.Fatal(err)
	}
}
func TestPlanGuardianRejectsTraversalAndDuplicateScope(t *testing.T) {
	for _, scope := range [][]string{{"../outside.go"}, {"/tmp/outside.go"}, {"."}, {"math.go", "math.go"}} {
		a := newAgent(t, newFixture(t), "http://localhost:1", 10)
		a.WriteScope = scope
		if err := a.initializeWriteScope(nil); err == nil {
			t.Fatalf("bad scope accepted: %v", scope)
		}
	}
}
func TestPlanGuardianStagesAreEvidenceDriven(t *testing.T) {
	a := newAgent(t, newFixture(t), "http://localhost:1", 10)
	a.State = &TaskState{Evidence: Evidence{ReadFiles: map[string]string{}}}
	if got := a.currentPlanStage(); got != "inspect" {
		t.Fatal(got)
	}
	a.State.Evidence.Inspected = true
	if got := a.currentPlanStage(); got != "implement" {
		t.Fatal(got)
	}
	a.State.Evidence.WrotePath = "math.go"
	a.State.Evidence.LastWriteIter = 3
	if got := a.currentPlanStage(); got != "verify-diff" {
		t.Fatal(got)
	}
}
