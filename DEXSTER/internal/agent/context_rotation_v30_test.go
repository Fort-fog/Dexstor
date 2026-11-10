package agent

import (
	"kraken/memory"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV30RotationPersistsEpochAndFullCASAcrossRestart(t *testing.T) {
	project := newFixture(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := "v30 stable immutable task"
	giant := strings.Repeat("ROTATE_V30_SOURCE_MARKER", 500)
	st := &TaskState{Version: stateVersion, Task: task, TaskHash: taskHash(task), Iteration: 2, Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"math.go": giant}}, Journal: []ActionRecord{{Iteration: 2, Tool: "read_file", Path: "math.go", OK: true}}}
	a := newAgent(t, project, "http://127.0.0.1:1", 4)
	a.Memory = store
	a.State = st
	if _, err := store.BeginTask(project, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(project, task, 2, "", false); err != nil {
		t.Fatal(err)
	}
	if err := a.markContextRotation(task, 2); err != nil {
		t.Fatal(err)
	}
	if err := a.markContextRotation(task, 2); err != nil {
		t.Fatal(err)
	}
	saved, resumed, err := loadState(project, task)
	if err != nil || !resumed {
		t.Fatalf("load state: %v resumed=%v", err, resumed)
	}
	if saved.ContextEpoch != 1 || !saved.ContextCompact || saved.LastRotationIteration != 2 {
		t.Fatalf("unexpected epoch state %+v", saved)
	}
	if saved.Task != task || saved.Evidence.ReadFiles["math.go"] != giant {
		t.Fatal("task or CAS snapshot lost")
	}
	b := newAgent(t, project, "http://127.0.0.1:1", 4)
	b.Memory = store
	b.State = saved
	b.compactPrompt = saved.ContextCompact
	if err := b.checkResumeCapsule(task); err != nil {
		t.Fatalf("KRAKEN capsule mismatch: %v", err)
	}
	prompt := b.buildPrompt(task)
	if strings.Contains(prompt, "ROTATE_V30_SOURCE_MARKER") {
		t.Fatal("unbounded source leaked after process restart")
	}
	if !strings.Contains(prompt, task) || !strings.Contains(prompt, "COLD RESUME") {
		t.Fatal("goal/cold instructions lost")
	}
}

func TestV30RotationWorksWithoutKraken(t *testing.T) {
	project := newFixture(t)
	task := "standalone persistent rotation"
	a := newAgent(t, project, "http://127.0.0.1:1", 2)
	a.State = &TaskState{Version: stateVersion, Task: task, TaskHash: taskHash(task), Iteration: 1, Evidence: Evidence{ReadFiles: map[string]string{"math.go": "exact CAS"}}}
	if err := a.markContextRotation(task, 1); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadState(project, task)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ContextCompact || loaded.ContextEpoch != 1 || loaded.Evidence.ReadFiles["math.go"] != "exact CAS" {
		t.Fatal("persisted rotation incomplete")
	}
}
