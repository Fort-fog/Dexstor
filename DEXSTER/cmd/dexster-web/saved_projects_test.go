package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSavedProjectsReadOnlyList(t *testing.T) {
	root := t.TempDir()
	finished := filepath.Join(root, "finished")
	broken := filepath.Join(root, "broken")
	empty := filepath.Join(root, "empty")
	for _, path := range []string{finished, broken} {
		if err := os.MkdirAll(filepath.Join(path, ".dexster"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(p string, done bool, commit string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"done": done, "git_commit": commit})
		if err := os.WriteFile(filepath.Join(p, ".dexster", "state.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(finished, true, "abcdef")
	write(broken, false, "")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(finished, ".dexster", "state.json"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(finished, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := listSavedProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 3 {
		t.Fatalf("projects: %+v", got.Projects)
	}
	if got.Projects[0].Name != "broken" || got.Projects[0].Status != "incomplete" {
		t.Fatalf("newest unfinished not first: %+v", got.Projects)
	}
	statuses := map[string]string{}
	for _, p := range got.Projects {
		statuses[p.Name] = p.Status
	}
	if statuses["finished"] != "completed" || statuses["empty"] != "new" {
		t.Fatalf("statuses: %+v", statuses)
	}
	data, err := os.ReadFile(filepath.Join(finished, ".dexster", "state.json"))
	if err != nil || !json.Valid(data) {
		t.Fatal("state altered")
	}
}
