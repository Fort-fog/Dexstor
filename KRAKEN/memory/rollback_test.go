package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func setupRollback(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, t.TempDir()
}
func TestRollbackExistingCASAndPreview(t *testing.T) {
	s, p := setupRollback(t)
	name := filepath.Join(p, "source.go")
	if err := os.WriteFile(name, []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(p, "source.go", true, []byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewRollback(cp, p); err == nil {
		t.Fatal("unarmed checkpoint was accepted")
	}
	if err = os.WriteFile(name, []byte("after"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("after")); err == nil {
		t.Fatal("rearmed checkpoint")
	}
	if _, err = s.PreviewRollback(cp, p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(name)
	if string(b) != "after" {
		t.Fatal("preview modified target")
	}
	if err = s.ApplyRollback(cp, p); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(name)
	if string(b) != "before" {
		t.Fatalf("got %q", b)
	}
	if err = s.ApplyRollback(cp, p); err == nil {
		t.Fatal("repeated rollback not refused")
	}
}
func TestRollbackRefusesConflictAndForeignProject(t *testing.T) {
	s, p := setupRollback(t)
	name := filepath.Join(p, "source.go")
	cp, err := s.SaveCheckpoint(p, "source.go", true, []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("other edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyRollback(cp, p); err == nil {
		t.Fatal("conflict accepted")
	}
	if _, err = s.PreviewRollback(cp, t.TempDir()); err == nil {
		t.Fatal("cross-project restore accepted")
	}
	b, _ := os.ReadFile(name)
	if string(b) != "other edit" {
		t.Fatal("conflict overwrote target")
	}
}
func TestRollbackCreatedFileAndSymlinkProtection(t *testing.T) {
	s, p := setupRollback(t)
	name := filepath.Join(p, "new.go")
	cp, err := s.SaveCheckpoint(p, "new.go", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyRollback(cp, p); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("created file was not deleted")
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err = os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, name); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewRollback(cp, p); err == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "secret" {
		t.Fatal("symlink target altered")
	}
}
func TestRollbackRejectsCheckpointTampering(t *testing.T) {
	s, p := setupRollback(t)
	cp, err := s.SaveCheckpoint(p, "x.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	var obj Checkpoint
	if err = json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	obj.Content = []byte("tampered")
	b, err = json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cp, b, 0600); err != nil {
		t.Fatal(err)
	}
	// A snapshot's preimage hash must match the content: corrupted snapshots refuse restore.
	if _, err = s.PreviewRollback(cp, p); err == nil {
		t.Fatal("tampered checkpoint accepted")
	}
}
