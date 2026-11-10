package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStructuredQwenRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		var think bool
		if err := json.Unmarshal(req["think"], &think); err != nil || think {
			t.Errorf("think must explicitly be false: %s", req["think"])
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(req["format"], &schema); err != nil || schema.Type != "object" || len(schema.Properties) != 6 || len(schema.Required) != 4 {
			t.Errorf("bad schema: %s", req["format"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": `{"name":"read_file","path":"main.go","content":"","source":""}`, "done": true, "done_reason": "stop"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	result, err := c.GenerateWithStats("qwen", "choose action")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := Parse(result.Text)
	if !ok || call.Name != "read_file" || call.PathArg() != "main.go" {
		t.Fatalf("bad action: %+v ok=%v", call, ok)
	}
}
