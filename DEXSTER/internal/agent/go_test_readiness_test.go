package agent

import (
	"encoding/json"
	"kraken/memory"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
)

// Regression for the actual 35-iteration REST API log: writing incomplete
// scaffolding must not pay for two verification steps per source file.
func TestRequestedGoTestsAreBuiltBeforeVerificationAndFinishAtLimit(t *testing.T) {
	root := t.TempDir()
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
func Add(a,b int) int { return a+b }
`
	testSource := `package main
import "testing"
func TestAdd(t *testing.T) { if Add(2,3)!=5 {t.Fatal("broken")} }
`
	actions := []llm.Call{
		{Name: "list_files", Args: map[string]string{"path": "."}},
		{Name: "write_file", Args: map[string]string{"path": "go.mod", "content": "module fixture\n\ngo 1.21\n"}},
		{Name: "write_file", Args: map[string]string{"path": "main.go", "content": source}},
		{Name: "write_file", Args: map[string]string{"path": "main_test.go", "content": testSource}},
	}
	n := 0
	server, requests := mockServer(t, func(prompt string) string {
		if n >= len(actions) {
			t.Errorf("unnecessary model request %d", n+1)
			return "invalid"
		}
		data, err := json.Marshal(actions[n].Args)
		if err != nil {
			t.Fatal(err)
		}
		// The parser expects top-level name/path/content, not nested Args.
		m := map[string]string{"name": actions[n].Name}
		for k, v := range actions[n].Args {
			m[k] = v
		}
		data, err = json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		n++
		return string(data)
	})
	a := newAgent(t, root, server.URL, 6)
	a.AutoVerify = true
	a.Memory = store
	task := "Создай рабочий API на чистом Go. Напиши Go-тесты."
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done || a.State.Iteration != 6 {
		t.Fatalf("unfinished: %+v", a.State)
	}
	if *requests != 4 {
		t.Fatalf("model requests=%d want 4", *requests)
	}
	for i, want := range []string{"list_files", "write_file", "write_file", "write_file", "git_diff", "run_tests"} {
		if a.State.Journal[i].Tool != want || !a.State.Journal[i].OK {
			t.Fatalf("journal step %d %+v", i, a.State.Journal[i])
		}
	}
	m, err := store.ReadTask(root, task)
	if err != nil || !m.Done || m.AcknowledgedIteration != 6 {
		t.Fatalf("KRAKEN manifest %+v: %v", m, err)
	}
}

func TestTrustedCASAdvancesOnWriteAndRejectsExternalChange(t *testing.T) {
	root := t.TempDir()
	a, err := New(root, "mock", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{Task: "Create Go code", Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{}}}
	if err := a.initializeConstraints(nil); err != nil {
		t.Fatal(err)
	}
	first := llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": "package main\n"}}
	res := a.execute(first)
	if !res.OK {
		t.Fatalf("first write: %s", res.Error)
	}
	a.applyEvidence(first, res, 1)
	second := llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": "package main\n// updated\n"}}
	res = a.execute(second)
	if !res.OK {
		t.Fatalf("second write to our own file: %s", res.Error)
	}
	a.applyEvidence(second, res, 2)
	if got := a.State.Evidence.ReadFiles["main.go"]; got != second.Args["content"] {
		t.Fatal("missing trusted CAS snapshot")
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// external tampering\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third := llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": "package main\n// yet another revision\n"}}
	res = a.execute(third)
	if res.OK || !strings.Contains(strings.ToLower(res.Error), "stale snapshot") {
		t.Fatalf("failed to reject external mutation: %+v", res)
	}
}
