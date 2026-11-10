package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyWriteForRecovery(t *testing.T) {
	project := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := "recover checked write"
	if _, err := store.BeginTask(project, task); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, "code.go")
	if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	cp, err := store.SaveCheckpoint(project, "code.go", true, []byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareWrite(project, task, 2, cp, []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := store.ReconcilePending(project, task); err != nil || status != "after" {
		t.Fatalf("reconcile: %q %v", status, err)
	}
	if err := store.VerifyWriteForRecovery(project, task, cp, "code.go"); err != nil {
		t.Fatalf("valid recovered write: %v", err)
	}
	if err := store.VerifyWriteForRecovery(project, task, cp, "other.go"); err == nil {
		t.Fatal("accepted wrong journal path")
	}
	if err := store.VerifyWriteForRecovery(project, task, filepath.Join(root, "checkpoints", "missing.json"), "code.go"); err == nil {
		t.Fatal("accepted absent checkpoint")
	}
	if err := os.WriteFile(target, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyWriteForRecovery(project, task, cp, "code.go"); err == nil || !strings.Contains(err.Error(), "after-hash") {
		t.Fatalf("accepted changed target: %v", err)
	}
}
