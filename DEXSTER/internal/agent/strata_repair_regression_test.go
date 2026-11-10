package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
	"kraken/memory"
)

func TestStrataRepeatingTestFailureDespiteSuccessfulWrites(t *testing.T) {
	a := &Agent{State: &TaskState{Journal: []ActionRecord{
		{Iteration: 1, Tool: "run_tests", OK: false, Summary: "malformed go.sum: wrong number of fields"},
		{Iteration: 2, Tool: "write_file", Path: "main.go", OK: true},
		{Iteration: 3, Tool: "run_tests", OK: false, Summary: "malformed go.sum: wrong number of fields"},
		{Iteration: 4, Tool: "write_file", Path: "main.go", OK: true},
		{Iteration: 5, Tool: "run_tests", OK: false, Summary: "malformed go.sum: wrong number of fields"},
	}}}
	msg, stop := a.repeatedFailure()
	if !stop || !strings.Contains(msg, "dependency") || !strings.Contains(msg, "malformed go.sum") {
		t.Fatalf("repeated environment error ignored: %q %v", msg, stop)
	}
	a.State.Journal[4].Summary = "undefined: handler"
	if _, stop := a.repeatedFailure(); stop {
		t.Fatal("changed root cause should reset identical-failure detector")
	}
}

func TestStrataManualVerificationRemainsReachable(t *testing.T) {
	root := t.TempDir()
	a, err := New(root, "unused", nil, 6)
	if err != nil {
		t.Fatal(err)
	}
	a.AutoVerify = false
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{}, WrotePath: "main.go", LastWriteIter: 3}}
	prompt := a.buildPrompt("write code")
	if !strings.Contains(prompt, "AUTOVERIFY=OFF") || !strings.Contains(prompt, "call git_diff") {
		t.Fatalf("manual verification tools are unreachable: %s", prompt)
	}
	if result := a.execute(llm.Call{Name: "declare_done"}); !result.OK {
		t.Fatalf("data project path unexpectedly rejected: %+v", result)
	}
}

func TestStrataDeclareDoneCannotClaimUnverifiedTests(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{}, WrotePath: "main.go", LastWriteIter: 3, LastDiffIter: 4}}
	result := a.execute(llm.Call{Name: "declare_done"})
	if result.OK || !strings.Contains(result.Error, "verification pending") {
		t.Fatalf("false DONE: %+v", result)
	}
}

func TestStrataRepairCompletesDespiteUnrelatedOperatorConfusableCode(t *testing.T) {
	root := newFixture(t)
	if err := os.WriteFile(filepath.Join(root, "legacy.go"), []byte("package main\nvar count = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitBaseline(t, root)
	if err := os.WriteFile(filepath.Join(root, "legacy.go"), []byte("package main\nvar cоunt = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server, _ := mockServer(t, func(prompt string) string {
		calls++
		switch calls {
		case 1:
			return `{"name":"read_file","path":"math.go","content":""}`
		case 2:
			return `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int { return 10 }\n"}`
		default:
			return `{"name":"declare_done","path":"","content":""}`
		}
	})
	a := newAgent(t, root, server.URL, 12)
	a.AutoVerify = true
	if err := a.Run("Fix math.go so Add(2,3) returns 10"); err != nil {
		t.Fatalf("repair blocked by operator-owned change: %v", err)
	}
	if !a.State.Done {
		t.Fatal("verified task did not finish")
	}
	if calls > 3 {
		t.Fatalf("wasted model calls: %d", calls)
	}
	b, err := os.ReadFile(filepath.Join(root, "legacy.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "cоunt") {
		t.Fatal("operator change overwritten")
	}
}

func TestStrataEndToEndRepairWithKrakenAndScope(t *testing.T) {
	root := newFixture(t)
	if err := os.WriteFile(filepath.Join(root, "legacy.go"), []byte("package main\nvar count=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitBaseline(t, root)
	memroot := t.TempDir()
	if err := os.Mkdir(filepath.Join(memroot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(memroot)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := mockServer(t, func(prompt string) string {
		if !strings.Contains(prompt, "reads=[math.go]") {
			return `{"name":"read_file","path":"math.go","content":""}`
		}
		return `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int { return 10 }\n"}`
	})
	a := newAgent(t, root, server.URL, 12)
	a.Memory = store
	a.AutoVerify = true
	a.WriteScope = []string{"math.go"}
	task := "Fix existing math.go and preserve legacy.go"
	if err := a.Run(task); err != nil {
		t.Fatalf("verified repair failed: %v", err)
	}
	if !a.State.Done || a.State.Evidence.LastDiffIter <= a.State.Evidence.LastWriteIter || a.State.Evidence.LastTestIter <= a.State.Evidence.LastWriteIter {
		t.Fatalf("evidence gate incomplete: %+v", a.State.Evidence)
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil || !manifest.Done || manifest.Verified == nil {
		t.Fatalf("KRAKEN missed verified result: %+v err=%v", manifest, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "legacy.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "count=1") {
		t.Fatal("unrelated operator file overwritten")
	}
}

func TestStrataConfusableAgentEditRepairsAfterFailedDiff(t *testing.T) {
	root := newFixture(t)
	gitBaseline(t, root)
	seq := 0
	server, _ := mockServer(t, func(prompt string) string {
		seq++
		switch seq {
		case 1:
			return `{"name":"read_file","path":"math.go","content":""}`
		case 2:
			return `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int { cоunt := 10; return cоunt }\n"}`
		case 3:
			if !strings.Contains(prompt, "GIT DIFF BLOCKED") || !strings.Contains(prompt, "U+043E") {
				t.Errorf("missing actual Unicode diagnostic after failed diff: %s", prompt)
			}
			return `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int { count := 10; return count }\n"}`
		default:
			return `{"name":"declare_done","path":"","content":""}`
		}
	})
	a := newAgent(t, root, server.URL, 12)
	a.AutoVerify = true
	if err := a.Run("Fix math.go for Add"); err != nil {
		t.Fatalf("repair unable to recover from failed diff: %v", err)
	}
	if !a.State.Done {
		t.Fatal("not VERIFIED")
	}
	failedDiff, successfulDiff, testsOK := false, false, false
	for _, rec := range a.State.Journal {
		if rec.Tool == "git_diff" && !rec.OK {
			failedDiff = true
		}
		if rec.Tool == "git_diff" && rec.OK {
			successfulDiff = true
		}
		if rec.Tool == "run_tests" && rec.OK {
			testsOK = true
		}
	}
	if !failedDiff || !successfulDiff || !testsOK {
		t.Fatalf("missing repair chain: %+v", a.State.Journal)
	}
}
