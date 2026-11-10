package agent

import (
	"encoding/json"
	"kraken/memory"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKrakenReceivesCompletedRun(t *testing.T) {
	project := newFixture(t)
	storeRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(storeRoot, "events"), 0755); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, project, server.URL, 10)
	a.Memory = store
	if err := a.Run("Fix math.go so Add(2, 3) returns 10"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(storeRoot, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("want 6 events (start + 4 actions + completed), got %d", len(entries))
	}
	seen := map[string]int{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(storeRoot, "events", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var ev memory.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Project != project || ev.Task != "Fix math.go so Add(2, 3) returns 10" {
			t.Fatalf("unexpected event: %+v", ev)
		}
		seen[ev.Type]++
	}
	for _, kind := range []string{"task_started", "change_applied", "test_result", "task_completed"} {
		if seen[kind] != 1 {
			t.Fatalf("expected %s exactly once, got %d", kind, seen[kind])
		}
	}
}

func TestKrakenSavesPreWriteCheckpoint(t *testing.T) {
	project := newFixture(t)
	storeRoot := t.TempDir()
	for _, d := range []string{"events", "checkpoints", "lessons"} {
		if err := os.Mkdir(filepath.Join(storeRoot, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := memory.New(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, project, server.URL, 10)
	a.Memory = store
	if err := a.Run("Fix Add"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(storeRoot, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one checkpoint, got %d", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(storeRoot, "checkpoints", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var cp memory.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	if string(cp.Content) != brokenMath || !cp.Existed || cp.Path != "math.go" || cp.AfterSHA256 == "" {
		t.Fatalf("wrong saved snapshot: %+v", cp)
	}
	checkpointFile := filepath.Join(storeRoot, "checkpoints", entries[0].Name())
	if _, err := store.PreviewRollback(checkpointFile, project); err != nil {
		t.Fatalf("agent checkpoint preview: %v", err)
	}
	if err := store.ApplyRollback(checkpointFile, project); err != nil {
		t.Fatalf("agent checkpoint restore: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(project, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != brokenMath {
		t.Fatalf("restore did not recover original: %q", actual)
	}
}

func TestReviewedKrakenLessonInPrompt(t *testing.T) {
	project := newFixture(t)
	root := t.TempDir()
	for _, d := range []string{"events", "lessons"} {
		if err := os.Mkdir(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	l := memory.Lesson{Project: project, Title: "Test first", Note: "Read existing tests", Approved: true}
	b, _ := json.Marshal(l)
	if err := os.WriteFile(filepath.Join(root, "lessons", "note.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, project, "http://localhost:1", 10)
	a.Memory = store
	a.State = &TaskState{Evidence: Evidence{ReadFiles: map[string]string{}}}
	prompt := a.buildPrompt("task")
	if !strings.Contains(prompt, "Test first: Read existing tests") {
		t.Fatal("reviewed lesson absent")
	}
}
