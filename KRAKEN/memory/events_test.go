package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordsAreAppendOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0755); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Record(Event{Type: "action_recorded", Task: "fix", Project: "/tmp/project", Iteration: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 events, got %d", len(entries))
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(root, "events", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var ev Event
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "action_recorded" {
			t.Fatalf("wrong event: %+v", ev)
		}
	}
}
func TestMissingStoreIsRejected(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing root error")
	}
}
