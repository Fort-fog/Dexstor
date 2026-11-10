package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTaskProtocolPersistResumeAndConflict(t *testing.T) {
	s, p := setupRollback(t)
	m, err := s.BeginTask(p, "task-one")
	if err != nil {
		t.Fatal(err)
	}
	if m.BaselineStatus != "no_git" {
		t.Fatalf("unexpected baseline status: %s", m.BaselineStatus)
	}
	cp := taskCheckpoint(t, s, p, "a.go", "old", "new", false)
	if err = s.Acknowledge(p, "task-one", 3, cp, false); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.BeginTask(p, "task-one")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AcknowledgedIteration != 3 || len(resumed.Checkpoints) != 1 {
		t.Fatalf("resume: %+v", resumed)
	}
	if err = s.Acknowledge(p, "task-one", 3, cp, false); err != nil {
		t.Fatal(err)
	}
	reviewed, err := s.ReviewTask(p, "task-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Checkpoints) != 1 {
		t.Fatal("duplicate checkpoint")
	}
	if err = os.WriteFile(filepath.Join(p, "a.go"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviewTask(p, "task-one"); err == nil {
		t.Fatal("expected conflict")
	}
	if err = s.Acknowledge(p, "task-one", 2, "", false); err == nil {
		t.Fatal("regression allowed")
	}
}
func TestTaskProtocolRejectUnarmedCheckpoint(t *testing.T) {
	s, p := setupRollback(t)
	if _, err := s.BeginTask(p, "task-two"); err != nil {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(p, "a.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Acknowledge(p, "task-two", 1, cp, false); err == nil {
		t.Fatal("unarmed checkpoint accepted")
	}
}
func TestPendingWriteCrashReconcile(t *testing.T) {
	s, p := setupRollback(t)
	if _, err := s.BeginTask(p, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "a.go"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(p, "a.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareWrite(p, "interrupted", 2, cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	// Crash before the write: no file change, clear the intent.
	state, err := s.ReconcilePending(p, "interrupted")
	if err != nil || state != "before" {
		t.Fatalf("before: %s %v", state, err)
	}
	cp, err = s.SaveCheckpoint(p, "a.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareWrite(p, "interrupted", 2, cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "a.go"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	// Crash after changing the source but before arming checkpoint.
	state, err = s.ReconcilePending(p, "interrupted")
	if err != nil || state != "after" {
		t.Fatalf("after: %s %v", state, err)
	}
	if err = s.Acknowledge(p, "interrupted", 2, "", false); err != nil {
		t.Fatal(err)
	}
	m, err := s.ReviewTask(p, "interrupted")
	if err != nil || len(m.Checkpoints) != 1 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
}
func TestPendingWriteRejectsConflict(t *testing.T) {
	s, p := setupRollback(t)
	if _, err := s.BeginTask(p, "conflict"); err != nil {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(p, "a.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareWrite(p, "conflict", 1, cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "a.go"), []byte("user edits"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcilePending(p, "conflict"); err == nil {
		t.Fatal("conflict accepted")
	}
}
