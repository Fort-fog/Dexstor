package agent

import (
	"encoding/json"
	"kraken/memory"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestColdResumeOverRepeatedProcessRestarts(t *testing.T) {
	project := newFixture(t)
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	task := "Fix math with interrupted context"
	// Completion now happens in the same iteration as the final PASS.
	for _, max := range []int{1, 2, 3, 4, 8} {
		a := newAgent(t, project, server.URL, max)
		a.Memory = store
		a.MaxPromptTokens = 3000
		err = a.Run(task)
		if max < 4 && err == nil {
			t.Fatal("expected iteration stop, not completion")
		}
		if max >= 4 && err != nil {
			t.Fatalf("cold continuation: %v", err)
		}
		if max < 4 {
			m, err := store.ReadTask(project, task)
			if err != nil {
				t.Fatal(err)
			}
			if m.Resume == nil || m.Resume.Iteration != max {
				t.Fatalf("missing capsule at %d", max)
			}
		}
	}
	m, err := store.ReadTask(project, task)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Done {
		t.Fatal("task did not finish")
	}
}

func TestResumeRejectsTamperedCapsule(t *testing.T) {
	project := newFixture(t)
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, evidenceDrivenMock())
	a := newAgent(t, project, server.URL, 1)
	a.Memory = store
	_ = a.Run("tamper test")
	m, err := store.ReadTask(project, "tamper test")
	if err != nil {
		t.Fatal(err)
	}
	if m.Resume == nil {
		t.Fatal("capsule missing")
	}
	path := filepath.Join(project, ".dexster", "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st TaskState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	st.Journal[0].Summary = "tampered"
	raw, _ = json.Marshal(st)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a = newAgent(t, project, server.URL, 4)
	a.Memory = store
	if err := a.Run("tamper test"); err == nil || !strings.Contains(err.Error(), "capsule/journal mismatch") {
		t.Fatalf("wanted mismatch, got %v", err)
	}
}

func TestCompactPromptLeavesCASSnapshotButNotFullSource(t *testing.T) {
	project := newFixture(t)
	a := newAgent(t, project, "http://localhost:1", 2)
	content := strings.Repeat("EXTRA_MARKER", 900)
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"math.go": content}}}
	full := a.buildPrompt("task")
	a.compactPrompt = true
	cold := a.buildPrompt("task")
	if strings.Contains(cold, "EXTRA_MARKER") || len(cold) >= len(full) {
		t.Fatal("not compact")
	}
	if a.State.Evidence.ReadFiles["math.go"] != content {
		t.Fatal("lost CAS source snapshot")
	}
}

func TestContextWatchdogRotatesBeforeLargePrompt(t *testing.T) {
	project := newFixture(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot in state is intentionally much larger than the compact prompt.
	giant := strings.Repeat("UNREPEATED_SOURCE_MARKER", 300)
	st := &TaskState{Version: stateVersion, Task: "rotate", TaskHash: taskHash("rotate"), Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"math.go": giant}}, Journal: []ActionRecord{{Iteration: 0, Tool: "read_file", Path: "math.go", OK: true}}}
	if err := saveState(project, st); err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "UNREPEATED_SOURCE_MARKER") {
			t.Error("oversized snapshot leaked into cold prompt")
		}
		return "TOOL_CALL\nname: list_files\nEND_TOOL_CALL"
	})
	a := newAgent(t, project, server.URL, 1)
	a.Memory = store
	// Small enough to accommodate compact protocol, not full read snapshot.
	a.MaxPromptTokens = 1200
	_ = a.Run("rotate")
	if !a.compactPrompt {
		t.Fatal("watchdog did not trigger")
	}
}

// Repeated process recreation must preserve exactly one logical task even
// when the model intermittently responds with non-tool prose.
func TestRepeatedRestartWindowsWithMalformedModelReplies(t *testing.T) {
	project := newFixture(t)
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	validCalls := 0
	mock := evidenceDrivenMock()
	server, _ := mockServer(t, func(prompt string) string {
		// Simulate a struggling small model every second generation,
		// without ever inventing or executing an invalid file operation.
		validCalls++
		if validCalls <= 14 && validCalls%2 == 1 {
			return "I should inspect the code first."
		}
		return mock(prompt)
	})
	task := "twelve restarts with malformed LLM replies"
	done := false
	for limit := 1; limit <= 20; limit++ {
		a := newAgent(t, project, server.URL, limit)
		a.Memory = store
		a.AutoVerify = true
		a.MaxPromptTokens = 2200
		err := a.Run(task)
		if err == nil {
			done = true
			break
		}
		if !strings.Contains(err.Error(), "without completion evidence") {
			t.Fatalf("restart %d: %v", limit, err)
		}
		manifest, err := store.ReadTask(project, task)
		if err != nil || manifest.Resume == nil || manifest.AcknowledgedIteration != limit {
			t.Fatalf("restart %d inconsistent checkpoint: %+v %v", limit, manifest, err)
		}
	}
	if !done {
		t.Fatal("agent never finished across restarts")
	}
	m, err := store.ReadTask(project, task)
	if err != nil || !m.Done {
		t.Fatalf("task did not finish: %+v %v", m, err)
	}
}
