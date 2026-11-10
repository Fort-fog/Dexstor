package agent

import (
	"dexster/internal/llm"
	"testing"
)

func TestModelUsageRecordedAndPersisted(t *testing.T) {
	root := newFixture(t)
	a, err := New(root, "fake", llm.New("http://localhost:1"), 5)
	if err != nil {
		t.Fatal(err)
	}
	task := "usage checkpoint"
	a.State = &TaskState{Version: stateVersion, Task: task, TaskHash: taskHash(task), Evidence: Evidence{ReadFiles: map[string]string{}}}
	a.recordModelUsage("12345", llm.Generation{PromptEvalCount: 11, EvalCount: 7})
	a.recordModelUsage("ab", llm.Generation{PromptEvalCount: 4, EvalCount: 2})
	if err := saveState(root, a.State); err != nil {
		t.Fatal(err)
	}
	restored, resumed, err := loadState(root, task)
	if err != nil || !resumed {
		t.Fatalf("loadState resumed=%v err=%v", resumed, err)
	}
	u := restored.Usage
	if u.Requests != 2 || u.PromptTokens != 15 || u.CompletionTokens != 9 || u.PromptBytes != 7 || u.PeakPromptTokens != 11 || u.PeakPromptBytes != 5 || u.LastPromptTokens != 4 || u.LastCompletionTokens != 2 {
		t.Fatalf("incorrect persisted usage: %+v", u)
	}
	a.State = restored
	a.recordModelUsage("xyz", llm.Generation{PromptEvalCount: 13, EvalCount: 0})
	if a.State.Usage.Requests != 3 || a.State.Usage.PromptTokens != 28 || a.State.Usage.PeakPromptTokens != 13 {
		t.Fatalf("resume counters failed: %+v", a.State.Usage)
	}
}
