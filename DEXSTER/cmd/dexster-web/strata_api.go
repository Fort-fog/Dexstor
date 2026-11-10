package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// This controller never restarts an existing STRATA engine or changes a preset.
// Only the IQ3_S model already installed on this machine is started with 128K.
var strataLaunchMu sync.Mutex
var strataStartedByDexster bool

func strataLaunch128K(ctx context.Context, base, model string) error {
	if model != "iq3_s" {
		return fmt.Errorf("STRATA: неподдерживаемый ID %q; автоматический запуск разрешён только для установленной iq3_s", model)
	}
	u, err := url.Parse(base)
	if err != nil || u.Port() != "8081" {
		return errors.New("STRATA: для управляемого запуска требуется локальный порт 8081")
	}
	strataLaunchMu.Lock()
	defer strataLaunchMu.Unlock()
	if loaded, err := strataLoaded(ctx, base); err == nil && loaded {
		return nil
	}
	// Never interfere with a different running launch, including 128K on another port.
	statusCtx, cancelStatus := context.WithTimeout(ctx, 8*time.Second)
	status, statusErr := exec.CommandContext(statusCtx, "strata", "launch", "--status").CombinedOutput()
	cancelStatus()
	if statusErr == nil && strings.Contains(strings.ToLower(string(status)), "http://127.0.0.1:") && !strings.Contains(string(status), ":8081") {
		return fmt.Errorf("STRATA already running at another endpoint; refusing second launch: %.300s", status)
	}
	args := []string{"launch", "--family", "qwen", "--model", "iq3_s", "--context", "128k", "--access", "local", "--port", "8081", "--yes", "--no-tui"}
	cmd := exec.CommandContext(ctx, "strata", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("STRATA launch: %w: %.800s", err, output)
	}
	loaded, err := strataLoaded(ctx, base)
	if err != nil || !loaded {
		return fmt.Errorf("STRATA launch завершился, но API не подтвердил загрузку: %v: %.500s", err, output)
	}
	strataStartedByDexster = true
	return nil
}

func strataStopOwned(ctx context.Context) error {
	strataLaunchMu.Lock()
	defer strataLaunchMu.Unlock()
	if !strataStartedByDexster {
		return errors.New("STRATA запущена вне DEXSTER; её остановка запрещена")
	}
	output, err := exec.CommandContext(ctx, "strata", "launch", "--stop").CombinedOutput()
	if err != nil {
		return fmt.Errorf("STRATA stop: %w: %.500s", err, output)
	}
	strataStartedByDexster = false
	return nil
}

// strataAPIURL accepts only a local STRATA service to avoid proxy SSRF and
// to prevent accidentally sending project content to an untrusted endpoint.
func strataAPIURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("DEXSTER_STRATA_URL"))
	}
	if value == "" {
		return "", errors.New("укажи адрес STRATA /v1 (например, http://127.0.0.1:8081/v1)")
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("STRATA URL должен быть http://127.0.0.1:PORT/v1")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", errors.New("STRATA URL должен быть адресом локального сервера")
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "" && path != "/v1" {
		return "", errors.New("STRATA URL: допустим только путь /v1")
	}
	u.Path = "/v1"
	return u.String(), nil
}

// strataHTTPClient never follows redirects away from the validated loopback API.
func strataHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// strataUpstream sends controls only to the existing, locally managed STRATA
// server. DEXSTER never spawns a new engine or changes its tuning/profile.
func strataUpstream(ctx context.Context, method, base, path string, body []byte, timeout time.Duration) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := os.Getenv("DEXSTER_STRATA_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return strataHTTPClient(timeout).Do(req)
}

func strataLoaded(ctx context.Context, base string) (bool, error) {
	// STRATA Desktop Host exposes OpenAI /v1/models but not /v1/status.
	// A successful model listing is enough to accept an existing API connection;
	// no model startup or implicit reload is attempted by DEXSTER.
	if u, err := url.Parse(base); err == nil && u.Port() == "47831" {
		resp, err := strataUpstream(ctx, http.MethodGet, base, "/models", nil, 10*time.Second)
		if err != nil {
			return false, fmt.Errorf("STRATA Desktop API: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("STRATA Desktop /v1/models HTTP %d", resp.StatusCode)
		}
		var listing struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&listing); err != nil {
			return false, fmt.Errorf("STRATA Desktop models JSON: %w", err)
		}
		for _, m := range listing.Data {
			if m.ID == "iq3_s" {
				return true, nil
			}
		}
		return false, errors.New("STRATA Desktop API: iq3_s отсутствует в списке моделей")
	}
	resp, err := strataUpstream(ctx, http.MethodGet, base, "/status", nil, 10*time.Second)
	if err != nil {
		return false, fmt.Errorf("STRATA API недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("STRATA status HTTP %d", resp.StatusCode)
	}
	var data struct {
		Loaded *bool `json:"loaded"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return false, fmt.Errorf("STRATA status JSON: %w", err)
	}
	if data.Loaded == nil {
		return false, errors.New("STRATA status: сервер не сообщил loaded")
	}
	return *data.Loaded, nil
}

// requireStrataLoaded prevents an ordinary task or Analyst call from silently
// triggering STRATA's slow auto-load. Only the dedicated UI button loads it.
func requireStrataLoaded(ctx context.Context, base string) error {
	loaded, err := strataLoaded(ctx, base)
	if err != nil {
		return err
	}
	if !loaded {
		return errors.New("STRATA выгружена. Сначала нажми «⬆ Загрузить STRATA» вручную")
	}
	return nil
}

func (c *controller) strataControlBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.analystBusy || c.s.Status == "running" || c.s.Status == "stopping"
}

func (c *controller) registerStrataModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/strata/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		base, err := strataAPIURL(r.URL.Query().Get("url"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if key := os.Getenv("DEXSTER_STRATA_API_KEY"); key != "" {
			upstream.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(upstream)
		if err != nil {
			// STRATA Desktop can be installed while its model API is stopped.
			// Read real local installed configurations via its existing CLI;
			// never substitute Ollama models or pretend they are loaded.
			cmdCtx, stop := context.WithTimeout(r.Context(), 5*time.Second)
			defer stop()
			output, cliErr := exec.CommandContext(cmdCtx, "strata", "models", "list", "--installed", "--output", "json").Output()
			if cliErr == nil {
				var local struct {
					OK     bool `json:"ok"`
					Models []struct {
						ID     string `json:"id"`
						Name   string `json:"name"`
						Status string `json:"component_status"`
					} `json:"models"`
				}
				if json.Unmarshal(output, &local) == nil && local.OK {
					models := make([]map[string]string, 0, len(local.Models))
					for _, model := range local.Models {
						if model.ID != "" && model.Status == "present" {
							models = append(models, map[string]string{"id": model.ID, "object": "model", "name": model.Name})
						}
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Cache-Control", "no-store")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"data": models, "source": "strata-local-installed",
						"warning": "STRATA API не запущен: показаны установленные локальные модели, а не загруженные",
					})
					return
				}
			}
			http.Error(w, "STRATA API недоступен: "+err.Error()+"; локальный список STRATA тоже недоступен", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			http.Error(w, "STRATA models read failed", http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("STRATA models HTTP %d: %.200s", resp.StatusCode, string(data)), http.StatusBadGateway)
			return
		}
		var obj struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &obj); err != nil {
			http.Error(w, "STRATA returned invalid model JSON", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/strata/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		base, err := strataAPIURL(r.URL.Query().Get("url"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		loaded, err := strataLoaded(ctx, base)
		if err != nil {
			// Desktop host on 47831 is not the model API; offline is not a loaded model.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"loaded": false, "online": false, "detail": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]bool{"loaded": loaded, "online": true})
	})
	for _, op := range []string{"load", "unload"} {
		op := op
		mux.HandleFunc("/api/strata/models/"+op, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("X-Dexster-Token") != c.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if c.strataControlBusy() {
				http.Error(w, "STRATA: дождись завершения задачи или Аналитика", http.StatusConflict)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			var input struct {
				URL   string `json:"url"`
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid STRATA control JSON", http.StatusBadRequest)
				return
			}
			model := strings.TrimSpace(input.Model)
			if model == "" || len(model) > 256 {
				http.Error(w, "укажи модель STRATA", http.StatusBadRequest)
				return
			}
			base, err := strataAPIURL(input.URL)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// STRATA Desktop owns the already loaded model. Never send load/unload
			// or call strata launch to a Desktop-managed session.
			if u, e := url.Parse(base); e == nil && u.Port() == "47831" {
				if op == "unload" {
					http.Error(w, "STRATA Desktop управляет выгрузкой самостоятельно; DEXSTER не останавливает чужую сессию", http.StatusConflict)
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				present, e := strataLoaded(ctx, base)
				if e != nil || !present {
					http.Error(w, fmt.Sprintf("STRATA Desktop недоступна: %v", e), http.StatusBadGateway)
					return
				}
				if model != "iq3_s" {
					http.Error(w, "Выбери API ID iq3_s", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// The HTTP load endpoint cannot start a server that is not listening.
			// Start the existing STRATA CLI first; do not silently restart another model.
			if op == "load" {
				probeCtx, probeCancel := context.WithTimeout(r.Context(), 3*time.Second)
				_, probeErr := strataLoaded(probeCtx, base)
				probeCancel()
				if probeErr != nil {
					// An absent HTTP listener does not prove an unloaded model.
					// Desktop/host may already hold a 128K instance in VRAM.
					// Never spawn a second heavyweight engine based on connection refused.
					http.Error(w, "STRATA API не отвечает по "+base+". Возможна уже загруженная модель в STRATA Desktop. Повторный запуск заблокирован во избежание OOM; требуется адрес API существующего inference-сервера.", http.StatusConflict)
					return
				}
			}
			if op == "unload" && strataStartedByDexster {
				stopCtx, stopCancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer stopCancel()
				if err := strataStopOwned(stopCtx); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			body, _ := json.Marshal(map[string]string{"model": model})
			timeout := 45 * time.Second
			if op == "load" {
				// The user's IQ3_S engine takes ~200 s to load; no artificial 15 s timeout.
				timeout = 12 * time.Minute
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			resp, err := strataUpstream(ctx, http.MethodPost, base, "/"+op, body, timeout)
			if err != nil {
				http.Error(w, "STRATA "+op+": "+err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				http.Error(w, fmt.Sprintf("STRATA %s HTTP %d: %.300s", op, resp.StatusCode, string(data)), resp.StatusCode)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
		})
	}

}
