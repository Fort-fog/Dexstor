package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScopeNewGoModule(t *testing.T) {
	root := t.TempDir()
	req := analystPlanInput{Root: root, Task: "Создай с нуля HTTP Task Manager на Go"}
	p := analystPlan{Contract: taskContract{WriteScope: []string{"main.go", "server.go", "task_manager.go", "test/task_manager_test.go"}}}
	if err := ensureNewGoModuleScope(req, &p); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Contract.WriteScope, "go.mod") || len(p.Contract.WriteScope) != 5 {
		t.Fatalf("scope = %v", p.Contract.WriteScope)
	}
	if err := ensureNewGoModuleScope(req, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Contract.WriteScope) != 5 {
		t.Fatal("duplicate go.mod")
	}
}
func TestScopeNoExpansionForOtherProjects(t *testing.T) {
	root := t.TempDir()
	for _, task := range []string{"Исправь существующий Go сервер", "Создай с нуля сервер на Rust"} {
		p := analystPlan{Contract: taskContract{WriteScope: []string{"main.go"}}}
		if err := ensureNewGoModuleScope(analystPlanInput{Root: root, Task: task}, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Contract.WriteScope) != 1 {
			t.Fatalf("unexpected expansion: %q %v", task, p.Contract.WriteScope)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := analystPlan{Contract: taskContract{WriteScope: []string{"main.go"}}}
	if err := ensureNewGoModuleScope(analystPlanInput{Root: root, Task: "Создай с нуля HTTP Task Manager на Go"}, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Contract.WriteScope) != 1 {
		t.Fatal("expanded existing module")
	}
}
func TestScopeFullFailsClosed(t *testing.T) {
	p := analystPlan{Contract: taskContract{WriteScope: []string{"main.go"}}}
	for i := 0; i < 15; i++ {
		p.Contract.WriteScope = append(p.Contract.WriteScope, "file"+string(rune('a'+i))+".txt")
	}
	if err := ensureNewGoModuleScope(analystPlanInput{Root: t.TempDir(), Task: "Создай с нуля проект на Go"}, &p); err == nil {
		t.Fatal("scope overrun accepted")
	}
}
