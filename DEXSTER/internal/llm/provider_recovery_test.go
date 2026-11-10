package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransientOllama503RetriesSamePromptWithoutToolExecution(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("bad URL %q", r.URL.Path)
			return
		}
		var req generateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Prompt != "verify snapshot" {
			t.Errorf("prompt changed: %q", req.Prompt)
		}
		if hits.Add(1) == 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(generateResponse{Response: `{"name":"read_file","path":"main.go","content":"","source":""}`, Done: true, DoneReason: "stop"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RetryDelay = time.Nanosecond
	generation, err := c.GenerateWithStats("qwen", "verify snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(generation.Text, `"read_file"`) || hits.Load() != 2 {
		t.Fatalf("generation=%#v attempts=%d", generation, hits.Load())
	}
}

func TestTruncatedOllamaJSONTransportRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":"incomple`)
			return
		}
		_ = json.NewEncoder(w).Encode(generateResponse{Response: `{"name":"read_file","path":"x","content":"","source":""}`, Done: true, DoneReason: "stop"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RetryDelay = time.Nanosecond
	_, err := c.GenerateWithStats("qwen", "prompt")
	if err != nil || hits.Load() != 2 {
		t.Fatalf("err=%v attempts=%d", err, hits.Load())
	}
}

func TestOllamaContext400NeverBlindRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"code":400,"message":"request (13131 tokens) exceeds the available context size (4096 tokens), try increasing it","type":"exceed_context_size_error"}}`, 400)
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RetryDelay = time.Nanosecond
	_, err := c.GenerateWithStats("qwen", "prompt")
	var contextErr *ContextLimitError
	if !errors.As(err, &contextErr) {
		t.Fatalf("expected typed context error, got %v", err)
	}
	if contextErr.Requested != 13131 || contextErr.Capacity != 4096 || hits.Load() != 1 {
		t.Fatalf("err=%#v attempts=%d", contextErr, hits.Load())
	}
}

func TestPermanent400AndIncompleteOutputNeverRetried(t *testing.T) {
	for _, scenario := range []string{"bad-request", "incomplete-generation"} {
		t.Run(scenario, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if scenario == "bad-request" {
					http.Error(w, "invalid format", 400)
					return
				}
				_ = json.NewEncoder(w).Encode(generateResponse{Done: false, Response: `{"name":"write_file","path":"main.go","content":"bad","source":""}`})
			}))
			defer srv.Close()
			c := New(srv.URL)
			c.RetryDelay = time.Nanosecond
			if _, err := c.GenerateWithStats("qwen", "prompt"); err == nil || hits.Load() != 1 {
				t.Fatalf("err=%v attempts=%d", err, hits.Load())
			}
		})
	}
}

func TestOllamaRetryBounded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "temporary", 503)
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RetryDelay = time.Nanosecond
	_, err := c.GenerateWithStats("qwen", "prompt")
	if err == nil || hits.Load() != 3 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}
