package tools

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestRouterCannotEditItsOwnSource(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("source unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := New(root); err == nil {
		t.Fatal("self source accepted")
	}
	if _, err := New(filepath.Join(root, "cmd")); err == nil {
		t.Fatal("nested self source accepted")
	}
	if _, err := New(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
