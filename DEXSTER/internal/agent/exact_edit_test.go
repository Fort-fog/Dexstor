package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
	"kraken/memory"
)

func largeWebsiteGo() string {
	return "package main\n\nvar theme = `" + strings.Repeat(".card { color: cyan; }\n", 80) + "`\n" +
		"var image = \"assets/old.png\"\nfunc main() {}\n"
}

func makeExactEditFixture(t *testing.T) *Agent {
	t.Helper()
	root := t.TempDir()
	content := largeWebsiteGo()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "local-test", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	a.AutoVerify = true
	a.State = &TaskState{Task: "Доработай существующий сайт и сохрани исходный неоновый дизайн. Замени картинку.", Evidence: Evidence{ReadFiles: map[string]string{"main.go": content}, Inspected: true}}
	if err := a.initializeConstraints(nil); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestExactReplaceUsesCASAndPreservesOtherBytes(t *testing.T) {
	a := makeExactEditFixture(t)
	modelCall := llm.Call{Name: "replace_in_file", Args: map[string]string{"path": "main.go", "old": "assets/old.png", "new": "assets/neon.png"}}
	transformed, err := a.prepareExactReplacement(modelCall)
	if err != nil {
		t.Fatal(err)
	}
	if transformed.Name != "write_file" {
		t.Fatalf("tool=%s", transformed.Name)
	}
	original := largeWebsiteGo()
	if transformed.Args["content"] != strings.Replace(original, "assets/old.png", "assets/neon.png", 1) {
		t.Fatal("other source bytes mutated")
	}
	// Applying through the real write_file path enforces CAS and pre-write checkpoint.
	a.validatedPatch, a.validatedContent = transformed.PathArg(), transformed.Args["content"]
	result := a.execute(transformed)
	a.validatedPatch, a.validatedContent = "", ""
	if !result.OK {
		t.Fatalf("rejected safe patch: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(a.Root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != transformed.Args["content"] {
		t.Fatal("wrong bytes on disk")
	}
	a.applyEvidence(transformed, result, 2)
	if a.State.Evidence.ReadFiles["main.go"] != string(got) {
		t.Fatal("CAS snapshot not advanced")
	}
}

func TestExactReplaceRejectsMissingDuplicatedAndStale(t *testing.T) {
	a := makeExactEditFixture(t)
	cases := []struct{ old, new, needle string }{
		{"", "new", "nonempty"},
		{"missing", "new", "0 times"},
		{".card { color: cyan; }", ".card { color: blue; }", "80 times"},
		{"assets/old.png", "assets/old.png", "nonempty"},
	}
	for _, c := range cases {
		_, err := a.prepareExactReplacement(llm.Call{Name: "replace_in_file", Args: map[string]string{"path": "main.go", "old": c.old, "new": c.new}})
		if err == nil || !strings.Contains(err.Error(), c.needle) {
			t.Fatalf("case %+v: %v", c, err)
		}
	}
	// Snapshot remains trusted but an outside editor changed the file: must
	// reject before any unauthorized patch or checkpoint can alter it.
	modified := strings.Replace(largeWebsiteGo(), "assets/old.png", "assets/manual.png", 1)
	if err := os.WriteFile(filepath.Join(a.Root, "main.go"), []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	call, err := a.prepareExactReplacement(llm.Call{Name: "replace_in_file", Args: map[string]string{"path": "main.go", "old": "assets/old.png", "new": "assets/new.png"}})
	if err != nil {
		t.Fatal(err)
	}
	a.validatedPatch, a.validatedContent = call.PathArg(), call.Args["content"]
	res := a.execute(call)
	a.validatedPatch, a.validatedContent = "", ""
	if res.OK {
		t.Fatal("CAS allowed overwriting external modification")
	}
	now, err := os.ReadFile(filepath.Join(a.Root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(now) != modified {
		t.Fatal("CAS failed to preserve external changes")
	}
}

func TestExistingLargeWebsiteAuditedFullWritePreservesOtherBytes(t *testing.T) {
	a := makeExactEditFixture(t)
	raw := strings.Replace(largeWebsiteGo(), "assets/old.png", "assets/next.png", 1)
	res := a.execute(llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": raw}})
	if !res.OK {
		t.Fatalf("CAS-backed full edit rejected: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(a.Root, "main.go"))
	if err != nil || string(got) != raw {
		t.Fatalf("unrelated bytes changed: %v", err)
	}
	// Even a full-file rewrite must not bypass stale-source CAS protection.
	stale := a.execute(llm.Call{Name: "write_file", Args: map[string]string{"path": "main.go", "content": largeWebsiteGo()}})
	if stale.OK {
		t.Fatal("stale CAS overwrite accepted")
	}
}

func TestExactReplaceProducesKrakenPrewriteCheckpoint(t *testing.T) {
	a := makeExactEditFixture(t)
	memRoot := filepath.Join(t.TempDir(), "KRAKEN")
	for _, dir := range []string{memRoot, filepath.Join(memRoot, "events"), filepath.Join(memRoot, "checkpoints")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := memory.New(memRoot)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory = store
	a.State.TaskHash = taskHash(a.State.Task)
	a.State.Iteration = 1
	_, err = store.BeginTask(a.Root, a.State.Task)
	if err != nil {
		t.Fatal(err)
	}
	call, err := a.prepareExactReplacement(llm.Call{Name: "replace_in_file", Args: map[string]string{"path": "main.go", "old": "assets/old.png", "new": "assets/blue.png"}})
	if err != nil {
		t.Fatal(err)
	}
	a.validatedPatch, a.validatedContent = call.PathArg(), call.Args["content"]
	res := a.execute(call)
	a.validatedPatch, a.validatedContent = "", ""
	if !res.OK {
		t.Fatalf("write failed: %+v", res)
	}
	if a.pendingCheckpoint == "" {
		t.Fatal("no checkpoint for exact edit")
	}
	plan, err := store.PreviewRollback(a.pendingCheckpoint, a.Root)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256([]byte(largeWebsiteGo()))
	if plan.RestoredSHA256 != hex.EncodeToString(before[:]) {
		t.Fatalf("KRAKEN checkpoint did not preserve prior content: %+v", plan)
	}
	if !strings.HasSuffix(plan.Path, "main.go") {
		t.Fatal(fmt.Sprint(plan))
	}
}

func TestRealAgentExactEditThroughModelToDone(t *testing.T) {
	project := t.TempDir()
	original := largeWebsiteGo()
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module exact-edit-fixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	seq := 0
	server, count := mockServer(t, func(prompt string) string {
		seq++
		if seq == 1 {
			return `{"name":"read_file","path":"main.go","content":""}`
		}
		return `{"name":"replace_in_file","path":"main.go","old":"assets/old.png","new":"assets/neon.png"}`
	})
	a := newAgent(t, project, server.URL, 15)
	a.AutoVerify = true
	task := "Доработай существующий сайт, сохрани дизайн. Замени ссылку на одну картинку."
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done {
		t.Fatal("not DONE")
	}
	if a.State.Iteration != 4 {
		t.Fatalf("want 4 iterations, got %d", a.State.Iteration)
	}
	if a.State.Usage.Requests != 2 || *count != 2 {
		t.Fatalf("extra model calls: %d / %d", a.State.Usage.Requests, *count)
	}
	got, err := os.ReadFile(filepath.Join(project, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(original, "assets/old.png", "assets/neon.png", 1)
	if string(got) != expected {
		t.Fatal("source was not changed in one precise location")
	}
}

func TestRepeatedReadWithoutNewMutationIsRefused(t *testing.T) {
	a := makeExactEditFixture(t)
	a.State.Journal = []ActionRecord{{Iteration: 1, Tool: "read_file", Path: "main.go", OK: true}}
	result := a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if result.OK || !strings.Contains(result.Error, "replace_in_file") {
		t.Fatalf("read loop not stopped: %+v", result)
	}
	// A write genuinely changes the source and permits another read.
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 2, Tool: "write_file", Path: "main.go", OK: true})
	result = a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if !result.OK {
		t.Fatalf("new epoch read rejected: %+v", result)
	}
	// CAS stale rejection permits a fresh read even inside same epoch.
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 3, Tool: "read_file", Path: "main.go", OK: true})
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 4, Tool: "write_file", Path: "main.go", Summary: "stale snapshot", OK: false})
	result = a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if !result.OK {
		t.Fatalf("CAS refresh was blocked: %+v", result)
	}
}
