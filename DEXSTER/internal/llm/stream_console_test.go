package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamingModelConsoleReassemblesAndReportsTokens(t *testing.T) {
	written := `{"name":"write_file","path":"main.go","content":"package main\nfunc main() {}","source":""}`
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req generateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !req.Stream {
			t.Error("web model console did not enable streaming")
		}
		if req.Format == nil {
			t.Error("lost JSON constrained format")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		_ = enc.Encode(generateResponse{Response: written[:45]})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_ = enc.Encode(generateResponse{Response: written[45:], Done: true, DoneReason: "stop", PromptEvalCount: 22, EvalCount: 10})
	}))
	defer srv.Close()
	c := New(srv.URL)
	var events []string
	var seen strings.Builder
	c.Trace = func(event, text string) {
		events = append(events, event)
		if event == "chunk" {
			seen.WriteString(text)
		}
	}
	gen, err := c.GenerateWithStats("qwen", "make Go program")
	if err != nil {
		t.Fatal(err)
	}
	if gen.Text != written || seen.String() != written || gen.PromptEvalCount != 22 || gen.EvalCount != 10 {
		t.Fatalf("generation=%#v chunks=%q", gen, seen.String())
	}
	if strings.Join(events, ",") != "start,chunk,chunk,done" || hits.Load() != 1 {
		t.Fatalf("events=%v hits=%d", events, hits.Load())
	}
}

func TestStreamDisconnectRetriesAndReportsUncommittedPartial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if hits.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"response":"partial","done":false}`+"\n")
			return // no done=true; must never be executed
		}
		_ = json.NewEncoder(w).Encode(generateResponse{Response: `{"name":"read_file","path":"a.go","content":"","source":""}`, Done: true})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RetryDelay = time.Nanosecond
	var events []string
	c.Trace = func(event, _ string) { events = append(events, event) }
	gen, err := c.GenerateWithStats("qwen", "test")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 || !strings.Contains(gen.Text, `"read_file"`) {
		t.Fatalf("hits=%d gen=%#v", hits.Load(), gen)
	}
	if strings.Join(events, ",") != "start,chunk,retry,start,chunk,done" {
		t.Fatalf("events=%v", events)
	}
}

func TestTraceNotEnabledRetainsNonStreamingMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req generateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Stream {
			t.Error("normal CLI should stay non-streaming")
		}
		_ = json.NewEncoder(w).Encode(generateResponse{Response: `{"name":"read_file","path":"a.go","content":"","source":""}`, Done: true})
	}))
	defer srv.Close()
	c := New(srv.URL)
	if _, err := c.GenerateWithStats("qwen", "prompt"); err != nil {
		t.Fatal(err)
	}
}
