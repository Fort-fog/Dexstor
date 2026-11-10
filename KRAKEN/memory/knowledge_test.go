package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointKeepsOriginalBytes(t *testing.T) {
	root := t.TempDir()
	store := &Store{Root: root}
	name, err := store.SaveCheckpoint("/project", "src/main.go", true, []byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var cp Checkpoint
	if err = json.Unmarshal(b, &cp); err != nil {
		t.Fatal(err)
	}
	if string(cp.Content) != "before" || cp.Path != "src/main.go" || !cp.Existed {
		t.Fatalf("wrong checkpoint: %+v", cp)
	}
	if info, err := os.Stat(name); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected permissions: %v %v", info, err)
	}
	if _, err := store.SaveCheckpoint("/project", "../escape", true, nil); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := store.SaveCheckpoint("/project", "new.go", false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLessonsProjectScopedAndReviewed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "lessons")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]Lesson{
		"valid.json":      {Project: "/project", Title: "Read tests", Note: "Inspect test expectations before changes", Approved: true},
		"other.json":      {Project: "/other", Title: "Other", Note: "wrong repo", Approved: true},
		"unreviewed.json": {Project: "/project", Title: "No", Note: "unapproved", Approved: false},
	} {
		b, _ := json.Marshal(l)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "valid.json"), filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	found, err := (&Store{Root: root}).Lessons("/project", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Title != "Read tests" {
		t.Fatalf("unexpected lessons: %+v", found)
	}
}
