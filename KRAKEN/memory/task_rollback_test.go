package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func taskCheckpoint(t *testing.T, s *Store, p, name, before, after string, created bool) string {
	t.Helper()
	cp, err := s.SaveCheckpoint(p, name, !created, []byte(before))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p, name)
	if err := os.WriteFile(path, []byte(after), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.ArmCheckpoint(cp, []byte(after)); err != nil {
		t.Fatal(err)
	}
	return cp
}
func TestTaskRollbackTwoFilesAndCreated(t *testing.T) {
	s, p := setupRollback(t)
	a := taskCheckpoint(t, s, p, "a.go", "old-a", "new-a", false)
	b := taskCheckpoint(t, s, p, "b.go", "", "new-b", true)
	plan, err := s.PreviewTaskRollback([]string{b, a}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Files[0].Path != "a.go" || !plan.Files[1].Created {
		t.Fatalf("plan: %+v", plan)
	}
	if err = s.ApplyTaskRollback([]string{a, b}, p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p, "a.go"))
	if err != nil || string(data) != "old-a" {
		t.Fatalf("a=%q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(p, "b.go")); !os.IsNotExist(err) {
		t.Fatal("new file not removed")
	}
}
func TestTaskRollbackPreflightConflictLeavesAll(t *testing.T) {
	s, p := setupRollback(t)
	a := taskCheckpoint(t, s, p, "a.go", "old-a", "new-a", false)
	b := taskCheckpoint(t, s, p, "b.go", "old-b", "new-b", false)
	if err := os.WriteFile(filepath.Join(p, "b.go"), []byte("user edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTaskRollback([]string{a, b}, p); err == nil {
		t.Fatal("conflict was accepted")
	}
	data, _ := os.ReadFile(filepath.Join(p, "a.go"))
	if string(data) != "new-a" {
		t.Fatal("first file was incorrectly restored")
	}
}
func TestTaskRollbackRejectsDuplicateAndSymlink(t *testing.T) {
	s, p := setupRollback(t)
	a := taskCheckpoint(t, s, p, "a.go", "old", "new", false)
	if _, err := s.PreviewTaskRollback([]string{a, a}, p); err == nil {
		t.Fatal("duplicate accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p, "a.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p, "a.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewTaskRollback([]string{a}, p); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink not refused: %v", err)
	}
}
