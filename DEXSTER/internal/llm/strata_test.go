package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSTRATAChatCompletionAndUnload(t *testing.T) {
	calls, unloads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer strata-secret" {
			t.Errorf("missing API key")
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			calls++
			var q struct {
				Model           string              `json:"model"`
				Messages        []map[string]string `json:"messages"`
				ReasoningEffort string              `json:"reasoning_effort"`
				ResponseFormat  struct {
					Type       string `json:"type"`
					JSONSchema struct {
						Name   string         `json:"name"`
						Strict bool           `json:"strict"`
						Schema map[string]any `json:"schema"`
					} `json:"json_schema"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
			}
			if q.Model != "strata" || len(q.Messages) != 1 || q.Messages[0]["content"] != "hello" || q.ReasoningEffort != "low" {
				t.Errorf("bad query %+v", q)
			}
			if q.ResponseFormat.Type != "json_schema" || !q.ResponseFormat.JSONSchema.Strict || q.ResponseFormat.JSONSchema.Name != "dexster_tool_call" {
				t.Errorf("bad schema %+v", q.ResponseFormat)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"name\":\"declare_done\",\"path\":\"\",\"content\":\"\",\"source\":\"\"}"}}],"usage":{"prompt_tokens":43,"completion_tokens":14}}`))
		case "/v1/unload":
			unloads++
			var v map[string]string
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil || v["model"] != "strata" {
				t.Errorf("bad unload %+v %v", v, err)
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.URL + "/v1")
	client.Provider = "strata"
	client.APIKey = "strata-secret"
	t.Setenv("DEXSTER_STRATA_REASONING_EFFORT", "low")
	result, err := client.GenerateWithStats("strata", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.PromptEvalCount != 43 || result.EvalCount != 14 || !strings.Contains(result.Text, "declare_done") {
		t.Fatalf("result %+v", result)
	}
	if err := client.Unload("strata"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || unloads != 1 {
		t.Fatalf("calls=%d unloads=%d", calls, unloads)
	}
}

func TestSTRATARejectsTruncatedToolCall(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"{\"name\":\"write_file\"}"}}]}`))
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "strata"
	if _, err := c.GenerateWithStats("strata", "hello"); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation, got %v", err)
	}
}

func TestSTRATAUnloadingFailsClosed(t *testing.T) {
	c := New("http://example.com/v1")
	c.Provider = "strata"
	if err := c.Unload("strata"); err == nil {
		t.Fatal("accepted remote endpoint")
	}
}
