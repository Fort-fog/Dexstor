package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitBaseline(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "DEXSTER Test"},
		{"config", "user.email", "dexster@test.invalid"},
		{"add", "."},
		{"commit", "-qm", "baseline"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestRepairModeClassificationIsProjectAgnostic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.toml"), []byte("mode='old'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{
		"Исправь существующий конфиг и удали старую точку.",
		"Замени old на new в settings.toml.",
		"Update the existing configuration without rebuilding the project.",
	} {
		if got := a.classifyTaskMode(task); got != taskModeRepair {
			t.Fatalf("task %q classified as %q", task, got)
		}
	}
}

func TestRepairSourcePreflightListsExplicitDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "a.png"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{Task: "SOURCE:\nКаталог `assets/`.\nTARGET:\nconfig.txt\nACTION:\nЗамени значение.", TaskMode: taskModeRepair, Evidence: Evidence{ReadFiles: map[string]string{}}}
	call, ok := a.nextRepairPreflight()
	if !ok || call.Name != "list_files" || filepath.Clean(call.PathArg()) != "assets" {
		t.Fatalf("preflight=%+v ok=%v", call, ok)
	}
}

func TestRepairReplaceBudgetSurvivesInterleavedReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "test", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	a.State = &TaskState{TaskMode: taskModeRepair, Evidence: Evidence{ReadFiles: map[string]string{"config.txt": "old\n"}}, Journal: []ActionRecord{
		{Iteration: 1, Tool: "replace_in_file", Path: "config.txt", Summary: "old snippet appears 0 times", OK: false},
		{Iteration: 2, Tool: "read_file", Path: "config.txt", OK: true},
		{Iteration: 3, Tool: "replace_in_file", Path: "config.txt", Summary: "old snippet appears 0 times", OK: false},
	}}
	if got := a.repairReplaceFailures("config.txt"); got != 2 {
		t.Fatalf("failures=%d", got)
	}
	if got := a.repairFallbackPath(); got != "config.txt" {
		t.Fatalf("fallback=%q", got)
	}
	if !strings.Contains(a.repairPolicyText(), "write_file") {
		t.Fatal("fallback directive missing")
	}
}

func TestRepairModuleFallsBackToFullWriteAndFinishes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "one.bin"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("mode=old\nkeep=yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitBaseline(t, root)

	seq := 0
	server, calls := mockServer(t, func(prompt string) string {
		seq++
		switch seq {
		case 1:
			if !strings.Contains(prompt, "one.bin") {
				t.Errorf("repair SOURCE preflight result missing from prompt")
			}
			return `{"name":"read_file","path":"config.txt","content":""}`
		case 2, 3:
			return `{"name":"replace_in_file","path":"config.txt","old":"not-present","new":"mode=new"}`
		case 4:
			if !strings.Contains(prompt, "REPAIR FALLBACK REQUIRED") {
				t.Errorf("fallback directive missing after two exact-edit failures")
			}
			return `{"name":"write_file","path":"config.txt","content":"mode=new\nkeep=yes\n"}`
		case 5:
			return `{"name":"declare_done","path":"","content":""}`
		default:
			return `{"name":"declare_done","path":"","content":""}`
		}
	})
	a := newAgent(t, root, server.URL, 15)
	a.AutoVerify = true
	task := "SOURCE:\nКаталог `assets/`.\nTARGET:\nconfig.txt\nACTION:\nИсправь существующий конфиг и сохрани остальные значения.\nVERIFY:\nПроверь diff.\nREPORT:\nСообщи что изменено."
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	if a.State.TaskMode != taskModeRepair {
		t.Fatalf("mode=%q", a.State.TaskMode)
	}
	if *calls != 5 {
		t.Fatalf("LLM calls=%d want=5", *calls)
	}
	got, err := os.ReadFile(filepath.Join(root, "config.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mode=new\nkeep=yes\n" {
		t.Fatalf("config=%q", got)
	}
	var sourcePreflight bool
	for _, rec := range a.State.Journal {
		if rec.Tool == "list_files" && filepath.Clean(rec.Path) == "assets" && rec.OK {
			sourcePreflight = true
		}
	}
	if !sourcePreflight {
		t.Fatal("repair controller did not inspect SOURCE")
	}
}
