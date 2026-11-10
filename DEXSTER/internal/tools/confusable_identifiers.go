package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// checkChangedCodeIdentifiers checks only changed programming-language source
// files. It never examines documents, CSV, XLSX, text literals or comments.
// It reports suspicious identifiers; it never alters source bytes.
func (r *Router) checkChangedCodeIdentifiers() error {
	files, err := r.changedSourceFiles()
	if err != nil {
		return err
	}
	return r.checkCodeIdentifiersForPaths(files)
}

// checkCodeIdentifiersForPaths validates agent-owned edits only. Other dirty
// files may belong to the operator and must not block a scoped task.
func (r *Router) checkCodeIdentifiersForPaths(files []string) error {
	var issues []string
	for _, rel := range files {
		full, err := r.safePath(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".go":
			issues = append(issues, scanGoIdentifiers(rel, data)...)
		case ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx":
			issues = append(issues, scanCIdentifiers(rel, data)...)
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Strings(issues)
	return fmt.Errorf("mixed Latin/Cyrillic identifier(s): %s; inspect identifiers and make an exact CAS edit; strings and comments were not checked", strings.Join(issues, "; "))
}

func (r *Router) changedSourceFiles() ([]string, error) {
	selected := map[string]bool{}
	if _, err := os.Stat(filepath.Join(r.Root, ".git")); err == nil && gitHasHead(r.Root) {
		for _, args := range [][]string{
			{"-C", r.Root, "diff", "--name-only", "-z", "HEAD", "--"},
			{"-C", r.Root, "ls-files", "--others", "--exclude-standard", "-z", "--"},
		} {
			out, err := exec.Command("git", args...).Output()
			if err != nil {
				return nil, fmt.Errorf("list changed source files: %w", err)
			}
			for _, raw := range bytes.Split(out, []byte{0}) {
				if len(raw) > 0 {
					selected[string(raw)] = true
				}
			}
		}
	} else {
		files, err := r.ListFiles()
		if err != nil {
			return nil, err
		}
		for _, rel := range files {
			ext := strings.ToLower(filepath.Ext(rel))
			if !sourceExtension(ext) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(r.Root, rel))
			if err != nil {
				return nil, err
			}
			if shaString(data) != r.baseSnapshot[rel] {
				selected[rel] = true
			}
		}
	}
	result := make([]string, 0, len(selected))
	for rel := range selected {
		if sourceExtension(strings.ToLower(filepath.Ext(rel))) {
			result = append(result, rel)
		}
	}
	sort.Strings(result)
	return result, nil
}

// An unborn repository has a .git directory but no HEAD commit. Use the
// captured run baseline instead of asking git diff HEAD (exit status 128).
func gitHasHead(root string) bool {
	return exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Run() == nil
}

func sourceExtension(ext string) bool {
	switch ext {
	case ".go", ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx":
		return true
	}
	return false
}

func shaString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func suspiciousIdentifier(s string) bool {
	latin, cyr := false, false
	for _, ch := range s {
		if unicode.In(ch, unicode.Latin) {
			latin = true
		}
		if unicode.In(ch, unicode.Cyrillic) {
			cyr = true
		}
	}
	return latin && cyr
}

func issueAt(path string, line int, ident string) string {
	var chars []string
	for _, ch := range ident {
		if unicode.In(ch, unicode.Cyrillic) {
			chars = append(chars, fmt.Sprintf("%q U+%04X", ch, ch))
		}
	}
	return fmt.Sprintf("%s:%d: %q includes Cyrillic %s", path, line, ident, strings.Join(chars, ","))
}

func scanGoIdentifiers(path string, data []byte) []string {
	fs := token.NewFileSet()
	f := fs.AddFile(path, fs.Base(), len(data))
	var s scanner.Scanner
	s.Init(f, data, nil, 0)
	var problems []string
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.IDENT && suspiciousIdentifier(lit) {
			problems = append(problems, issueAt(path, fs.Position(pos).Line, lit))
		}
	}
	return problems
}

// scanCIdentifiers is a bounded lexical scan. Comments, quoted literals and
// C++ raw strings are skipped; only identifier tokens are inspected.
func scanCIdentifiers(path string, data []byte) []string {
	src := string(data)
	line := 1
	var problems []string
	for i := 0; i < len(src); {
		if src[i] == '\n' {
			line++
			i++
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '/' {
			i += 2
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i < len(src) {
				if src[i] == '\n' {
					line++
				}
				if i+1 < len(src) && src[i] == '*' && src[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		// R"tag(contents)tag" with optional encoding prefixes is recognized
		// when scanning reaches the R itself.
		if i+1 < len(src) && src[i] == 'R' && src[i+1] == '"' {
			j := i + 2
			for j < len(src) && j-i <= 18 && src[j] != '(' && src[j] != '\n' {
				j++
			}
			if j < len(src) && src[j] == '(' {
				closing := ")" + src[i+2:j] + "\""
				end := strings.Index(src[j+1:], closing)
				if end >= 0 {
					stop := j + 1 + end + len(closing)
					line += strings.Count(src[i:stop], "\n")
					i = stop
					continue
				}
			}
		}
		if src[i] == '"' || src[i] == '\'' {
			quote := src[i]
			i++
			for i < len(src) {
				if src[i] == '\n' {
					line++
				}
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				if src[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		ch, width := utf8.DecodeRuneInString(src[i:])
		if ch == '_' || unicode.IsLetter(ch) {
			begin, beginLine := i, line
			i += width
			for i < len(src) {
				ch, width = utf8.DecodeRuneInString(src[i:])
				if ch != '_' && !unicode.IsLetter(ch) && !unicode.IsDigit(ch) {
					break
				}
				i += width
			}
			ident := src[begin:i]
			if suspiciousIdentifier(ident) {
				problems = append(problems, issueAt(path, beginLine, ident))
			}
			continue
		}
		i += width
	}
	return problems
}
