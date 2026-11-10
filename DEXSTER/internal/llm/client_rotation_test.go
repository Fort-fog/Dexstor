package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaMeasuredTokensAndUnload(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "bad path", 404)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		calls++
		if calls == 1 {
			if body["prompt"] != "hello" {
				t.Errorf("prompt: %v", body["prompt"])
			}
			opts, ok := body["options"].(map[string]any)
			if !ok || opts["num_ctx"] != float64(16384) {
				t.Errorf("num_ctx: %v", body["options"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": "TOOL_CALL", "prompt_eval_count": 14201, "eval_count": 20, "done_reason": "stop", "done": true})
		} else {
			if body["prompt"] != "" || body["keep_alive"] != float64(0) {
				t.Errorf("unload request: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": "", "done": true})
		}
	}))
	defer server.Close()
	c := New(server.URL)
	c.NumCtx = 16384
	result, err := c.GenerateWithStats("model", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.PromptEvalCount != 14201 || result.EvalCount != 20 {
		t.Fatalf("wrong tokens: %#v", result)
	}
	if err := c.Unload("model"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestOllamaIncompleteResponseFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "TOOL_CALL\nname: write_file\npath: main.go\ncontent: BAD\nEND_TOOL_CALL", "done": false, "done_reason": ""})
	}))
	defer server.Close()
	c := New(server.URL)
	_, err := c.GenerateWithStats("model", "task")
	if err == nil {
		t.Fatal("accepted incomplete output")
	}
}

func TestOllamaHTTPErrorDoesNotBecomeToolResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := New(server.URL)
	_, err := c.GenerateWithStats("model", "task")
	if err == nil {
		t.Fatal("HTTP 503 accepted")
	}
}
