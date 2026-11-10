package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSTRATAManualControlsAndStatus(t *testing.T) {
	loaded := true
	loads, unloads := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer manual-secret" {
			t.Errorf("missing STRATA API key")
		}
		switch r.URL.Path {
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]bool{"loaded": loaded})
		case "/v1/load", "/v1/unload":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("bad control method/headers")
			}
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["model"] != "strata-test" {
				t.Errorf("bad control body: %+v %v", input, err)
			}
			if r.URL.Path == "/v1/load" {
				loads++
				loaded = true
			} else {
				unloads++
				loaded = false
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("DEXSTER_STRATA_API_KEY", "manual-secret")
	c := newController("/bin/true")
	h := c.routes()
	base := upstream.URL + "/v1"
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:8767"+path, strings.NewReader(body))
		req.Header.Set("X-Dexster-Token", token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	statusPath := "/api/strata/status?url=" + base
	if got := request("GET", statusPath, "", ""); got.Code != 403 {
		t.Fatalf("unauthorized status %d", got.Code)
	}
	if got := request("GET", statusPath, "", c.token); got.Code != 200 || !strings.Contains(got.Body.String(), `"loaded":true`) {
		t.Fatalf("initial status: %d %s", got.Code, got.Body.String())
	}
	body := `{"url":"` + base + `","model":"strata-test"}`
	if got := request("POST", "/api/strata/models/unload", body, ""); got.Code != 403 {
		t.Fatalf("unauthorized unload %d", got.Code)
	}
	if unloads != 0 {
		t.Fatal("unauthorized command reached upstream")
	}
	c.mu.Lock()
	c.s.Status = "running"
	c.mu.Unlock()
	if got := request("POST", "/api/strata/models/unload", body, c.token); got.Code != 409 {
		t.Fatalf("unload while running %d", got.Code)
	}
	c.mu.Lock()
	c.s.Status = "idle"
	c.mu.Unlock()
	if got := request("POST", "/api/strata/models/unload", body, c.token); got.Code != 204 {
		t.Fatalf("manual unload %d %s", got.Code, got.Body.String())
	}
	if err := requireStrataLoaded(context.Background(), base); err == nil || !strings.Contains(err.Error(), "вручную") {
		t.Fatalf("task should be blocked when unloaded: %v", err)
	}
	if got := request("POST", "/api/strata/models/load", body, c.token); got.Code != 204 {
		t.Fatalf("manual load %d %s", got.Code, got.Body.String())
	}
	if err := requireStrataLoaded(context.Background(), base); err != nil {
		t.Fatalf("loaded model blocked: %v", err)
	}
	if loads != 1 || unloads != 1 {
		t.Fatalf("loads=%d unloads=%d", loads, unloads)
	}
	unsafeBody := `{"url":"http://example.com:8081/v1","model":"strata-test"}`
	if got := request("POST", "/api/strata/models/unload", unsafeBody, c.token); got.Code != 400 {
		t.Fatalf("nonlocal url accepted: %d", got.Code)
	}
}

// Starting an ordinary task must not implicitly trigger the 200 s STRATA reload.
func TestSTRATAStartRequiresManualLoad(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Errorf("unexpected upstream %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"loaded":false,"auto_load":true}`))
	}))
	defer upstream.Close()
	c := newController("/bin/true")
	err := c.start(config{Runtime: "host", Provider: "strata", StrataURL: upstream.URL + "/v1", Root: t.TempDir(), Model: "strata-test", Task: "test", Context: 8192, Budget: 4000, MaxIterations: 5})
	if err == nil || !strings.Contains(err.Error(), "вручную") {
		t.Fatalf("task was not blocked: %v", err)
	}
}
