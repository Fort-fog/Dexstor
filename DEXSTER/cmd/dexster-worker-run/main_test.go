package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackRejectPathTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/tmp/escape"} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 0}); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		if err := unpack(t.TempDir(), &b); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	d := t.TempDir()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	if err := unpack(d, &b); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := os.Lstat(filepath.Join(d, "link")); !os.IsNotExist(err) {
		t.Fatal("link was written")
	}
}
