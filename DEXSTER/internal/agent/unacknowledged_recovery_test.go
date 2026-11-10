package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
	"kraken/memory"
)

func TestUnacknowledgedWriteRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper bool
	}{{"matching", false}, {"modified", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := newFixture(t)
			memroot := t.TempDir()
			if err := os.Mkdir(filepath.Join(memroot, "events"), 0700); err != nil {
				t.Fatal(err)
			}
			store, err := memory.New(memroot)
			if err != nil {
				t.Fatal(err)
			}
			task := "recover write in split acknowledgement window"
			if _, err := store.BeginTask(root, task); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, "math.go"))
			if err != nil {
				t.Fatal(err)
			}
			after := []byte(strings.Replace(string(before), "return a - b", "return a + b", 1))
			if string(after) == string(before) {
				after = append(append([]byte{}, before...), []byte("\n// updated\n")...)
			}
			if err := store.Acknowledge(root, task, 1, "", false); err != nil {
				t.Fatal(err)
			}
			cp, err := store.SaveCheckpoint(root, "math.go", true, before)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PrepareWrite(root, task, 2, cp, after); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "math.go"), after, 0600); err != nil {
				t.Fatal(err)
			}
			if status, err := store.ReconcilePending(root, task); err != nil || status != "after" {
				t.Fatalf("reconcile %q %v", status, err)
			}
			st := &TaskState{Version: stateVersion, Task: task, TaskHash: taskHash(task), Iteration: 2,
				Evidence: Evidence{ReadFiles: map[string]string{"math.go": string(before)}, Inspected: true, WrotePath: "math.go", LastWriteIter: 2},
				Journal:  []ActionRecord{{Iteration: 1, Tool: "read_file", Path: "math.go", OK: true}, {Iteration: 2, Tool: "write_file", Path: "math.go", Checkpoint: cp, OK: true}},
			}
			if err := saveState(root, st); err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				if err := os.WriteFile(filepath.Join(root, "math.go"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			agent, err := New(root, "not-used", llm.New("http://127.0.0.1:1"), 2)
			if err != nil {
				t.Fatal(err)
			}
			agent.Memory = store
			err = agent.Run(task)
			manifest, readErr := store.ReadTask(root, task)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.tamper {
				if err == nil || !strings.Contains(err.Error(), "recovery blocked") {
					t.Fatalf("missing fail-closed: %v", err)
				}
				if manifest.AcknowledgedIteration != 1 {
					t.Fatalf("advanced acknowledgement on tampering: %d", manifest.AcknowledgedIteration)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "stopped after 2 iterations") {
					t.Fatalf("expected resume beyond recovery, got %v", err)
				}
				if manifest.AcknowledgedIteration != 2 {
					t.Fatalf("did not acknowledge verified write: %d", manifest.AcknowledgedIteration)
				}
			}
		})
	}
}
