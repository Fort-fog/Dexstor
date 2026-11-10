package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLlamaCPPColdRotationUnloadsRegisteredModel(t *testing.T) {
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/models/unload" || r.Method != http.MethodPost {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "unsloth/GLM-4.7-Flash-GGUF:Q6_K" {
			t.Errorf("wrong payload %+v: %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "llamacpp"
	c.APIKey = "secret"
	if err := c.Unload("unsloth/GLM-4.7-Flash-GGUF:Q6_K"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("unload hits = %d", hits)
	}
}

func TestLlamaCPPColdRotationFailsClosed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model busy", http.StatusConflict)
	}))
	defer s.Close()
	c := New(s.URL + "/v1")
	c.Provider = "llamacpp"
	if err := c.Unload("model"); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("unexpected error: %v", err)
	}
	c.BaseURL = "http://example.com/v1"
	if err := c.Unload("model"); err == nil {
		t.Fatal("nonlocal unload accepted")
	}
}

func TestUnslothUnloadRemainsNoop(t *testing.T) {
	c := New("http://127.0.0.1:65535/v1")
	c.Provider = "unsloth"
	if err := c.Unload("model"); err != nil {
		t.Fatal(err)
	}
}
