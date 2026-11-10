package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dexster/internal/llm"
	"kraken/memory"
)

// A provider retry is not a tool retry: no write may happen until a complete,
// parsed tool response is available. KRAKEN journals a single successful
// file operation, even when Ollama temporarily returns 503.
func TestTransientProviderRetryDoesNotDuplicateWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "example.txt"), []byte("OLD"), 0644); err != nil {
		t.Fatal(err)
	}
	krakenRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(krakenRoot, "events"), 0755); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	responses := []string{
		`{"name":"read_file","path":"example.txt","content":"","source":""}`,
		`{"name":"write_file","path":"example.txt","content":"NEW","source":""}`,
		`{"name":"declare_done","path":"","content":"","source":""}`,
	}
	var completed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "temporary", 503)
			return
		}
		i := int(completed.Add(1)) - 1
		if i >= len(responses) {
			t.Errorf("unexpected extra model request %d", i)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": responses[i], "done": true, "done_reason": "stop", "prompt_eval_count": 100})
	}))
	defer srv.Close()
	client := llm.New(srv.URL)
	client.RetryDelay = time.Nanosecond
	a, err := New(root, "qwen", client, 12)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory = store
	a.AutoVerify = true
	if err := a.Run("replace example.txt with NEW"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "example.txt"))
	if err != nil || string(data) != "NEW" {
		t.Fatalf("file=%q err=%v", data, err)
	}
	if !a.State.Done {
		t.Fatal("DONE not acknowledged")
	}
	writes := 0
	for _, record := range a.State.Journal {
		if record.Tool == "write_file" && record.OK {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("unexpected duplicate writes: %d", writes)
	}
	if hits.Load() != 4 {
		t.Fatalf("expected 503 + 3 LLM replies, got %d", hits.Load())
	}
}

// Mimics the 13131/4096 context failure observed in Crush, without a model
// response being interpreted as a tool. Compaction is recorded before retry.
func TestProviderContextRejectionCompactsOnce(t *testing.T) {
	root := t.TempDir()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if hits.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"request (13131 tokens) exceeds the available context size (4096 tokens)","type":"exceed_context_size_error"}}`, 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": `{"name":"list_files","path":".","content":"","source":""}`, "done": true, "done_reason": "stop"})
	}))
	defer srv.Close()
	client := llm.New(srv.URL)
	client.NumCtx = 16384
	a, err := New(root, "qwen", client, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.MaxPromptTokens = 9000
	err = a.Run("inspect directory")
	if err == nil || !strings.Contains(err.Error(), "without completion evidence") {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits.Load() != 2 || !a.State.ContextCompact || a.State.ContextEpoch == 0 {
		t.Fatalf("hits=%d compact=%v epoch=%d", hits.Load(), a.State.ContextCompact, a.State.ContextEpoch)
	}
	if len(a.State.Journal) != 1 || a.State.Journal[0].Tool != "list_files" {
		t.Fatalf("journal=%+v", a.State.Journal)
	}
}

// Context overflow is rejected before the model call when even the compact
// representation is clearly too large for Ollama's configured num_ctx.
func TestProviderPreflightRejectsOversizeWithoutAPICall(t *testing.T) {
	root := t.TempDir()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Error("oversized request unexpectedly reached Ollama")
	}))
	defer srv.Close()
	client := llm.New(srv.URL)
	client.NumCtx = 4096
	a, err := New(root, "qwen", client, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.MaxPromptTokens = 9000
	err = a.Run("large instructions " + strings.Repeat("requirements_", 7000))
	if err == nil || !strings.Contains(err.Error(), "compact prompt exceeds budget") {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("unexpected provider calls: %d", hits.Load())
	}
	if len(a.State.Journal) != 0 {
		t.Fatalf("unexpected actions: %+v", a.State.Journal)
	}
}
