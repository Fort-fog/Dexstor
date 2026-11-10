package agent

import (
	"dexster/internal/tools"
	"testing"
)

func TestHTMLValidationGateIsMandatoryAfterWrite(t *testing.T) {
	r, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Router: r, AutoVerify: true, State: &TaskState{Evidence: Evidence{Inspected: true, WrotePath: "index.html", LastWriteIter: 2, LastDiffIter: 3, DeclaredIter: 4}, Journal: []ActionRecord{{Iteration: 2, Tool: "write_file", Path: "index.html", OK: true}, {Iteration: 3, Tool: "git_diff", OK: true}, {Iteration: 4, Tool: "declare_done", OK: true}}}}
	if a.gateComplete() {
		t.Fatal("must not accept HTML DONE before validator PASS")
	}
	if tool, yes := a.nextVerification(); !yes || tool != "validate_html" {
		t.Fatalf("got %q %v", tool, yes)
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 5, Tool: "validate_html", OK: false})
	if tool, yes := a.nextVerification(); yes {
		t.Fatalf("validation failure must wait for repair, got %q", tool)
	}
	if a.gateComplete() {
		t.Fatal("failed validation must block DONE")
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 6, Tool: "write_file", Path: "index.html", OK: true})
	a.State.Evidence.LastWriteIter = 6
	a.State.Evidence.LastDiffIter = 7
	if tool, yes := a.nextVerification(); !yes || tool != "validate_html" {
		t.Fatalf("retry after repair: %q %v", tool, yes)
	}
	a.applyEvidence(callForTest("validate_html", "index.html"), okResultForTest("validate_html", "index.html"), 8)
	a.State.Evidence.DeclaredIter = 9
	if tool, yes := a.nextVerification(); !yes || tool != "review_task" {
		t.Fatalf("expected review_task after HTML check, got %q %v", tool, yes)
	}
	a.applyEvidence(callForTest("review_task", ""), okResultForTest("review_task", ""), 10)
	if !a.gateComplete() {
		t.Fatal("PASS validator should permit completion")
	}
}
