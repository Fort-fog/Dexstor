package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The llama.cpp backend must constrain its answer to one DEXSTER action,
// rather than relying on a plain chat prompt. No control path changes.
func TestLlamaCPPUsesExistingToolActionSchema(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			ResponseFormat struct {
				Type   string `json:"type"`
				Schema struct {
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"schema"`
			} `json:"response_format"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.ResponseFormat.Type != "json_schema" || req.ResponseFormat.Schema.Type != "object" || len(req.ResponseFormat.Schema.Properties) != 6 || len(req.ResponseFormat.Schema.Required) != 4 {
			t.Fatalf("llama.cpp request did not carry existing action schema: %+v", req.ResponseFormat)
		}
		if len(req.Tools) != 0 {
			t.Fatalf("unexpected native tools field; controller owns actions")
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"name\":\"list_files\",\"path\":\".\",\"content\":\"\",\"source\":\"\"}"}}],"usage":{"prompt_tokens":15,"completion_tokens":14}}`))
	}))
	defer server.Close()
	client := New(server.URL + "/v1")
	client.Provider = "llamacpp"
	result, err := client.GenerateWithStats("unsloth/GLM-4.7-Flash-GGUF:Q6_K", "inspect project")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := Parse(result.Text)
	if !ok || call.Name != "list_files" || call.PathArg() != "." {
		t.Fatalf("invalid controller call: %+v ok=%v", call, ok)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}

func TestUnslothSchemaNotForced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if _, ok := req["response_format"]; ok {
			t.Fatal("llama.cpp schema leaked into Unsloth API")
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"name\":\"list_files\",\"path\":\".\"}"}}]}`))
	}))
	defer server.Close()
	client := New(server.URL + "/v1")
	client.Provider = "unsloth"
	if _, err := client.GenerateWithStats("some-model", "inspect"); err != nil {
		t.Fatal(err)
	}
}
