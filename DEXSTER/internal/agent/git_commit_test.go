package agent

import (
	"dexster/internal/llm"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func fixture(t *testing.T) (string, *Agent) {
	t.Helper()
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	gitTest(t, d, "config", "user.email", "agent@example.test")
	gitTest(t, d, "config", "user.name", "Dexster Test")
	if err := os.WriteFile(filepath.Join(d, "main.go"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, d, "add", "main.go")
	gitTest(t, d, "commit", "-qm", "initial")
	a, err := New(d, "fake", llm.New("http://127.0.0.1:1"), 5)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{Task: "repair", Journal: []ActionRecord{{Iteration: 2, Tool: "write_file", Path: "main.go", OK: true}}}
	return d, a
}
func TestGitQualityCommitSuccessful(t *testing.T) {
	d, a := fixture(t)
	if err := os.WriteFile(filepath.Join(d, "main.go"), []byte("after\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "unrelated.txt"), []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := a.commitVerifiedChanges("repair")
	if err != nil {
		t.Fatal(err)
	}
	if hash != gitTest(t, d, "rev-parse", "HEAD") {
		t.Fatal("wrong commit hash")
	}
	if files := gitTest(t, d, "show", "--pretty=format:", "--name-only", "HEAD"); files != "main.go" {
		t.Fatalf("wrong committed paths: %q", files)
	}
	if _, err := os.Stat(filepath.Join(d, "unrelated.txt")); err != nil {
		t.Fatal(err)
	}
	if status := gitTest(t, d, "status", "--porcelain"); !strings.Contains(status, "?? unrelated.txt") {
		t.Fatal(status)
	}
}
func TestGitQualityCommitRejectsStagedUserWork(t *testing.T) {
	d, a := fixture(t)
	_ = os.WriteFile(filepath.Join(d, "main.go"), []byte("after\n"), 0644)
	_ = os.WriteFile(filepath.Join(d, "other.txt"), []byte("user work"), 0644)
	gitTest(t, d, "add", "other.txt")
	before := gitTest(t, d, "rev-parse", "HEAD")
	if _, err := a.commitVerifiedChanges("repair"); err == nil {
		t.Fatal("expected staged guard")
	}
	if before != gitTest(t, d, "rev-parse", "HEAD") {
		t.Fatal("HEAD changed")
	}
	if got := gitTest(t, d, "diff", "--cached", "--name-only"); got != "other.txt" {
		t.Fatal(got)
	}
}
func TestGitQualityCommitCrashRetryNoDuplicate(t *testing.T) {
	d, a := fixture(t)
	_ = os.WriteFile(filepath.Join(d, "main.go"), []byte("after\n"), 0644)
	hash, err := a.commitVerifiedChanges("repair")
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := a.commitVerifiedChanges("repair")
	if err != nil {
		t.Fatal(err)
	}
	if hash != hash2 {
		t.Fatal("duplicate commit")
	}
	if n := gitTest(t, d, "rev-list", "--count", "HEAD"); n != "2" {
		t.Fatal(n)
	}
}
func TestGitQualityCommitRejectsNonRepo(t *testing.T) {
	d := t.TempDir()
	a, err := New(d, "fake", llm.New("http://127.0.0.1:1"), 5)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{Journal: []ActionRecord{{Tool: "write_file", Path: "a.go", OK: true}}}
	if _, err := a.commitVerifiedChanges("repair"); err == nil {
		t.Fatal("expected nonrepository error")
	}
}

func TestGitAutoCommitFullAgentPass(t *testing.T) {
	root := newFixture(t)
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "agent@example.test")
	gitTest(t, root, "config", "user.name", "Dexster Test")
	gitTest(t, root, "add", "math.go", "math_test.go")
	gitTest(t, root, "commit", "-qm", "baseline")
	baseline := gitTest(t, root, "rev-parse", "HEAD")
	server, _ := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "reads=[math.go]") {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 8)
	a.AutoVerify = true
	a.AutoCommit = true
	if err := a.Run("git full flow"); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done || a.State.GitCommit == "" {
		t.Fatalf("missing commit receipt: %+v", a.State)
	}
	if baseline == gitTest(t, root, "rev-parse", "HEAD") {
		t.Fatal("no Git commit")
	}
	if got := gitTest(t, root, "show", "--pretty=format:", "--name-only", "HEAD"); got != "math.go" {
		t.Fatalf("wrong files: %q", got)
	}
	// Same task must not create a new commit.
	b := newAgent(t, root, server.URL, 8)
	b.AutoVerify = true
	b.AutoCommit = true
	if err := b.Run("git full flow"); err != nil {
		t.Fatal(err)
	}
	if a.State.GitCommit != gitTest(t, root, "rev-parse", "HEAD") {
		t.Fatal("unexpected second commit")
	}
}

func TestGitAutoCommitBlockedAfterFailedTests(t *testing.T) {
	root := newFixture(t)
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "agent@example.test")
	gitTest(t, root, "config", "user.name", "Dexster Test")
	gitTest(t, root, "add", "math.go", "math_test.go")
	gitTest(t, root, "commit", "-qm", "baseline")
	baseline := gitTest(t, root, "rev-parse", "HEAD")
	calls := 0
	server, _ := mockServer(t, func(prompt string) string {
		calls++
		if calls == 1 {
			return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
		}
		if calls == 2 {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: package main\nfunc Add(a,b int) int { return -1 }\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 6)
	a.AutoVerify = true
	a.AutoCommit = true
	if err := a.Run("failed tests no Git commit"); err == nil {
		t.Fatal("expected incomplete task")
	}
	if a.State.Done || a.State.GitCommit != "" {
		t.Fatal("incorrect done or commit receipt")
	}
	if baseline != gitTest(t, root, "rev-parse", "HEAD") {
		t.Fatal("committed failing code")
	}
}
