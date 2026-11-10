package memory

import "testing"

func TestRepairPromptNeverTriggersProjectRollback(t *testing.T) {
	prompts := []string{
		"Продолжи ремонт существующего проекта. Восстанови task.go. Используй сохраненное состояние и результаты тестов.",
		"Восстанови task.go из Git и продолжи исправление ошибок сборки",
		"Верни файл main.go к рабочему состоянию и прогони go test",
		"Restore task.go and use saved state to continue repair",
	}
	for _, p := range prompts {
		if _, ok := ParseRollbackIntent(p); ok {
			t.Errorf("repair interpreted as rollback: %q", p)
		}
	}
}

func TestExplicitProjectRollbackStillRecognized(t *testing.T) {
	prompts := []struct {
		text string
		want ProjectRollbackMode
	}{
		{"откатись до рабочей версии", RollbackPrevious},
		{"верни первую рабочую версию", RollbackFirst},
		{"восстанови предыдущую версию", RollbackPrevious},
		{"rollback to first working version", RollbackFirst},
	}
	for _, p := range prompts {
		r, ok := ParseRollbackIntent(p.text)
		if !ok || r.Mode != p.want {
			t.Errorf("explicit rollback lost: %q -> %v %v", p.text, r.Mode, ok)
		}
	}
}
