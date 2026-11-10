package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalChatReasoningOnlyRetry(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if calls == 1 {
			if b.MaxTokens != 4096 {
				t.Errorf("first budget %d", b.MaxTokens)
			}
			w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":"thinking"}}],"usage":{"completion_tokens":4096}}`))
		} else {
			if b.MaxTokens != 8192 {
				t.Errorf("retry budget %d", b.MaxTokens)
			}
			w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"TOOL_CALL: list_files .","reasoning_content":"thoughts"}}],"usage":{"prompt_tokens":10,"completion_tokens":4400}}`))
		}
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "llamacpp"
	res, err := c.GenerateWithStats("model", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(res.Text, "TOOL_CALL") || res.ThinkingChars != len("thoughts") {
		t.Fatalf("calls=%d result=%+v", calls, res)
	}
}

func TestLocalChatReasoningOnlyNoTool(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"thinking"}}],"usage":{"completion_tokens":99}}`))
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "unsloth"
	_, err := c.GenerateWithStats("model", "prompt")
	if err == nil || !strings.Contains(err.Error(), "reasoning_chars=8") {
		t.Fatalf("err=%v", err)
	}
}

func TestLocalChatGLMThinkingDisabled(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model  string         `json:"model"`
			Kwargs map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Kwargs["enable_thinking"] != false {
			t.Fatalf("missing disabled thinking: %+v", body.Kwargs)
		}
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"TOOL_CALL: list_files ."}}],"usage":{"prompt_tokens":10,"completion_tokens":9}}`))
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "llamacpp"
	out, err := c.GenerateWithStats("unsloth/GLM-4.7-Flash-GGUF:Q6_K", "prompt")
	if err != nil || calls != 1 || !strings.Contains(out.Text, "TOOL_CALL") {
		t.Fatalf("calls=%d output=%+v err=%v", calls, out, err)
	}
}

func TestLocalChatDoesNotDisableThinkingForOtherModels(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["chat_template_kwargs"]; ok {
			t.Fatalf("unexpected thinking override in %s", string(body["chat_template_kwargs"]))
		}
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"OK"}}]}`))
	}))
	defer s.Close()
	for _, tc := range []struct{ provider, model string }{{"llamacpp", "gemma-4"}, {"unsloth", "unsloth/GLM-4.7-Flash-GGUF:Q6_K"}} {
		c := New(s.URL + "/v1")
		c.Provider = tc.provider
		if _, err := c.GenerateWithStats(tc.model, "prompt"); err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
	}
}
