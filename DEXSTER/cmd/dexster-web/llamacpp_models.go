package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// These routes manage ONLY the independent llama.cpp router; Ollama is untouched.
func llamaRouterURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("DEXSTER_LLAMACPP_URL"))
	if raw == "" {
		raw = "http://127.0.0.1:8080/v1"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("llama.cpp URL must be a local HTTP endpoint")
	}
	host := u.Hostname()
	if host != "localhost" && net.ParseIP(host) == nil {
		return "", errors.New("llama.cpp must use loopback")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", errors.New("llama.cpp must use loopback")
	}
	if u.Port() == "" {
		return "", errors.New("llama.cpp URL requires port")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (c *controller) registerLlamaModelRoutes(mux *http.ServeMux) {
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	mux.HandleFunc("/api/llamacpp/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		llamaModelProxy(w, r, client, http.MethodGet, "/models", nil)
	})
	mux.HandleFunc("/api/llamacpp/models/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		if status := c.snapshot().Status; status == "running" || status == "stopping" {
			http.Error(w, "stop DEXSTER task before downloading", 409)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		var input struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid model", 400)
			return
		}
		input.Model = strings.TrimSpace(input.Model)
		if len(input.Model) < 5 || len(input.Model) > 200 || strings.ContainsAny(input.Model, " \t\r\n\\") || !strings.Contains(input.Model, "/") || !strings.Contains(input.Model, ":") {
			http.Error(w, "provide HF GGUF repo:quant", 400)
			return
		}
		data, _ := json.Marshal(input)
		llamaModelProxy(w, r, client, http.MethodPost, "/models", data)
	})
	for _, op := range []string{"load", "unload"} {
		op := op
		mux.HandleFunc("/api/llamacpp/models/"+op, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method", 405)
				return
			}
			if r.Header.Get("X-Dexster-Token") != c.token {
				http.Error(w, "forbidden", 403)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			var req struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(strings.TrimSpace(req.Model)) == 0 || len(req.Model) > 512 {
				http.Error(w, "invalid model", 400)
				return
			}
			req.Model = strings.TrimSpace(req.Model)
			snap := c.snapshot()
			if snap.Status == "running" || snap.Status == "stopping" {
				http.Error(w, "stop DEXSTER task before managing llama.cpp models", 409)
				return
			}
			// A model must already exist in the router; never turn this into a download endpoint.
			data, err := llamaListModels(r.Context(), client)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			found := false
			for _, m := range data.Data {
				if m.ID == req.Model {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "model not registered in llama.cpp router", 404)
				return
			}
			body, _ := json.Marshal(req)
			llamaModelProxy(w, r, client, http.MethodPost, "/models/"+op, body)
		})
	}
}

type llamaModelsResponse struct {
	Data []struct {
		ID     string `json:"id"`
		Status struct {
			Value string `json:"value"`
		} `json:"status"`
	} `json:"data"`
}

func llamaListModels(ctx context.Context, client *http.Client) (llamaModelsResponse, error) {
	var result llamaModelsResponse
	base, err := llamaRouterURL()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return result, err
	}
	res, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, errors.New("llama.cpp model list unavailable")
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&result)
	return result, err
}
func llamaModelProxy(w http.ResponseWriter, r *http.Request, client *http.Client, method, path string, body []byte) {
	base, err := llamaRouterURL()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var b io.Reader
	if body != nil {
		b = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, base+path, b)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, "llama.cpp router unavailable: "+err.Error(), 502)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	io.Copy(w, io.LimitReader(res.Body, 2<<20))
}
