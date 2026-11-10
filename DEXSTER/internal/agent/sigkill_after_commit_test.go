package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"kraken/memory"
)

// TestSIGKILLAfterGitCommitBeforeReceipt kills the real agent process *after*
// git commit succeeds but *before* commitVerifiedChanges returns to persist the
// commit hash. It uses an isolated Git executable wrapper, not a production
// crash hook. No Ollama service is needed.
func TestSIGKILLAfterGitCommitBeforeReceipt(t *testing.T) {
	if os.Getenv("DEXSTER_SIGKILL_CHILD") == "1" {
		return
	}
	root := newFixture(t)
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "agent@example.test")
	gitTest(t, root, "config", "user.name", "Dexster Test")
	gitTest(t, root, "add", "math.go", "math_test.go")
	gitTest(t, root, "commit", "-qm", "baseline")
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	task := "v20 real SIGKILL after git commit before receipt"
	// Reuse the v19 child with an isolated task name controlled by environment.
	env := append(os.Environ(),
		"DEXSTER_SIGKILL_CHILD=1",
		"DEXSTER_E2E_ROOT="+root,
		"DEXSTER_E2E_KRAKEN="+krakenRoot,
		"DEXSTER_E2E_URL="+server.URL,
		"DEXSTER_E2E_TASK="+task,
	)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := `#!/bin/sh
"$DEXSTER_TEST_REAL_GIT" "$@"
status=$?
case " $* " in
  *" commit "*) if [ "$status" -eq 0 ]; then kill -KILL "$PPID"; fi ;;
esac
exit "$status"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	killedEnv := append(append([]string{}, env...), "DEXSTER_TEST_REAL_GIT="+realGit, "PATH="+bin+":"+os.Getenv("PATH"))
	spawn := func(environment []string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSIGKILLAgentChild$", "-test.v")
		cmd.Env = environment
		return cmd
	}
	first := spawn(killedEnv)
	output, err := first.CombinedOutput()
	if err == nil {
		t.Fatalf("child should be killed after commit:\n%s", output)
	}
	status, ok := first.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL after commit, got %v:\n%s", err, output)
	}
	if count := gitTest(t, root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("first child did not commit exactly once: %s\n%s", count, output)
	}
	committedHead := gitTest(t, root, "rev-parse", "HEAD")
	st, resumed, err := loadState(root, task)
	if err != nil || !resumed {
		t.Fatalf("no saved state: %v", err)
	}
	if st.GitCommit != "" || st.Done {
		t.Fatalf("receipt saved before kill: %+v", st)
	}
	// Real restart must recover HEAD via the task hash+path trailer, without
	// generating any additional commit, and finish KRAKEN acknowledgement.
	second := spawn(env)
	output, err = second.CombinedOutput()
	if err != nil {
		t.Fatalf("restart failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "DONE after") {
		t.Fatalf("no DONE after restart:\n%s", output)
	}
	st, resumed, err = loadState(root, task)
	if err != nil || !resumed || !st.Done || st.GitCommit != committedHead {
		t.Fatalf("invalid recovered commit receipt: %+v err=%v", st, err)
	}
	if count := gitTest(t, root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("duplicate commit after restart: %s", count)
	}
	third := spawn(env)
	output, err = third.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "already completed") {
		t.Fatalf("third start not idempotent: %v\n%s", err, output)
	}
	if count := gitTest(t, root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatal("duplicate commit after third start")
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil || !manifest.Done {
		t.Fatalf("KRAKEN not done: %v / %+v", err, manifest)
	}
	t.Logf("real SIGKILL after successful Git commit -> recovered receipt %s -> exactly one commit", committedHead[:12])
}
