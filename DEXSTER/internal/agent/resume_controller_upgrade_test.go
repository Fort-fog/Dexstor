package agent

import (
	"encoding/json"
	"kraken/memory"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeControllerUpgradeDoesNotInvalidatePersistedCapsule(t *testing.T) {
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
	task := "Исправь math.go"

	first := newAgent(t, project, server.URL, 1)
	first.Memory = store
	if err := first.Run(task); err == nil {
		t.Fatal("expected interrupted first run")
	}

	st, resumed, err := loadState(project, task)
	if err != nil || !resumed {
		t.Fatalf("load state: resumed=%v err=%v", resumed, err)
	}
	// Simulate a state written by the previous controller build before
	// TaskMode existed/populated. KRAKEN's capsule is updated to match that
	// exact persisted legacy state.
	st.TaskMode = ""
	st.PlanStage = ""
	if err := saveState(project, st); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveResume(project, task, memory.ResumeCapsule{
		Iteration:   st.Iteration,
		StateSHA256: memory.StateDigest(raw),
		NextStep:    "continue",
	}); err != nil {
		t.Fatal(err)
	}

	second := newAgent(t, project, server.URL, 8)
	second.Memory = store
	if err := second.Run(task); err != nil {
		t.Fatalf("controller-upgrade resume failed: %v", err)
	}
	if second.State.TaskMode != taskModeRepair {
		t.Fatalf("mode=%q, want %q", second.State.TaskMode, taskModeRepair)
	}
}
