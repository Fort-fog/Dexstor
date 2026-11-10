package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"kraken/memory"
)

// TestSIGKILLAfterWriteBeforeKrakenAck checks the real Linux process boundary.
// The test-only pause occurs after state.json is saved and before the KRAKEN
// receipt. Neither a real Ollama server nor a production crash hook is needed.
func TestSIGKILLAfterWriteBeforeKrakenAck(t *testing.T) {
	if os.Getenv("DEXSTER_SIGKILL_CHILD") == "1" {
		return
	}
	root := newFixture(t)
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "agent@example.test")
	gitTest(t, root, "config", "user.name", "Dexster Test")
	gitTest(t, root, "add", "math.go", "math_test.go")
	gitTest(t, root, "commit", "-qm", "baseline")
	baseline := gitTest(t, root, "rev-parse", "HEAD")
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	task := "v22 real SIGKILL after write before KRAKEN acknowledgement"
	signalFile := filepath.Join(t.TempDir(), "written-before-ack")
	env := append(os.Environ(),
		"DEXSTER_SIGKILL_CHILD=1", "DEXSTER_E2E_ROOT="+root,
		"DEXSTER_E2E_KRAKEN="+krakenRoot,
		"DEXSTER_E2E_URL="+server.URL,
		"DEXSTER_E2E_TASK="+task)
	spawn := func(environment []string) *exec.Cmd {
		child := exec.Command(os.Args[0], "-test.run=^TestSIGKILLAgentChild$", "-test.v")
		child.Env = environment
		return child
	}
	first := spawn(append(append([]string{}, env...), "DEXSTER_E2E_BEFORE_WRITE_ACK_SIGNAL="+signalFile))
	logPath := filepath.Join(t.TempDir(), "first.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	first.Stdout, first.Stderr = logFile, logFile
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	// Ensure child cleanup even when the test fails early.
	killed := false
	defer func() {
		if !killed {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(signalFile); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(signalFile); err != nil {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("child never reached the write/ack window: %v\n%s", err, b)
	}
	beforeState, resumed, err := loadState(root, task)
	if err != nil || !resumed {
		t.Fatalf("state missing before kill: %v", err)
	}
	writeCount := 0
	for _, a := range beforeState.Journal {
		if a.Tool == "write_file" && a.OK {
			writeCount++
		}
	}
	if writeCount != 1 {
		t.Fatalf("expected exactly one persisted successful write, got %d", writeCount)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.AcknowledgedIteration >= beforeState.Iteration {
		t.Fatal("KRAKEN already acknowledged target iteration; not the intended crash window")
	}
	contentBefore, err := os.ReadFile(filepath.Join(root, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contentBefore), "return 10") {
		t.Fatalf("wrong pre-crash contents: %s", contentBefore)
	}
	if err := first.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err = first.Wait()
	killed = true
	status, ok := first.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("not killed by SIGKILL: %v", err)
	}
	if gitTest(t, root, "rev-parse", "HEAD") != baseline {
		t.Fatal("premature Git commit")
	}
	second := spawn(env)
	output, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("resume failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "resumed from persisted state") || !strings.Contains(string(output), "DONE after") {
		t.Fatalf("resume did not complete:\n%s", output)
	}
	contentAfter, err := os.ReadFile(filepath.Join(root, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contentAfter) != string(contentBefore) {
		t.Fatal("source bytes changed on resume")
	}
	st, resumed, err := loadState(root, task)
	if err != nil || !resumed || !st.Done || st.GitCommit == "" {
		t.Fatalf("bad resumed state: %+v / %v", st, err)
	}
	writesAfter := 0
	for _, a := range st.Journal {
		if a.Tool == "write_file" && a.OK {
			writesAfter++
		}
	}
	if writesAfter != 1 {
		t.Fatalf("write replayed: successful write count %d", writesAfter)
	}
	if count := gitTest(t, root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("expected one commit, got %s", count)
	}
	third := spawn(env)
	output, err = third.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "already completed") {
		t.Fatalf("third start failed: %v\n%s", err, output)
	}
	if count := gitTest(t, root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatal("duplicate commit on third start")
	}
	manifest, err = store.ReadTask(root, task)
	if err != nil || !manifest.Done {
		t.Fatalf("KRAKEN not completed: %v / %+v", err, manifest)
	}
	t.Logf("real SIGKILL before KRAKEN ack -> SHA-256 verified recovery -> one write and one commit %s", st.GitCommit[:12])
}
