package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, text := range files {
		h := &tar.Header{Name: name, Mode: 0600, Size: int64(len(text)), Typeflag: tar.TypeReg}
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write([]byte(text)); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	return buf.Bytes()
}
func TestReviewSnapshotDoesNotOverwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := makeTestTar(t, map[string]string{"calc.go": "hello"})
	if e := saveInitialSnapshot("/tmp/project", "test", raw); e != nil {
		t.Fatal(e)
	}
	if e := saveInitialSnapshot("/tmp/project", "test", []byte("changed")); e != nil {
		t.Fatal(e)
	}
	p, _ := reviewSnapshotPath("/tmp/project", "test")
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, raw) {
		t.Fatal("snapshot overwritten")
	}
}
func TestReviewApplyRejectsChangedOriginal(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "calc.go")
	os.WriteFile(p, []byte("locally changed"), 0600)
	e := applyReview(root, map[string][]byte{"calc.go": []byte("base")}, map[string][]byte{"calc.go": []byte("worker")})
	if e == nil || !strings.Contains(e.Error(), "исходник изменился") {
		t.Fatalf("expected conflict, got %v", e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "locally changed" {
		t.Fatal("original modified on conflict")
	}
}
func TestReviewApplyExistingFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "calc.go")
	os.WriteFile(p, []byte("base"), 0600)
	if e := applyReview(root, map[string][]byte{"calc.go": []byte("base")}, map[string][]byte{"calc.go": []byte("worker")}); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "worker" {
		t.Fatal("change missing")
	}
}
func TestReviewApplyRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(other, []byte("secret"), 0600)
	os.Symlink(other, filepath.Join(root, "calc.go"))
	if e := applyReview(root, map[string][]byte{"calc.go": []byte("base")}, map[string][]byte{"calc.go": []byte("worker")}); e == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(other)
	if string(b) != "secret" {
		t.Fatal("secret overwritten")
	}
}
func TestReviewRejectsArchiveTraversal(t *testing.T) {
	_, e := readArchive(makeTestTar(t, map[string]string{"../evil": "payload"}))
	if e == nil {
		t.Fatal("traversal accepted")
	}
}
