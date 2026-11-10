// Package constraint enforces explicit, machine-checkable source restrictions.
// It does not attempt to interpret arbitrary natural-language requirements.
package constraint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Rule protects either a whole file or a named top-level Go function/method.
// A function rule must include a file path; a method may be specified as Type.Method.
type Rule struct {
	File     string `json:"file"`
	Function string `json:"function,omitempty"`
	SHA256   string `json:"sha256"`
}

type Gate struct {
	Root  string
	Rules []Rule
}

// Capture validates the rules and records a baseline for the current task.
// Persist the returned Rules in the task state before the first agent write.
func Capture(root string, specifications []string) (*Gate, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	g := &Gate{Root: abs}
	for _, spec := range specifications {
		file, fn, ok := strings.Cut(spec, ":")
		if !ok {
			file = spec
			fn = ""
		}
		if file == "" {
			return nil, errors.New("empty protected file")
		}
		raw, err := g.readFile(file)
		if err != nil {
			return nil, err
		}
		hash, err := fingerprint(raw, fn)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec, err)
		}
		g.Rules = append(g.Rules, Rule{File: filepath.Clean(file), Function: fn, SHA256: hash})
	}
	return g, nil
}

// Restore reconstructs a gate from persisted task-state rules (not new baselines).
func Restore(root string, rules []Rule) (*Gate, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	g := &Gate{Root: abs, Rules: append([]Rule(nil), rules...)}
	for _, rule := range rules {
		if rule.SHA256 == "" {
			return nil, errors.New("missing baseline hash")
		}
		if _, err := g.safePath(rule.File); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func (g *Gate) safePath(p string) (string, error) {
	if p == "" || filepath.IsAbs(p) || filepath.Clean(p) == "." {
		return "", errors.New("invalid relative path")
	}
	rel := filepath.Clean(p)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes root")
	}
	full := filepath.Join(g.Root, rel)
	cur := g.Root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("protected path missing: %s", p)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink denied: %s", p)
		}
	}
	return full, nil
}
func (g *Gate) readFile(p string) ([]byte, error) {
	path, err := g.safePath(p)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// CheckWrite rejects an agent write that changes any protected entity.
// This must be invoked before checkpoint creation and before the router write.
func (g *Gate) CheckWrite(path string, newContent []byte) error {
	norm := filepath.Clean(path)
	for _, rule := range g.Rules {
		if rule.File != norm {
			continue
		}
		hash, err := fingerprint(newContent, rule.Function)
		if err != nil {
			return fmt.Errorf("constraint %s:%s: %w", rule.File, rule.Function, err)
		}
		if hash != rule.SHA256 {
			return fmt.Errorf("constraint violation: protected %s:%s changed", rule.File, rule.Function)
		}
	}
	return nil
}

// Verify checks the actual files before completion; it detects external edits too.
func (g *Gate) Verify() error {
	for _, rule := range g.Rules {
		raw, err := g.readFile(rule.File)
		if err != nil {
			return fmt.Errorf("constraint %s: %w", rule.File, err)
		}
		hash, err := fingerprint(raw, rule.Function)
		if err != nil {
			return err
		}
		if hash != rule.SHA256 {
			return fmt.Errorf("constraint violation: protected %s:%s changed on disk", rule.File, rule.Function)
		}
	}
	return nil
}
func fingerprint(raw []byte, fn string) (string, error) {
	if fn == "" {
		s := sha256.Sum256(raw)
		return hex.EncodeToString(s[:]), nil
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "protected.go", raw, parser.AllErrors)
	if err != nil {
		return "", fmt.Errorf("invalid Go source: %w", err)
	}
	var candidate *ast.FuncDecl
	for _, d := range parsed.Decls {
		decl, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := decl.Name.Name
		if decl.Recv != nil && len(decl.Recv.List) > 0 {
			typ := decl.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		if name == fn {
			if candidate != nil {
				return "", fmt.Errorf("ambiguous function %q", fn)
			}
			candidate = decl
		}
	}
	if candidate == nil {
		return "", fmt.Errorf("protected function %q not found", fn)
	}
	// Formatting canonicalizes whitespace without discarding semantic differences.
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, candidate); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
