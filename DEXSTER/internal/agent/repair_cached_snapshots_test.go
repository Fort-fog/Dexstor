package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
)

func TestRepairPromptRetainsPreviousSmallReadForComparison(t *testing.T) {
	a := newAgent(t, newFixture(t), "http://localhost:1", 5)
	a.State = &TaskState{Evidence: Evidence{Inspected: true, ReadFiles: map[string]string{
		"income_sheet.csv": "ITEM,PRICE\nApple,3\n",
		"sales_data.csv":   "ITEM,SOLD\nApple,4\n",
	}}, Journal: []ActionRecord{
		{Iteration: 1, Tool: "read_file", Path: "income_sheet.csv", OK: true},
		{Iteration: 2, Tool: "read_file", Path: "sales_data.csv", OK: true},
	}}
	prompt := a.buildPrompt("repair existing records")
	for _, needed := range []string{"--- FILE: income_sheet.csv ---", "Apple,3", "--- FILE: sales_data.csv ---", "Apple,4"} {
		if !strings.Contains(prompt, needed) {
			t.Fatalf("cached small read not accessible: %s", needed)
		}
	}
}

func TestRepairRepeatedReadKeepsExternalEditsVisible(t *testing.T) {
	a := makeExactEditFixture(t)
	old, err := os.ReadFile(filepath.Join(a.Root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	a.State.Evidence.ReadFiles["main.go"] = string(old)
	a.State.Journal = append(a.State.Journal, ActionRecord{Iteration: 1, Tool: "read_file", Path: "main.go", OK: true})
	duplicate := a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if duplicate.OK {
		t.Fatalf("unchanged duplicate should be refused: %+v", duplicate)
	}
	if err := os.WriteFile(filepath.Join(a.Root, "main.go"), append(old, []byte("\n// manual change\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	fresh := a.execute(llm.Call{Name: "read_file", Args: map[string]string{"path": "main.go"}})
	if !fresh.OK || !strings.Contains(fresh.Output, "manual change") {
		t.Fatalf("external change not visible: %+v", fresh)
	}
}
