package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cleanupStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"events", "tasks", "checkpoints"} {
		if err := os.Mkdir(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func cleanupTask(t *testing.T, s *Store, name string, done bool, pending bool) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(project, "a.go", true, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ArmCheckpoint(cp, []byte("new")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err = json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.Created = time.Now().Add(-60 * 24 * time.Hour)
	raw, _ = json.Marshal(checkpoint)
	if err = os.WriteFile(cp, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m := TaskManifest{Version: 1, Project: project, Task: name, ID: TaskID(project, name), Done: done, Checkpoints: []string{cp}}
	if pending {
		m.Pending = &PendingWrite{Iteration: 2, Checkpoint: cp, AfterSHA256: "digest"}
	}
	if err = s.writeTask(m); err != nil {
		t.Fatal(err)
	}
	// Update independently of wall clock while preserving schema.
	taskPath := filepath.Join(s.Root, "tasks", m.ID+".json")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved TaskManifest
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Updated = time.Now().Add(-60 * 24 * time.Hour)
	data, _ = json.Marshal(saved)
	if err = os.WriteFile(taskPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return cp
}
func TestCleanupCompletedOnlyAndIdempotent(t *testing.T) {
	s := cleanupStore(t)
	old := cleanupTask(t, s, "finished", true, false)
	active := cleanupTask(t, s, "active", false, false)
	pending := cleanupTask(t, s, "pending", true, true)
	age := 30 * 24 * time.Hour
	now := time.Now()
	p, err := s.PreviewCleanup(age, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Checkpoint != old || p.Protected != 2 {
		t.Fatalf("plan %+v", p)
	}
	if _, err = os.Stat(old); err != nil {
		t.Fatalf("preview mutated: %v", err)
	}
	n, err := s.ApplyCleanup(age, now, p)
	if err != nil || n != 1 {
		t.Fatalf("applied %d %v", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old checkpoint not pruned: %v", err)
	}
	for _, cp := range []string{active, pending} {
		if _, err := os.Stat(cp); err != nil {
			t.Fatalf("protected checkpoint removed: %v", err)
		}
	}
	next, err := s.PreviewCleanup(age, now)
	if err != nil || len(next.Candidates) != 0 {
		t.Fatalf("re-preview %+v %v", next, err)
	}
}
func TestCleanupFailsClosedOnCorruptManifest(t *testing.T) {
	s := cleanupStore(t)
	cp := cleanupTask(t, s, "finished", true, false)
	if err := os.WriteFile(filepath.Join(s.Root, "tasks", "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewCleanup(24*time.Hour, time.Now()); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatal("checkpoint changed")
	}
}
func TestCleanupPlanChangesRejected(t *testing.T) {
	s := cleanupStore(t)
	cp := cleanupTask(t, s, "finished", true, false)
	p, err := s.PreviewCleanup(24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p.Candidates[0].Checkpoint += "-tampered"
	if _, err := s.ApplyCleanup(24*time.Hour, time.Now(), p); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("should reject changed plan %v", err)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupNoRecentDeletion(t *testing.T) {
	s := cleanupStore(t)
	_ = cleanupTask(t, s, "finished", true, false)
	p, err := s.PreviewCleanup(90*24*time.Hour, time.Now())
	if err != nil || len(p.Candidates) != 0 {
		t.Fatalf("unexpected recent cleanup %+v %v", p, err)
	}
}
