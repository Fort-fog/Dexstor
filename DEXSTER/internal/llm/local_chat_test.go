package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalChatProviders(t *testing.T) {
	for _, provider := range []string{"llamacpp", "unsloth"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models/unload" && provider == "llamacpp" {
					if r.Method != http.MethodPost {
						t.Errorf("unload method %s", r.Method)
					}
					var unload struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(r.Body).Decode(&unload); err != nil || unload.Model != "some-model" {
						t.Errorf("unload payload: %+v, %v", unload, err)
					}
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer local-secret" {
					t.Errorf("token not passed")
				}
				var payload struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Model != "some-model" || len(payload.Messages) != 1 || payload.Messages[0].Content != "hello" {
					t.Errorf("bad payload: %+v", payload)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"content":"{\"name\":\"declare_done\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":7}}`))
			}))
			defer server.Close()
			c := New(server.URL + "/v1")
			c.Provider = provider
			c.APIKey = "local-secret"
			got, err := c.GenerateWithStats("some-model", "hello")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Text, "declare_done") || got.PromptEvalCount != 12 || got.EvalCount != 7 {
				t.Fatalf("bad generation %+v", got)
			}
			if err := c.Unload("some-model"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
