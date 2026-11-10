package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeOldCASFuseDoesNotBlockRecovery(t *testing.T) {
	root := t.TempDir()
	a := newAgent(t, root, "http://127.0.0.1:1", 180)
	a.State = &TaskState{Iteration: 133, Journal: []ActionRecord{
		{Iteration: 119, Tool: "delete_path", Path: "task.go", OK: true},
		{Iteration: 126, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
		{Iteration: 129, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
		{Iteration: 133, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
	}}
	if _, bad := a.repeatedFailure(); !bad {
		t.Fatal("baseline fuse expected")
	}
	a.prepareDeletedFileCASFuseRecovery()
	if a.casFuseResetThrough != 133 {
		t.Fatalf("recovery boundary: %d", a.casFuseResetThrough)
	}
	if _, bad := a.repeatedFailure(); bad {
		t.Fatal("obsolete failures still block resume")
	}
	for _, i := range []int{134, 135, 136} {
		a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: i, Tool: "write_file", Path: "task.go", Summary: "different failure", OK: false})
	}
	if _, bad := a.repeatedFailure(); !bad {
		t.Fatal("new failures MUST still trip fuse")
	}
}

func TestCASFuseRecoveryDoesNotIgnorePresentOrUserOwnedFile(t *testing.T) {
	root := t.TempDir()
	a := newAgent(t, root, "http://127.0.0.1:1", 180)
	a.State = &TaskState{Iteration: 133, Journal: []ActionRecord{
		{Iteration: 119, Tool: "delete_path", Path: "task.go", OK: true},
		{Iteration: 126, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
		{Iteration: 129, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
		{Iteration: 133, Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
	}}
	if err := os.WriteFile(filepath.Join(root, "task.go"), []byte("new content"), 0600); err != nil {
		t.Fatal(err)
	}
	a.prepareDeletedFileCASFuseRecovery()
	if a.casFuseResetThrough != 0 {
		t.Fatal("must not ignore failures when file has reappeared")
	}
	if _, bad := a.repeatedFailure(); !bad {
		t.Fatal("fuse must remain active")
	}
}
