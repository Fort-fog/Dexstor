package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrataAgentScopeDoesNotScanOperatorDirtyCode(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"legacy.go": "package main\nvar count = 1\n",
		"main.go":   "package main\nvar active = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=t@example.org", "commit", "-qm", "baseline"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if output, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "legacy.go"), []byte("package main\nvar cоunt = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nvar active = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := r.GitDiff(); res.OK {
		t.Fatal("whole-project guard must detect unsafe operator file")
	}
	if res := r.GitDiffForPaths([]string{"main.go"}); !res.OK {
		t.Fatalf("scoped agent diff blocked by unrelated file: %s", res.Error)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nvar cоunt = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := r.GitDiffForPaths([]string{"main.go"}); res.OK || !strings.Contains(res.Error, "main.go") {
		t.Fatalf("unsafe agent edit not blocked: %+v", res)
	}
}

func TestStrataGitWithoutHeadUsesRunBaseline(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nvar count=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "init", "-q")
	c.Dir = root
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nvar cоunt=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := r.GitDiffForPaths([]string{"main.go"}); res.OK || !strings.Contains(res.Error, "U+043E") {
		t.Fatalf("missing code guard without HEAD: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nvar count=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := r.GitDiffForPaths([]string{"main.go"}); !res.OK {
		t.Fatalf("git without HEAD must work: %+v", res)
	}
}
