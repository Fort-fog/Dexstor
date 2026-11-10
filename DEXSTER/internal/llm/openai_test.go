package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIResponsesProvider(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/responses" || r.Method != "POST" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-test" {
			t.Error("missing bearer")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["store"] != false || body["model"] != "example-model" {
			t.Errorf("bad request: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"name\":\"read_file\",\"path\":\"main.go\",\"content\":\"\"}"}]}],"usage":{"input_tokens":111,"output_tokens":22}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.Provider = "openai"
	c.APIKey = "secret-test"
	got, err := c.GenerateWithStats("example-model", "inspect project")
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.PromptEvalCount != 111 || got.EvalCount != 22 {
		t.Fatalf("bad usage %+v", got)
	}
	if action, ok := Parse(got.Text); !ok || action.Name != "read_file" {
		t.Fatalf("bad action: %q", got.Text)
	}
	if err := c.Unload("example-model"); err != nil {
		t.Fatal(err)
	}
}
func TestOpenAIMissingKeyAndSanitizedError(t *testing.T) {
	c := New("http://localhost:1")
	c.Provider = "openai"
	if _, err := c.GenerateWithStats("model", "text"); err == nil {
		t.Fatal("key should be required")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("secret-token must not leak"))
	}))
	defer srv.Close()
	c.BaseURL = srv.URL
	c.APIKey = "secret-token"
	_, err := c.GenerateWithStats("model", "text")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error %v", err)
	}
}
