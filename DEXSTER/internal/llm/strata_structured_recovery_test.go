package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const successfulToolCall = `{"name":"read_file","path":"hello.txt","content":"","source":""}`
const structuredFail = `{"error":{"type":"structured_output_failed","code":"structured_output_failed","message":"model did not return valid JSON: Expecting value: line 1 column 1 (char 0)"}}`

func TestSTRATAStructuredOutputRecovery(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		var request struct {
			Messages       []map[string]string `json:"messages"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string         `json:"name"`
					Strict bool           `json:"strict"`
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Name != "dexster_tool_call" || !request.ResponseFormat.JSONSchema.Strict {
			t.Errorf("original STRATA schema not preserved: %+v", request.ResponseFormat)
		}
		if request.Messages[0]["content"] != "original task" {
			t.Errorf("task changed: %+v", request.Messages)
		}
		if n == 1 {
			if len(request.Messages) != 1 {
				t.Error("first attempt should not contain retry guidance")
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(structuredFail))
			return
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[1]["content"], "JSON") {
			t.Errorf("missing correction for retry: %+v", request.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": successfulToolCall}}},
			"usage":   map[string]int{"prompt_tokens": 87, "completion_tokens": 25},
		})
	}))
	defer server.Close()
	c := New(server.URL + "/v1")
	c.Provider = "strata"
	g, err := c.GenerateWithStats("iq3_s", "original task")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := Parse(g.Text)
	if !ok || call.Name != "read_file" || call.PathArg() != "hello.txt" {
		t.Fatalf("unexpected tool call: %+v valid=%v", call, ok)
	}
	if hits.Load() != 2 || g.PromptEvalCount != 87 || g.EvalCount != 25 {
		t.Fatalf("hits=%d usage=%+v", hits.Load(), g)
	}
}

func TestSTRATAStructuredOutputFailureBounded(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(structuredFail))
	}))
	defer server.Close()
	c := New(server.URL + "/v1")
	c.Provider = "strata"
	_, err := c.GenerateWithStats("iq3_s", "task")
	if err == nil || !strings.Contains(err.Error(), "after 2 retries") || !strings.Contains(err.Error(), "no tool executed") || hits.Load() != 3 {
		t.Fatalf("unexpected response err=%v hits=%d", err, hits.Load())
	}
}

func TestSTRATANonStructuredErrorsNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"other-502", 502, `{"error":{"code":"backend_unavailable"}}`},
		{"invalid-schema-400", 400, structuredFail},
		{"bad-json-502", 502, `not json`},
		{"unavailable-503", 503, `{"error":{"code":"backend_unavailable"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := New(server.URL + "/v1")
			c.Provider = "strata"
			if _, err := c.GenerateWithStats("iq3_s", "task"); err == nil || hits.Load() != 1 {
				t.Fatalf("unexpected retry: err=%v hits=%d", err, hits.Load())
			}
		})
	}
}

func TestSTRATASuccessHasNoExtraRequests(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": successfulToolCall}}}})
	}))
	defer server.Close()
	c := New(server.URL + "/v1")
	c.Provider = "strata"
	if _, err := c.GenerateWithStats("iq3_s", "task"); err != nil || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestSTRATARequestCancellationNotRetried(t *testing.T) {
	var hits atomic.Int32
	client := New("http://localhost:8081/v1")
	client.Provider = "strata"
	client.HTTP.Transport = cancelTransport{calls: &hits}
	if _, err := client.GenerateWithStats("iq3_s", "task"); !errors.Is(err, context.Canceled) || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

type cancelTransport struct{ calls *atomic.Int32 }

func (transport cancelTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, context.Canceled
}
