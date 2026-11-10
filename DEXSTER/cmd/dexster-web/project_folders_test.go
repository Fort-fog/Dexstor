package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectFolderCreateAndList(t *testing.T) {
	home, _ := browserHome()
	base, err := os.MkdirTemp(home, "dexster-folder-picker-test-")
	if err != nil {
		t.Skipf("cannot make temp directory in home: %v", err)
	}
	defer os.RemoveAll(base)
	p, err := createProjectFolder(base, "new-project")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(base, "new-project") {
		t.Fatalf("unexpected path %q", p)
	}
	listing, err := listProjectFolders(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Folders) != 1 || listing.Folders[0].Name != "new-project" {
		t.Fatalf("unexpected listing: %+v", listing)
	}
	if _, err := createProjectFolder(base, "../escape"); err == nil {
		t.Fatal("traversal allowed")
	}
	if _, err := createProjectFolder(base, "new-project"); err == nil {
		t.Fatal("existing folder overwritten")
	}
	if _, _, err := browseFolderPath("/tmp"); err == nil && !stringsHasPrefixPath(home, "/tmp") {
		t.Fatal("outside home allowed")
	}
}
func stringsHasPrefixPath(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && rel != "." && len(rel) >= 0 && (len(rel) < 3 || rel[:3] != "../")
}
