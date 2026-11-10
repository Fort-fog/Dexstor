//go:build linux

package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests use actual independent processes and SIGKILL. The child writes
// a durable intent, moves to a precise transaction phase and announces READY;
// the parent SIGKILLs it, then recovers in ANOTHER process.
func TestSIGKILLRecovery(t *testing.T) {
	if phase := os.Getenv("KRAKEN_SIGKILL_CHILD_PHASE"); phase != "" {
		sigkillChild(phase)
		return
	}
	for _, phase := range []string{"intent", "manifest", "unlink"} {
		t.Run(phase, func(t *testing.T) {
			s := cleanupStore(t)
			cp := cleanupTask(t, s, "finished", true, false)
			var m TaskManifest
			entries, err := os.ReadDir(filepath.Join(s.Root, "tasks"))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					raw, err := os.ReadFile(filepath.Join(s.Root, "tasks", e.Name()))
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &m); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSIGKILLRecovery$")
			cmd.Env = append(os.Environ(), "KRAKEN_SIGKILL_CHILD_PHASE="+phase, "KRAKEN_SIGKILL_ROOT="+s.Root, "KRAKEN_SIGKILL_CP="+cp, "KRAKEN_SIGKILL_TASK="+m.Task, "KRAKEN_SIGKILL_PROJECT="+m.Project)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "READY\n" {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("child not ready: %q %v", line, err)
			}
			// The lock must be held by the still-running child.
			if l, err := s.LockExclusive(); err == nil {
				l.Close()
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal("concurrent store access accepted")
			}
			if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("SIGKILL did not terminate child")
			}
			// New process must acquire released flock and complete recovery.
			recovery := exec.Command(os.Args[0], "-test.run=^TestSIGKILLRecoverChild$")
			recovery.Env = append(os.Environ(), "KRAKEN_SIGKILL_RECOVER_ROOT="+s.Root)
			out, err := recovery.CombinedOutput()
			if err != nil {
				t.Fatalf("recovery process: %v: %s", err, out)
			}
			want := "RECOVERED=1"
			if phase == "unlink" {
				want = "RECOVERED=0"
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("unexpected recovery output: %q want %s", out, want)
			}
			after, err := s.ReadTask(m.Project, m.Task)
			if err != nil || len(after.Checkpoints) != 0 {
				t.Fatalf("broken manifest: %+v %v", after, err)
			}
			if _, err := os.Lstat(cp); !os.IsNotExist(err) {
				t.Fatalf("checkpoint not pruned: %v", err)
			}
			if _, err := os.Lstat(s.cleanupIntentPath()); !os.IsNotExist(err) {
				t.Fatalf("intent not cleared: %v", err)
			}
			// Idempotent third independent process.
			recovery = exec.Command(os.Args[0], "-test.run=^TestSIGKILLRecoverChild$")
			recovery.Env = append(os.Environ(), "KRAKEN_SIGKILL_RECOVER_ROOT="+s.Root)
			out, err = recovery.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "RECOVERED=0") {
				t.Fatalf("repeat recovery: %s %v", out, err)
			}
		})
	}
}
func sigkillChild(phase string) {
	s := &Store{Root: os.Getenv("KRAKEN_SIGKILL_ROOT")}
	l, err := s.LockExclusive()
	if err != nil {
		panic(err)
	}
	defer l.Close()
	cp := os.Getenv("KRAKEN_SIGKILL_CP")
	raw, err := os.ReadFile(cp)
	if err != nil {
		panic(err)
	}
	m, err := s.ReadTask(os.Getenv("KRAKEN_SIGKILL_PROJECT"), os.Getenv("KRAKEN_SIGKILL_TASK"))
	if err != nil {
		panic(err)
	}
	in := cleanupIntent{Version: 1, TaskID: m.ID, Project: m.Project, Checkpoints: []cleanupCheckpoint{digestForTest(cp, raw)}}
	if err := s.writeCleanupIntent(in); err != nil {
		panic(err)
	}
	if phase == "manifest" || phase == "unlink" {
		m.Checkpoints = nil
		if err := s.writeTask(m); err != nil {
			panic(err)
		}
	}
	if phase == "unlink" {
		if err := os.Remove(cp); err != nil {
			panic(err)
		}
		if err := syncDirectory(filepath.Dir(cp)); err != nil {
			panic(err)
		}
	}
	fmt.Println("READY")
	// SIGKILL from the parent is the only expected exit.
	for {
		time.Sleep(time.Hour)
	}
}
func TestSIGKILLRecoverChild(t *testing.T) {
	root := os.Getenv("KRAKEN_SIGKILL_RECOVER_ROOT")
	if root == "" {
		return
	}
	s := &Store{Root: root}
	l, err := s.LockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	n, err := s.RecoverCleanup()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RECOVERED=%d\n", n)
}
