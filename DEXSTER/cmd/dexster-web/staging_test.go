package main

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStageProjectRejectSymlinkAndIgnoreGit(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(d, ".git"), 0700)
	os.WriteFile(filepath.Join(d, ".git", "config"), []byte("private"), 0600)
	payload, err := stageProject(d)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(payload))
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "main.go" {
		t.Fatalf("unexpected staged file %q", h.Name)
	}
	if _, err := tr.Next(); err == nil {
		t.Fatal("unexpected extra file")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = stageProject(d); err == nil {
		t.Fatal("symlink unexpectedly accepted")
	}
}
func TestBridgeOnlyGenerate(t *testing.T) {
	b, err := newModelBridge()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
	h := b.server.Handler
	for _, tc := range []struct{ method, path string }{{"GET", "/api/generate"}, {"POST", "/api/pull"}, {"POST", "http://169.254.169.254/"}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s returned %d", tc.method, tc.path, rec.Code)
		}
	}
}
func TestWorkerFailClosedWithoutBridge(t *testing.T) {
	c := newController("/bin/true")
	cfg := config{Runtime: "worker", Provider: "ollama", Model: "fake", Task: "test", Root: t.TempDir()}
	if err := c.start(cfg); err == nil {
		t.Fatal("worker started without bridge")
	}
	cfg.Runtime = "vm"
	if err := c.start(cfg); err == nil {
		t.Fatal("retired vm runtime accepted")
	}
}
