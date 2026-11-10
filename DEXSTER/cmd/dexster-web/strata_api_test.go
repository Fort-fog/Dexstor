package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSTRATAModelListingAndToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("upstream %s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"strata","status":"loaded"}],"object":"list"}`))
	}))
	defer upstream.Close()
	t.Setenv("DEXSTER_STRATA_API_KEY", "secret")
	c := newController("/bin/true")
	handler := c.routes()
	path := "http://127.0.0.1:8767/api/strata/models?url=" + upstream.URL + "%2Fv1"
	call := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Dexster-Token", tok)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call(""); w.Code != 403 {
		t.Fatalf("no token %d", w.Code)
	}
	w := call(c.token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"strata"`) {
		t.Fatalf("models %d %s", w.Code, w.Body.String())
	}
	bad := httptest.NewRequest("GET", "http://127.0.0.1:8767/api/strata/models?url=http%3A%2F%2Fexample.com%3A8081%2Fv1", nil)
	bad.Header.Set("X-Dexster-Token", c.token)
	bw := httptest.NewRecorder()
	handler.ServeHTTP(bw, bad)
	if bw.Code != 400 {
		t.Fatalf("unsafe URL accepted: %d", bw.Code)
	}
}

func TestSTRATAAnalystPlanKeepsModelLoaded(t *testing.T) {
	calls, unloads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			w.Write([]byte(`{"loaded":true,"auto_load":true}`))
		case "/v1/chat/completions":
			calls++
			var q struct {
				Model           string         `json:"model"`
				ReasoningEffort string         `json:"reasoning_effort"`
				ResponseFormat  map[string]any `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
			}
			if q.Model != "qwen3.8:27b-iq4xs" || q.ReasoningEffort != "none" || q.ResponseFormat["type"] != "json_object" {
				t.Errorf("analyst query %+v", q)
			}
			raw := `{"goal":"Fix project code","contract":{"source":"existing files","target":"existing project","action":"repair existing source","verify":"Go tests pass","report":"summarize fixed code"},"steps":[{"title":"Inspect","action":"Read existing source","done_when":"Root cause understood"},{"title":"Fix","action":"Repair the actual defect","done_when":"Tests pass"}]}`
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": raw}, "finish_reason": "stop"}}})
			w.Write(b)
		case "/v1/unload":
			unloads++
			w.Write([]byte(`{"loaded":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	req := makeInput(t)
	req.AnalystProvider = "strata"
	req.AnalystModel = req.Model
	req.StrataURL = server.URL + "/v1"
	req, err := normalizeAnalystInput(req)
	if err != nil {
		t.Fatal(err)
	}
	c := newController("/bin/true")
	plan, err := c.requestAnalyst(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || unloads != 0 || plan.ID != planKey(req) {
		t.Fatalf("plan=%+v calls=%d unloads=%d", plan, calls, unloads)
	}
}
