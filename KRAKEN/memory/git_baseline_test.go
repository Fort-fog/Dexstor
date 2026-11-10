package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitCmd(t *testing.T, p string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", p}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}
func TestPinGitBaselineDoesNotTouchBranchOrIndex(t *testing.T) {
	p := t.TempDir()
	gitCmd(t, p, "init", "-q")
	gitCmd(t, p, "-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "initial")
	head := gitCmd(t, p, "rev-parse", "HEAD")
	branch := gitCmd(t, p, "symbolic-ref", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(p, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := PinGitBaseline(p, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Commit+"\n" != head || a.Ref == "" {
		t.Fatalf("baseline: %+v", a)
	}
	same, err := PinGitBaseline(p, "task-1")
	if err != nil || same != a {
		t.Fatalf("idempotent baseline: %+v %v", same, err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(p, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("index changed")
	}
	if gitCmd(t, p, "symbolic-ref", "HEAD") != branch || gitCmd(t, p, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD/branch changed")
	}
	if err := os.WriteFile(filepath.Join(p, "untracked.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := PinGitBaseline(p, "task-2"); err == nil {
		t.Fatal("dirty worktree accepted")
	}
}
func TestPinGitBaselineRejectsSubdirectory(t *testing.T) {
	p := t.TempDir()
	gitCmd(t, p, "init", "-q")
	gitCmd(t, p, "-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "initial")
	child := filepath.Join(p, "sub")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := PinGitBaseline(child, "t"); err == nil {
		t.Fatal("subdirectory accepted")
	}
}
