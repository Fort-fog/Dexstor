package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureManagedGitInitializesAndPinsCurrentProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("working-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureManagedGit(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf(".git missing: %v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("baseline not clean: %q", out)
	}
	cmd = exec.Command("git", "show", "HEAD:index.html")
	cmd.Dir = root
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "working-v1" {
		t.Fatalf("baseline content=%q", out)
	}
}

// A new task must not be blocked when the previous task's runtime state
// is renamed to stale. Both state variants remain on disk and outside Git.
func TestEnsureManagedGitExcludesPersistedStateAcrossTasks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".dexster"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".dexster", "state.json"), []byte("run 1"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a previously used version that tracked state.json in Git.
	if _, err := runGit(root, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(root, "config", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(root, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(root, "commit", "-m", "old baseline"); err != nil {
		t.Fatal(err)
	}
	if err := ensureManagedGit(root); err != nil {
		t.Fatal(err)
	}
	tracked, err := runGit(root, "ls-files", "--", ".dexster")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(tracked)) != "" {
		t.Fatalf("still tracking runtime state: %s", tracked)
	}
	if err := os.Rename(filepath.Join(root, ".dexster", "state.json"), filepath.Join(root, ".dexster", "state.json.stale-1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".dexster", "state.json"), []byte("run 2"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := runGit(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(status)) != "" {
		t.Fatalf("runtime rotation dirtied project Git: %s", status)
	}
	if err := ensureManagedGit(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "state.json.stale-1"} {
		if _, err := os.Stat(filepath.Join(root, ".dexster", name)); err != nil {
			t.Fatal(err)
		}
	}
}
