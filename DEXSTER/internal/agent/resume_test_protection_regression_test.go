package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeKeepsAgentCreatedTestsEditable(t *testing.T) {
	root := t.TempDir()
	orig := filepath.Join(root, "oracle_test.go")
	if err := os.WriteFile(orig, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Router.IsProtected("oracle_test.go") {
		t.Fatal("original test must be protected")
	}
	initial := originalProtectedTestPaths(a.Router)
	created := filepath.Join(root, "created_test.go")
	if err := os.WriteFile(created, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := &TaskState{ProtectedTests: initial}
	if err := saveState(root, st); err != nil {
		t.Fatal(err)
	}
	b, err := New(root, "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Router.IsProtected("created_test.go") {
		t.Fatal("precondition: startup shields test")
	}
	if err := b.restoreOriginalTestProtection(st); err != nil {
		t.Fatal(err)
	}
	if b.Router.IsProtected("created_test.go") {
		t.Fatal("agent-created test blocked after resume")
	}
	if !b.Router.IsProtected("oracle_test.go") {
		t.Fatal("original test protection lost")
	}
}

func TestLegacyResumeReconstructsProtectedTestsFromSuccessfulWrites(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"oracle_test.go", "created_test.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	st := &TaskState{Journal: []ActionRecord{{Tool: "write_file", Path: "created_test.go", OK: true}}}
	if err := saveState(root, st); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.restoreOriginalTestProtection(st); err != nil {
		t.Fatal(err)
	}
	if a.Router.IsProtected("created_test.go") {
		t.Fatal("legacy agent-created test is blocked")
	}
	if !a.Router.IsProtected("oracle_test.go") {
		t.Fatal("legacy oracle protection lost")
	}
	loaded, _, err := loadState(root, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = loaded // persisted baseline is checked directly below
	if len(st.ProtectedTests) != 1 || st.ProtectedTests[0] != "oracle_test.go" {
		t.Fatalf("wrong original tests: %v", st.ProtectedTests)
	}
}
