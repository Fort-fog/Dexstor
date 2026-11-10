package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureNewGoModuleScope adds only the required module manifest for an explicit
// new Go project. It does not grant wildcard, directory or unrelated access.
func ensureNewGoModuleScope(req analystPlanInput, p *analystPlan) error {
	task := strings.ToLower(req.Task)
	goTask := strings.Contains(task, "golang") || strings.Contains(task, "на go") ||
		strings.Contains(task, "in go") || strings.Contains(task, "go project") ||
		strings.Contains(task, "go module") || strings.Contains(task, "go-модул") ||
		strings.Contains(task, "go-проект") || strings.Contains(task, "go server")
	newTask := strings.Contains(task, "с нуля") || strings.Contains(task, "создай") ||
		strings.Contains(task, "create from scratch") || strings.Contains(task, "create a new") ||
		strings.Contains(task, "build a new")
	if !goTask || !newTask {
		return nil
	}
	hasGo := false
	for _, f := range p.Contract.WriteScope {
		if f == "go.mod" {
			return nil
		}
		if strings.HasSuffix(f, ".go") {
			hasGo = true
		}
	}
	if !hasGo {
		return nil
	}
	if _, err := os.Stat(filepath.Join(req.Root, "go.mod")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check existing go.mod: %w", err)
	}
	if len(p.Contract.WriteScope) >= 16 {
		return fmt.Errorf("new Go module requires go.mod but write_scope is full")
	}
	p.Contract.WriteScope = append(p.Contract.WriteScope, "go.mod")
	return nil
}
