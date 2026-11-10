package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLlamaRouterAddressRejectsExternal(t *testing.T) {
	old, exists := os.LookupEnv("DEXSTER_LLAMACPP_URL")
	defer func() {
		if exists {
			os.Setenv("DEXSTER_LLAMACPP_URL", old)
		} else {
			os.Unsetenv("DEXSTER_LLAMACPP_URL")
		}
	}()
	for _, url := range []string{"https://127.0.0.1:8080/v1", "http://example.com:8080/v1", "http://8.8.8.8:8080/v1"} {
		os.Setenv("DEXSTER_LLAMACPP_URL", url)
		if _, err := llamaRouterURL(); err == nil {
			t.Fatalf("accepted nonlocal URL %q", url)
		}
	}
	os.Setenv("DEXSTER_LLAMACPP_URL", "http://127.0.0.1:8080/v1")
	if s, err := llamaRouterURL(); err != nil || s != "http://127.0.0.1:8080" {
		t.Fatalf("bad router base %s: %v", s, err)
	}
}
func TestLlamaModelControls(t *testing.T) {
	var loads, unloads int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"registered","status":{"value":"loaded"}}]}`))
		case "/models/load":
			loads++
			w.Write([]byte(`{"success":true}`))
		case "/models/unload":
			unloads++
			w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	old, exists := os.LookupEnv("DEXSTER_LLAMACPP_URL")
	defer func() {
		if exists {
			os.Setenv("DEXSTER_LLAMACPP_URL", old)
		} else {
			os.Unsetenv("DEXSTER_LLAMACPP_URL")
		}
	}()
	os.Setenv("DEXSTER_LLAMACPP_URL", upstream.URL+"/v1")
	c := newController("/nonexistent")
	handler := c.routes()
	issue := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"; r.Host = "127.0.0.1:8767"
		r.Header.Set("X-Dexster-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if res := issue("GET", "/api/llamacpp/models", "", c.token); res.Code != 200 || !strings.Contains(res.Body.String(), "registered") {
		t.Fatalf("status: %d %s", res.Code, res.Body.String())
	}
	if res := issue("POST", "/api/llamacpp/models/load", `{"model":"registered"}`, ""); res.Code != 403 {
		t.Fatalf("missing token status: %d", res.Code)
	}
	if res := issue("POST", "/api/llamacpp/models/load", `{"model":"unknown"}`, c.token); res.Code != 404 {
		t.Fatalf("unknown model status: %d", res.Code)
	}
	if res := issue("POST", "/api/llamacpp/models/load", `{"model":"registered"}`, c.token); res.Code != 200 {
		t.Fatalf("load status: %d %s", res.Code, res.Body.String())
	}
	c.mu.Lock()
	c.s.Status = "running"
	c.mu.Unlock()
	if res := issue("POST", "/api/llamacpp/models/unload", `{"model":"registered"}`, c.token); res.Code != 409 {
		t.Fatalf("running guard status: %d", res.Code)
	}
	c.mu.Lock()
	c.s.Status = "idle"
	c.mu.Unlock()
	if res := issue("POST", "/api/llamacpp/models/unload", `{"model":"registered"}`, c.token); res.Code != 200 {
		t.Fatalf("unload status: %d %s", res.Code, res.Body.String())
	}
	if loads != 1 || unloads != 1 {
		t.Fatalf("upstream counts load=%d unload=%d", loads, unloads)
	}
}
