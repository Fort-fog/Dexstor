package agent

import (
	"dexster/internal/llm"
	"dexster/internal/tools"
	"testing"
)

func callForTest(name, path string) llm.Call {
	return llm.Call{Name: name, Args: map[string]string{"path": path}}
}
func okResultForTest(tool, path string) tools.Result {
	return tools.Result{Tool: tool, Path: path, OK: true}
}

func TestFailedFileMutationBlocksDoneUntilRepaired(t *testing.T) {
	r, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Router: r, State: &TaskState{
		Evidence: Evidence{Inspected: true, WrotePath: "index.html", LastWriteIter: 2, LastDiffIter: 3, DeclaredIter: 6},
		Journal: []ActionRecord{
			{Iteration: 2, Tool: "write_file", Path: "index.html", OK: true},
			{Iteration: 3, Tool: "git_diff", OK: true},
			{Iteration: 5, Tool: "copy_file", Path: "assets/neon.jpg", Source: "/home/user/Pictures/neon.jpg", OK: false},
			{Iteration: 6, Tool: "declare_done", OK: true},
		},
	}}
	if a.gateComplete() {
		t.Fatal("DONE accepted with unresolved failed copy_file")
	}

	// A successful retry becomes the newest mutation; it still needs a fresh diff.
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 7, Tool: "copy_file", Path: "assets/neon.jpg", Source: "/home/user/Pictures/neon.jpg", OK: true})
	a.State.Evidence.WrotePath = "assets/neon.jpg"
	a.State.Evidence.LastWriteIter = 7
	a.State.Evidence.DeclaredIter = 9
	if a.gateComplete() {
		t.Fatal("DONE accepted before diff after repaired copy")
	}
	a.State.Evidence.LastDiffIter = 8
	if a.gateComplete() {
		t.Fatal("DONE accepted without HTML validation")
	}
	a.State.Evidence.LastHTMLVerifyIter = 9
	if a.gateComplete() {
		t.Fatal("DONE accepted without task review")
	}
	a.State.Evidence.LastReviewIter = 10
	if !a.gateComplete() {
		t.Fatal("DONE rejected after successful copy, fresh diff, HTML validation, task review, and declaration")
	}
}

func TestMakeDirDoesNotInvalidateFileEvidence(t *testing.T) {
	r, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Router: r, State: &TaskState{Evidence: Evidence{Inspected: true, WrotePath: "index.html", LastWriteIter: 2, LastDiffIter: 3, DeclaredIter: 4}}}
	a.applyEvidence(callForTest("make_dir", "assets"), okResultForTest("make_dir", "assets"), 5)
	if a.State.Evidence.LastWriteIter != 2 {
		t.Fatalf("make_dir changed LastWriteIter to %d", a.State.Evidence.LastWriteIter)
	}
}
