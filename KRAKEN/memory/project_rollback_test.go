package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRollbackIntent(t *testing.T) {
	cases := []struct {
		task string
		ok   bool
		mode ProjectRollbackMode
	}{
		{"откатись до рабочей версии", true, RollbackPrevious},
		{"этот вариант сломан, верни предыдущую рабочую версию", true, RollbackPrevious},
		{"верни первую рабочую версию", true, RollbackFirst},
		{"rollback to first working version", true, RollbackFirst},
		{"верни кнопку назад", false, ""},
		{"не откатывай проект", false, ""},
	}
	for _, tc := range cases {
		r, ok := ParseRollbackIntent(tc.task)
		if ok != tc.ok || (ok && r.Mode != tc.mode) {
			t.Fatalf("%q -> ok=%v mode=%s", tc.task, ok, r.Mode)
		}
	}
}

func beginLegacyTask(t *testing.T, s *Store, p, task string) {
	t.Helper()
	if _, err := s.BeginTask(p, task); err != nil {
		t.Fatal(err)
	}
}

func legacyWrite(t *testing.T, s *Store, p, task, name, after string, iter int) {
	t.Helper()
	path := filepath.Join(p, name)
	before, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	cp, err := s.SaveCheckpoint(p, name, existed, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(after), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.ArmCheckpoint(cp, []byte(after)); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(p, task, iter, cp, false); err != nil {
		t.Fatal(err)
	}
}

func finishLegacyTask(t *testing.T, s *Store, p, task string, iter int) {
	t.Helper()
	if err := s.Acknowledge(p, task, iter, "", true); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyProjectRollbackPreviousHandlesRepeatedWrites(t *testing.T) {
	s, p := setupRollback(t)
	if err := os.WriteFile(filepath.Join(p, "index.html"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	beginLegacyTask(t, s, p, "first site")
	legacyWrite(t, s, p, "first site", "index.html", "working-v1", 1)
	finishLegacyTask(t, s, p, "first site", 1)
	beginLegacyTask(t, s, p, "bad edit")
	legacyWrite(t, s, p, "bad edit", "index.html", "broken-a", 1)
	legacyWrite(t, s, p, "bad edit", "index.html", "broken-b", 2)
	finishLegacyTask(t, s, p, "bad edit", 2)

	plan, err := s.PreviewProjectRollback(p, RollbackPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "legacy_checkpoint_chain" || plan.TargetTask != "bad edit" {
		t.Fatalf("plan=%+v", plan)
	}
	res, err := s.ApplyProjectRollback(plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredFiles != 2 {
		t.Fatalf("restored=%d", res.RestoredFiles)
	}
	b, _ := os.ReadFile(filepath.Join(p, "index.html"))
	if string(b) != "working-v1" {
		t.Fatalf("got %q", b)
	}
}

func TestLegacyProjectRollbackFirstWorking(t *testing.T) {
	s, p := setupRollback(t)
	if err := os.WriteFile(filepath.Join(p, "index.html"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	for i, pair := range []struct{ task, value string }{{"site-v1", "v1"}, {"site-v2", "v2"}, {"site-v3", "v3"}} {
		beginLegacyTask(t, s, p, pair.task)
		legacyWrite(t, s, p, pair.task, "index.html", pair.value, 1)
		finishLegacyTask(t, s, p, pair.task, 1)
		_ = i
	}
	plan, err := s.PreviewProjectRollback(p, RollbackFirst)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetTask != "site-v1" || len(plan.LegacyTasks) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := s.ApplyProjectRollback(plan); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p, "index.html"))
	if string(b) != "v1" {
		t.Fatalf("got %q", b)
	}
}

func TestVerifiedSnapshotsRestorePreviousWithoutLLM(t *testing.T) {
	s, p := setupRollback(t)
	if err := os.WriteFile(filepath.Join(p, "index.html"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	beginLegacyTask(t, s, p, "verified-v1")
	v1, err := s.PrepareVerifiedSnapshot(p, "verified-v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteVerifiedTask(p, "verified-v1", 1, v1); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(p, "index.html"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	beginLegacyTask(t, s, p, "verified-v2")
	v2, err := s.PrepareVerifiedSnapshot(p, "verified-v2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteVerifiedTask(p, "verified-v2", 1, v2); err != nil {
		t.Fatal(err)
	}

	plan, err := s.PreviewProjectRollback(p, RollbackPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "verified_snapshot" || plan.TargetTask != "verified-v1" {
		t.Fatalf("plan=%+v", plan)
	}
	res, err := s.ApplyProjectRollback(plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.SafetySnapshot == "" {
		t.Fatal("missing safety snapshot")
	}
	b, _ := os.ReadFile(filepath.Join(p, "index.html"))
	if string(b) != "v1" {
		t.Fatalf("got %q", b)
	}
}
