package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSourceGuard(t *testing.T) {
	self := sourceRoot()
	if self == "" {
		t.Skip("source path unavailable")
	}
	if err := validateProject(self, false); err == nil {
		t.Fatal("allowed self source")
	}
	if err := validateProject(filepath.Join(self, "cmd"), false); err == nil {
		t.Fatal("allowed nested self source")
	}
	if err := validateProject(filepath.Dir(self), false); err == nil {
		t.Fatal("allowed parent containing self")
	}
	independent := t.TempDir()
	if err := validateProject(independent, false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(self, link); err == nil {
		if err := validateProject(link, false); err == nil {
			t.Fatal("symlink bypass")
		}
	}
}
