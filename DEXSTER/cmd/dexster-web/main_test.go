package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalWebAndProcessLifecycle(t *testing.T) {
	d := t.TempDir()
	binary := filepath.Join(d, "fake-dexster")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho '[DEXSTER] #1 read_file main.go ok=true'\nsleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := newController(binary)
	h := c.routes()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8767/api/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("state %d", w.Code)
	}
	req = httptest.NewRequest("GET", "http://attacker.invalid:8767/api/state", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("host security %d", w.Code)
	}
	cfg := config{Runtime: "host", Root: d, Model: "fake", Task: "test", Context: 16384, Budget: 9000, MaxIterations: 20}
	if err := c.start(cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.start(cfg); err == nil {
		t.Fatal("concurrent run allowed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(c.snapshot().Log, "read_file") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(c.snapshot().Log, "read_file") {
		t.Fatal("missing log")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	for c.snapshot().Status == "stopping" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.snapshot().Status != "failed" {
		t.Fatalf("unexpected state: %s", c.snapshot().Status)
	}
}
func TestStartValidation(t *testing.T) {
	c := newController("/bin/true")
	cfg := config{Root: t.TempDir(), Model: "x", Task: "", Context: 16384, Budget: 9000, MaxIterations: 20}
	if err := c.start(cfg); err == nil {
		t.Fatal("empty task accepted")
	}
}

func TestModelListFromOllamaTags(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"gemma3:4b"},{"name":"qwen3:8b"}]}`))
	}))
	defer fake.Close()
	got, err := listOllamaModels(fake.URL + "/api/tags")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "qwen3:8b" || got[1] != "gemma3:4b" {
		t.Fatalf("models=%v", got)
	}
}

func TestModelsEndpointFallback(t *testing.T) {
	c := newController("/bin/true")
	w := httptest.NewRecorder()
	c.routes().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/api/models", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"models"`) {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestValidateProjectPreflight(t *testing.T) {
	dir := t.TempDir()
	if err := validateProject("", false); err == nil {
		t.Fatal("empty project accepted")
	}
	if err := validateProject(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := validateProject(dir, true); err != nil {
		t.Fatalf("non-Git project should be accepted; CLI initializes baseline: %v", err)
	}
	engine := t.TempDir()
	if err := os.WriteFile(filepath.Join(engine, "go.mod"), []byte("module dexster\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(engine, "cmd", "dexster-web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine, "cmd", "dexster-web", "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateProject(engine, false); err == nil {
		t.Fatal("self source edit allowed")
	}
}
func TestKRAKENReadOnlySummary(t *testing.T) {
	d := t.TempDir()
	for _, folder := range []string{"events", "checkpoints"} {
		if err := os.MkdirAll(filepath.Join(d, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f := filepath.Join(d, "events", "task.json")
	if err := os.WriteFile(f, []byte("sensitive event"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newController("/bin/true")
	c.s.Config.Kraken = d
	w := httptest.NewRecorder()
	c.routes().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/api/kraken", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"events":1`) || strings.Contains(w.Body.String(), "sensitive event") {
		t.Fatalf("metadata endpoint: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(f)
	if err != nil || string(data) != "sensitive event" {
		t.Fatal("KRAKEN mutated")
	}
}

func TestRemovedRuntimesFailClosed(t *testing.T) {
	c := newController("/bin/true")
	for _, runtime := range []string{"kvm", "", "worker", "unexpected"} {
		cfg := config{Runtime: runtime, Root: t.TempDir(), Model: "fake", Task: "test", Context: 16384, Budget: 9000, MaxIterations: 5}
		if err := c.start(cfg); err == nil {
			t.Fatalf("runtime %q must be rejected", runtime)
		}
	}
}

func TestOldVMEndpointsGone(t *testing.T) {
	c := newController("/bin/true")
	for _, path := range []string{"/api/vm/status", "/api/vm/start", "/api/vm/probe", "/api/vm/shutdown"} {
		w := httptest.NewRecorder()
		c.routes().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8767"+path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d", path, w.Code)
		}
	}
}

func TestWorkerProbeSecurity(t *testing.T) {
	c := newController("/bin/true")
	h := c.routes()
	for _, tc := range []struct {
		method, token string
		want          int
	}{
		{"GET", "", 405},
		{"POST", "bad", 403},
	} {
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:8767/api/worker/probe", nil)
		req.Header.Set("X-Dexster-Token", tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
}
