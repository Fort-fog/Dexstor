package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLlamaCppAnalystSeparateProvider(t *testing.T) {
	calls := 0
	unloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/unload" {
			unloads++
			var packet struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&packet); err != nil || packet.Model != "small-gguf" {
				t.Errorf("unload packet %+v %v", packet, err)
			}
			w.Write([]byte(`{"success":true}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("route %s", r.URL.Path)
			http.Error(w, "bad", 404)
			return
		}
		calls++
		var packet struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Error(err)
		}
		if packet.Model != "small-gguf" || len(packet.Messages) != 2 || !strings.Contains(packet.Messages[1].Content, "Build existing project dashboard") {
			t.Errorf("packet: %+v", packet)
		}
		raw := `{"goal":"Build project dashboard","contract":{"source":"existing project","target":"existing project","action":"implement dashboard","verify":"Go tests pass","report":"summarize changes"},"steps":[{"title":"Structure","action":"Create Go HTTP server and HTML","done_when":"GET / returns 200"},{"title":"Carousel","action":"Implement requested dashboard behavior","done_when":"Requested behavior works"}],"constraints":["No npm"]}`
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": raw}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer server.Close()
	t.Setenv("DEXSTER_LLAMACPP_URL", server.URL+"/v1")
	input := makeInput(t)
	input.AnalystModel = "small-gguf"
	input.AnalystProvider = "llamacpp"
	input, err := normalizeAnalystInput(input)
	if err != nil {
		t.Fatal(err)
	}
	c := newController("/bin/true")
	plan, err := c.requestAnalyst(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || unloads != 1 || plan.ID != planKey(input) || plan.AnalystModel != "small-gguf" {
		t.Fatalf("plan %+v calls %d", plan, calls)
	}
}

func TestLlamaCppDownloadProtected(t *testing.T) {
	downloads := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" && r.Method == "POST" {
			downloads++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "unsloth/GLM-4.7-Flash-GGUF:Q6_K" {
				t.Errorf("download body %+v", body)
			}
			w.Write([]byte(`{"success":true}`))
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	t.Setenv("DEXSTER_LLAMACPP_URL", upstream.URL+"/v1")
	c := newController("/bin/true")
	handler := c.routes()
	issue := func(model, token string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/llamacpp/models/download", strings.NewReader(`{"model":"`+model+`"}`))
		r.Header.Set("X-Dexster-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := issue("unsloth/GLM-4.7-Flash-GGUF:Q6_K", ""); code != 403 {
		t.Fatalf("no token %d", code)
	}
	if code := issue("bad-name", c.token); code != 400 {
		t.Fatalf("invalid model %d", code)
	}
	if code := issue("unsloth/GLM-4.7-Flash-GGUF:Q6_K", c.token); code != 200 {
		t.Fatalf("download %d", code)
	}
	if downloads != 1 {
		t.Fatalf("downloads %d", downloads)
	}
}
