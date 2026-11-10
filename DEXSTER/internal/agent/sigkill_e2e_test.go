package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dexster/internal/llm"
	"kraken/memory"
)

// TestSIGKILLAgentChild runs a complete DEXSTER agent inside a distinct OS
// process. It is invoked only by TestSIGKILLAgentResumeEndToEnd.
func TestSIGKILLAgentChild(t *testing.T) {
	if os.Getenv("DEXSTER_SIGKILL_CHILD") != "1" {
		return
	}
	root, krakenRoot, url := os.Getenv("DEXSTER_E2E_ROOT"), os.Getenv("DEXSTER_E2E_KRAKEN"), os.Getenv("DEXSTER_E2E_URL")
	if root == "" || krakenRoot == "" || url == "" {
		t.Fatal("missing child configuration")
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "mock-model", llm.New(url), 20)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory, a.AutoVerify, a.AutoCommit = store, true, true
	if signalFile := os.Getenv("DEXSTER_E2E_BEFORE_WRITE_ACK_SIGNAL"); signalFile != "" {
		a.testBeforeWriteAck = func() {
			// The only signal goes out after the persisted agent journal is durable,
			// while the KRAKEN acknowledgement is still missing.
			if err := os.WriteFile(signalFile, []byte("ready"), 0600); err != nil {
				panic(err)
			}
			time.Sleep(30 * time.Second) // parent SIGKILLs this child
		}
	}
	if err := a.Run(func() string {
		if task := os.Getenv("DEXSTER_E2E_TASK"); task != "" {
			return task
		}
		return "v19 verify real SIGKILL resume"
	}()); err != nil {
		t.Fatal(err)
	}
}

// TestSIGKILLAgentResumeEndToEnd proves that a killed agent can resume its
// persisted, already-written change, rerun checks, and create one Git commit.
// A slow test target gives the parent a reliable window to kill *after* the
// model's write AND the KRAKEN acknowledgement have reached disk;
// there is no production crash hook.
func TestSIGKILLAgentResumeEndToEnd(t *testing.T) {
	if os.Getenv("DEXSTER_SIGKILL_CHILD") == "1" {
		return
	}
	root := newFixture(t)
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "agent@example.test")
	gitTest(t, root, "config", "user.name", "Dexster Test")
	// Slow verification creates a deterministic interval for the real SIGKILL.
	slow := `package main
import (
 "testing"
 "time"
)
func TestPauseForSIGKILL(t *testing.T) { time.Sleep(2 * time.Second) }
`
	if err := os.WriteFile(filepath.Join(root, "pause_test.go"), []byte(slow), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "math.go", "math_test.go", "pause_test.go")
	gitTest(t, root, "commit", "-qm", "baseline")
	baseline := gitTest(t, root, "rev-parse", "HEAD")
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	childEnv := append(os.Environ(), "DEXSTER_SIGKILL_CHILD=1", "DEXSTER_E2E_ROOT="+root, "DEXSTER_E2E_KRAKEN="+krakenRoot, "DEXSTER_E2E_URL="+server.URL)
	spawn := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSIGKILLAgentChild$", "-test.v")
		cmd.Env = childEnv
		return cmd
	}
	first := spawn()
	childLog, logErr := os.CreateTemp(t.TempDir(), "child-*.log")
	if logErr != nil {
		t.Fatal(logErr)
	}
	defer childLog.Close()
	first.Stdout, first.Stderr = childLog, childLog
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(statePath(root))
		if err == nil {
			var st TaskState
			if json.Unmarshal(data, &st) == nil {
				for _, rec := range st.Journal {
					if rec.Tool == "write_file" && rec.OK {
						// A successful DEXSTER journal write precedes KRAKEN's
						// acknowledgement. Killing in that window must fail
						// closed on resume. This test instead proves a fully
						// acknowledged write can resume safely.
						store, storeErr := memory.New(krakenRoot)
						if storeErr != nil {
							t.Fatal(storeErr)
						}
						manifest, readErr := store.ReadTask(root, "v19 verify real SIGKILL resume")
						if readErr != nil || manifest.Pending != nil || manifest.AcknowledgedIteration < rec.Iteration {
							continue
						}
						if err := first.Process.Signal(syscall.SIGKILL); err != nil {
							t.Fatal(err)
						}
						killed = true
						break
					}
				}
			}
		}
		if killed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !killed {
		_ = first.Process.Kill()
		_ = first.Wait()
		logBytes, _ := os.ReadFile(childLog.Name())
		t.Fatalf("never observed KRAKEN-acknowledged successful write; child log:\n%s", logBytes)
	}
	err := first.Wait()
	if err == nil {
		t.Fatal("first child unexpectedly exited normally")
	}
	if state, ok := first.ProcessState.Sys().(syscall.WaitStatus); !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Fatalf("first child not killed by SIGKILL: %v", err)
	}
	if head := gitTest(t, root, "rev-parse", "HEAD"); head != baseline {
		t.Fatal("commit happened before crash")
	}
	second := spawn()
	output, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("resumed child failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "resumed from persisted state") {
		t.Fatalf("no resume evidence:\n%s", output)
	}
	if !strings.Contains(string(output), "DONE after") {
		t.Fatalf("no DONE after resume:\n%s", output)
	}
	state, resumed, err := loadState(root, "v19 verify real SIGKILL resume")
	if err != nil || !resumed || !state.Done || state.GitCommit == "" {
		t.Fatalf("bad persisted state: %+v err=%v", state, err)
	}
	if got := gitTest(t, root, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("expected baseline+one commit, got %s", got)
	}
	if head := gitTest(t, root, "rev-parse", "HEAD"); head != state.GitCommit {
		t.Fatalf("receipt %s != HEAD %s", state.GitCommit, head)
	}
	if files := gitTest(t, root, "show", "--pretty=format:", "--name-only", "HEAD"); files != "math.go" {
		t.Fatalf("unexpected committed files %q", files)
	}
	third := spawn()
	output, err = third.CombinedOutput()
	if err != nil {
		t.Fatalf("idempotent restart: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "already completed") {
		t.Fatalf("completed task not recognized: %s", output)
	}
	if got := gitTest(t, root, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("duplicate Git commit: %s", got)
	}
	manifestStore, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manifestStore.ReadTask(root, "v19 verify real SIGKILL resume")
	if err != nil || !manifest.Done {
		t.Fatalf("KRAKEN not completed: %v / %+v", err, manifest)
	}
	t.Log(fmt.Sprintf("real SIGKILL -> resume -> verified one commit %s -> idempotent restart", state.GitCommit[:12]))
}
