package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupIntent(t *testing.T) (*Store, string, TaskManifest) {
	t.Helper()
	s := cleanupStore(t)
	cp := cleanupTask(t, s, "finished", true, false)
	entries, err := os.ReadDir(filepath.Join(s.Root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	var m TaskManifest
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Root, "tasks", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	in := cleanupIntent{Version: 1, TaskID: m.ID, Project: m.Project, Checkpoints: []cleanupCheckpoint{digestForTest(cp, raw)}}
	if err = s.writeCleanupIntent(in); err != nil {
		t.Fatal(err)
	}
	return s, cp, m
}
func digestForTest(cp string, raw []byte) cleanupCheckpoint {
	h := sha256.Sum256(raw)
	return cleanupCheckpoint{Path: cp, SHA256: hex.EncodeToString(h[:])}
}
func TestCleanupRecoverCrashBeforeManifest(t *testing.T) {
	s, cp, m := setupIntent(t)
	if _, err := s.PreviewCleanup(24*time.Hour, time.Now()); err == nil {
		t.Fatal("must refuse pending cleanup")
	}
	n, err := s.RecoverCleanup()
	if err != nil || n != 1 {
		t.Fatalf("recover %d %v", n, err)
	}
	after, err := s.ReadTask(m.Project, m.Task)
	if err != nil || len(after.Checkpoints) != 0 {
		t.Fatalf("manifest %+v %v", after, err)
	}
	if _, err = os.Stat(cp); !os.IsNotExist(err) {
		t.Fatalf("checkpoint remains %v", err)
	}
	n, err = s.RecoverCleanup()
	if err != nil || n != 0 {
		t.Fatalf("idempotence %d %v", n, err)
	}
}
func TestCleanupRecoverCrashAfterManifest(t *testing.T) {
	s, cp, m := setupIntent(t)
	m.Checkpoints = nil
	if err := s.writeTask(m); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverCleanup()
	if err != nil || n != 1 {
		t.Fatalf("recover %d %v", n, err)
	}
	if _, err = os.Stat(cp); !os.IsNotExist(err) {
		t.Fatalf("checkpoint remains %v", err)
	}
}
func TestCleanupRecoverCrashAfterUnlink(t *testing.T) {
	s, cp, m := setupIntent(t)
	m.Checkpoints = nil
	if err := s.writeTask(m); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cp); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverCleanup()
	if err != nil || n != 0 {
		t.Fatalf("recover %d %v", n, err)
	}
}
func TestCleanupRecoveryRejectsMutation(t *testing.T) {
	s, cp, m := setupIntent(t)
	if err := os.WriteFile(cp, []byte("new checkpoint data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverCleanup(); err == nil {
		t.Fatal("mutated checkpoint accepted")
	}
	after, err := s.ReadTask(m.Project, m.Task)
	if err != nil || len(after.Checkpoints) != 1 {
		t.Fatal("manifest damaged")
	}
}
func TestCleanupRecoveryRejectsPendingWrite(t *testing.T) {
	s, cp, m := setupIntent(t)
	m.Pending = &PendingWrite{Checkpoint: cp, Iteration: 12, AfterSHA256: "xxx"}
	if err := s.writeTask(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverCleanup(); err == nil {
		t.Fatal("pending write accepted")
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupRecoveryRejectsCorruptIntent(t *testing.T) {
	s, cp, _ := setupIntent(t)
	if err := os.WriteFile(s.cleanupIntentPath(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverCleanup(); err == nil {
		t.Fatal("corrupt intent accepted")
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupNormalApplyClearsIntent(t *testing.T) {
	s := cleanupStore(t)
	cleanupTask(t, s, "finished", true, false)
	age := 30 * 24 * time.Hour
	p, err := s.PreviewCleanup(age, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCleanup(age, time.Now(), p); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.cleanupIntentPath()); !os.IsNotExist(err) {
		t.Fatal("intent not cleared")
	}
}
func TestCleanupRecoveryRejectsSharedCheckpoint(t *testing.T) {
	s, cp, _ := setupIntent(t)
	otherProject := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(otherProject, 0700); err != nil {
		t.Fatal(err)
	}
	m := TaskManifest{Version: 1, Project: otherProject, Task: "other", ID: TaskID(otherProject, "other"), Checkpoints: []string{cp}}
	if err := s.writeTask(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverCleanup(); err == nil {
		t.Fatal("shared checkpoint accepted")
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupRecoveryRejectsSymlink(t *testing.T) {
	s, cp, _ := setupIntent(t)
	if err := os.Remove(cp); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverCleanup(); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}
