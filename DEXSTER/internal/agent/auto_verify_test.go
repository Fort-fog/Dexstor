package agent

import (
	"kraken/memory"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tiny Qwen-like model can perform the edit and then emit only prose.
// The controller must verify the edit without spending another model call.
func TestAutoVerifyFinishesWhenModelCannotSelectVerificationTools(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "reads=[math.go]") {
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		}
		return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
	})
	a := newAgent(t, root, server.URL, 8)
	a.AutoVerify = true
	if err := a.Run("repair math automatically verified"); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("verification must not ask LLM; requests=%d", *requests)
	}
	want := []string{"read_file", "write_file", "git_diff", "run_tests"}
	for i, name := range want {
		if a.State.Journal[i].Tool != name || !a.State.Journal[i].OK {
			t.Fatalf("journal %d: %+v", i, a.State.Journal[i])
		}
	}
}

func TestAutoVerifyFailureReturnsErrorToAgentForRepair(t *testing.T) {
	root := newFixture(t)
	calls := 0
	server, requests := mockServer(t, func(prompt string) string {
		calls++
		switch calls {
		case 1:
			return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
		case 2:
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: package main\n\nfunc Add(a, b int) int { return -1 }\nEND_TOOL_CALL"
		case 3:
			if !strings.Contains(prompt, "tests are failing") {
				t.Errorf("missing test-failure hint")
			}
			return "TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL"
		case 4:
			return "TOOL_CALL\nname: write_file\npath: math.go\ncontent: " + fixedMath + "\nEND_TOOL_CALL"
		default:
			return "No tool call"
		}
	})
	a := newAgent(t, root, server.URL, 12)
	a.AutoVerify = true
	if err := a.Run("repair after test failure"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "math.go"))
	if !strings.Contains(string(data), "return 10") {
		t.Fatalf("source not repaired: %s", data)
	}
	if *requests != 4 {
		t.Fatalf("model calls = %d, want 4", *requests)
	}
	fails, successes := 0, 0
	for _, rec := range a.State.Journal {
		if rec.Tool == "run_tests" {
			if rec.OK {
				successes++
			} else {
				fails++
			}
		}
	}
	if fails != 1 || successes != 1 {
		t.Fatalf("test results: fails=%d successes=%d", fails, successes)
	}
}

func TestAutoVerifyAndKrakenAcknowledgements(t *testing.T) {
	root := newFixture(t)
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
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
	a.Memory = store
	task := "kraken auto verify integration"
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Done || manifest.AcknowledgedIteration != a.State.Iteration {
		t.Fatalf("not acknowledged: %+v", manifest)
	}
	if *requests != 2 {
		t.Fatalf("unexpected model requests: %d", *requests)
	}
}
