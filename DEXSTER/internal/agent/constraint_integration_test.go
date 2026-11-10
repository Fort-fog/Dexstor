package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConstraintGateBlocksModelChangingProtectedFunction(t *testing.T) {
	root := t.TempDir()
	initial := "package main\nfunc Add(a,b int) int { return a+b }\nfunc Multiply(a,b int) int { return a+b }\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	server, requests := mockServer(t, func(prompt string) string {
		return `{"name":"write_file","path":"main.go","content":"package main\nfunc Add(a,b int) int { return 10 }\nfunc Multiply(a,b int) int { return a*b }\n"}`
	})
	a := newAgent(t, root, server.URL, 4)
	a.Protected = []string{"main.go:Multiply"}
	if err := a.Run("change Add; leave Multiply alone"); err == nil {
		t.Fatal("expected iteration budget exit")
	}
	if *requests == 0 {
		t.Fatal("no model calls")
	}
	data, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Fatalf("protected file changed: %s", data)
	}
	rejected := false
	for _, rec := range a.State.Journal {
		if rec.Tool == "write_file" && !rec.OK && strings.Contains(rec.Summary, "constraint violation") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("missing constraint rejection: %+v", a.State.Journal)
	}
	restored, _, err := loadState(root, "change Add; leave Multiply alone")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Constraints) != 1 || restored.Constraints[0].Function != "Multiply" {
		t.Fatalf("missing persisted constraints: %+v", restored.Constraints)
	}
	b := newAgent(t, root, server.URL, 7)
	b.Protected = []string{"main.go:Multiply"}
	if err := b.initializeConstraints(restored); err != nil {
		t.Fatal(err)
	}
	if err := b.gate.CheckWrite("main.go", []byte(strings.Replace(initial, "return a+b }", "return a*b }", -1))); err == nil {
		t.Fatal("resumed gate bypassed")
	}
}

func TestConstraintGateDoesNotAllowChangedRulesOnResume(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Add(){}\nfunc Multiply(){}\n"), 0600)
	a, err := New(root, "unused", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	a.Protected = []string{"main.go:Multiply"}
	if err := a.initializeConstraints(nil); err != nil {
		t.Fatal(err)
	}
	state := &TaskState{Task: "test", TaskHash: taskHash("test"), Constraints: a.gate.Rules}
	b, err := New(root, "unused", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	b.Protected = []string{"main.go:Add"}
	if err := b.initializeConstraints(state); err == nil {
		t.Fatal("changing rules should be blocked")
	}
}

func TestConstraintGateAllowsLegalEditAndCompletion(t *testing.T) {
	root := newFixture(t)
	// The normal math.go fixture only contains Add; protect the existing function
	// in a separate file, while allowing the agent to fix math.go normally.
	readme := filepath.Join(root, "protected.txt")
	if err := os.WriteFile(readme, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	server, requests := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "reads=[math.go]") {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 8)
	a.AutoVerify = true
	a.Protected = []string{"protected.txt"}
	if err := a.Run("fix Add without changing protected.txt"); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("unexpected calls: %d", *requests)
	}
	if err := a.gate.Verify(); err != nil {
		t.Fatal(err)
	}
}
