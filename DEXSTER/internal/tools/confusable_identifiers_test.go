package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMixedScriptCodeIdentifiersOnly(t *testing.T) {
	cases := []struct {
		name, file, content string
		wantFail            bool
	}{
		{"Go mixed", "main.go", "package main\nvar cоunt = 1\n", true},
		{"Go Russian string/comment", "main.go", "package main\n// cоunt\nvar message = \"cоunt Морковь\"\n", false},
		{"Go Cyrillic-only identifier", "main.go", "package main\nvar количество = 1\n", false},
		{"C++ mixed", "main.cpp", "int main(){int cоunt=1; return cоunt;}\n", true},
		{"C++ strings and comments", "main.cpp", "// cоunt\nconst char* s=\"cоunt\"; /* cоunt */\n", false},
		{"C++ raw string", "main.cpp", "const char* s=R\"tag(cоunt)tag\";\n", false},
		{"CSV ignored", "sales_data.csv", "name,value\ncоunt,1\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			r, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate an agent edit made after taking the baseline snapshot.
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.content+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			result := r.GitDiff()
			if result.OK == tc.wantFail {
				t.Fatalf("GitDiff OK=%v error=%q", result.OK, result.Error)
			}
			if tc.wantFail && (!strings.Contains(result.Error, tc.file) || !strings.Contains(result.Error, "U+043E")) {
				t.Fatalf("missing source-position/unicode evidence: %s", result.Error)
			}
		})
	}
}

func TestMixedScriptGitChangedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.go"), []byte("package main\nvar cоunt = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\nvar count = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := r.GitDiff(); !res.OK {
		t.Fatalf("unmodified legacy file should not block new edits: %s", res.Error)
	}
}
