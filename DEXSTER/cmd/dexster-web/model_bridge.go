package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ModelBridge exposes one purpose only: /api/generate against local Ollama.
// The worker network namespace remains isolated and cannot reach host TCP.
type modelBridge struct {
	Dir      string
	Socket   string
	server   *http.Server
	listener net.Listener
}

func newModelBridge() (*modelBridge, error) {
	dir, err := os.MkdirTemp("", "dexster-model-")
	if err != nil {
		return nil, err
	}
	cleanup := func(e error) (*modelBridge, error) { os.RemoveAll(dir); return nil, e }
	if err = os.Chmod(dir, 0711); err != nil {
		return cleanup(err)
	}
	path := filepath.Join(dir, "ollama.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return cleanup(err)
	}
	if err = os.Chmod(path, 0666); err != nil {
		ln.Close()
		return cleanup(err)
	}
	client := &http.Client{Timeout: 7 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/generate" {
			http.Error(w, "denied", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		out, err := http.NewRequestWithContext(r.Context(), "POST", "http://127.0.0.1:11434/api/generate", r.Body)
		if err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		out.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(out)
		if err != nil {
			http.Error(w, "Ollama unavailable", 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		// Do not buffer the entire Ollama response in Worker mode: forward
		// each upstream read immediately so the model console updates live.
		io.Copy(flushResponseWriter{ResponseWriter: w}, io.LimitReader(resp.Body, 32<<20))
	})
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	b := &modelBridge{Dir: dir, Socket: path, server: server, listener: ln}
	go func() { _ = server.Serve(ln) }()
	return b, nil
}
func (b *modelBridge) Close() error {
	if b == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := b.server.Shutdown(ctx)
	b.listener.Close()
	os.RemoveAll(b.Dir)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (b *modelBridge) Check() error {
	if b == nil || b.Socket == "" {
		return fmt.Errorf("model bridge disabled")
	}
	if _, err := os.Stat(b.Socket); err != nil {
		return fmt.Errorf("model bridge unavailable: %w", err)
	}
	return nil
}

// flushResponseWriter preserves HTTP status/headers but flushes every copied
// upstream chunk; it is used only for the loopback Model Bridge.
type flushResponseWriter struct{ http.ResponseWriter }

func (w flushResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
