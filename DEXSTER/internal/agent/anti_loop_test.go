package agent

import (
	"os"
	"path/filepath"
	"strings"

	"dexster/internal/llm"
	"testing"
)

func TestAntiLoopDetectsConsecutiveReadsAndResetsOnWrite(t *testing.T) {
	a := newAgent(t, newFixture(t), "http://localhost:1", 5)
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"math.go": "package main\n"}}, Journal: []ActionRecord{
		{Iteration: 1, Tool: "read_file", Path: "math.go", OK: true},
		{Iteration: 2, Tool: "read_file", Path: "math.go", OK: true},
		{Iteration: 3, Tool: "read_file", Path: "math.go", OK: true},
	}}
	path, n := a.redundantReadStreak()
	if path != "math.go" || n != 3 {
		t.Fatalf("got %q %d", path, n)
	}
	prompt := a.buildPrompt("fix math")
	if !strings.Contains(prompt, "ANTI-LOOP:") || !strings.Contains(prompt, "EXECUTION PLAN") || !strings.Contains(prompt, "NEXT STEP: use trusted snapshot of math.go") {
		t.Fatalf("missing guided plan or correction: %s", prompt)
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 4, Tool: "write_file", Path: "math.go", OK: true})
	if _, n := a.redundantReadStreak(); n != 0 {
		t.Fatalf("repeat not reset after write: %d", n)
	}
	if strings.Contains(a.buildPrompt("fix math"), "ANTI-LOOP:") {
		t.Fatal("stale correction after write")
	}
}

func TestTokenBudgetIncludesOnlyLatestReadSource(t *testing.T) {
	a := newAgent(t, newFixture(t), "http://localhost:1", 5)
	older := strings.Repeat("OLDER_SOURCE_UNIQUE", 300)
	recent := strings.Repeat("CURRENT_SOURCE", 100)
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{"old.go": older, "math.go": recent}}, Journal: []ActionRecord{
		{Iteration: 1, Tool: "read_file", Path: "old.go", OK: true},
		{Iteration: 2, Tool: "read_file", Path: "math.go", OK: true},
	}}
	prompt := a.buildPrompt("fix math")
	if strings.Contains(prompt, "OLDER_SOURCE_UNIQUE") {
		t.Fatal("old source leaked into prompt")
	}
	if !strings.Contains(prompt, "CURRENT_SOURCE") {
		t.Fatal("active source missing")
	}
	if !strings.Contains(prompt, "old.go (cached;") {
		t.Fatal("old source cache summary missing")
	}
	if a.State.Evidence.ReadFiles["old.go"] != older {
		t.Fatal("snapshot was mutated")
	}
	if len(prompt) >= len(older)+len(recent) {
		t.Fatalf("no prompt savings: %d", len(prompt))
	}
}

// Repeated successful listing without a mutation should not consume more model
// iterations. An external filesystem change invalidates the comparison.
func TestRepeatedListRefusedUntilDirectoryChanges(t *testing.T) {
	a := makeExactEditFixture(t)
	first := a.execute(llm.Call{Name: "list_files", Args: map[string]string{"path": "."}})
	if !first.OK {
		t.Fatalf("initial listing: %+v", first)
	}
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 1, Tool: "list_files", Path: ".", Summary: snippet(resultText(first), 3500), OK: true})
	second := a.execute(llm.Call{Name: "list_files", Args: map[string]string{"path": "."}})
	if second.OK || !strings.Contains(second.Error, "ANTI-LOOP") {
		t.Fatalf("redundant list not refused: %+v", second)
	}
	if err := os.WriteFile(filepath.Join(a.Root, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	third := a.execute(llm.Call{Name: "list_files", Args: map[string]string{"path": "."}})
	if !third.OK || !strings.Contains(third.Output, "new.txt") {
		t.Fatalf("fresh directory change not detected: %+v", third)
	}
}

func TestExternalFileEditAllowsFreshRead(t *testing.T) {
	a := makeExactEditFixture(t)
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 1, Tool: "read_file", Path: "main.go", OK: true})
	modified := strings.Replace(largeWebsiteGo(), "assets/old.png", "assets/manual.png", 1)
	if err := os.WriteFile(filepath.Join(a.Root, "main.go"), []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	res := a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if !res.OK || res.Output != modified {
		t.Fatalf("external change must be readable: %+v", res)
	}
}
