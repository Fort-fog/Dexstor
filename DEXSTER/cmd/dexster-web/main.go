package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"dexster/internal/llm"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"kraken/memory"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	Root, Kraken, Model, Task, Provider, Runtime string
	StrataURL                                    string
	Context, Budget, RotateAt, MaxIterations     int
	AutoCommit, AutoVerify, Unload, HTTPVerify   bool
	HTTPURL, HTTPContains                        string
	Analyst                                      bool
	AnalystModel, AnalystPlanID, AnalystProvider string
}
type state struct {
	Status   string `json:"status"`
	PID      int    `json:"pid"`
	Started  string `json:"started"`
	Finished string `json:"finished"`
	ExitCode int    `json:"exit_code"`
	Log      string `json:"log"`
	ModelLog string `json:"model_log"`
	Config   config `json:"config"`
	Token    string `json:"-"`
}
type controller struct {
	mu                sync.Mutex
	s                 state
	cmd               *exec.Cmd
	binary            string
	token             string
	maxLog            int
	modelCurrentStart int
	modelAnswer       strings.Builder
	modelCount        int
	bridge            *modelBridge
	analystOllamaURL  string
	analystGitHubURL  string
	analystBusy       bool
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func newController(binary string) *controller {
	t := token()
	return &controller{binary: binary, token: t, maxLog: 500000, analystOllamaURL: "http://127.0.0.1:11434/api/generate", analystGitHubURL: "https://api.github.com/search/repositories", s: state{Status: "idle", Token: t}}
}
func (c *controller) snapshot() state { c.mu.Lock(); defer c.mu.Unlock(); return c.s }

// Model events are separate from controller telemetry. Raw streamed text is
// shown while Ollama generates; after a completed write_file call the pane
// displays the decoded source code instead of JSON quoting/escapes.
const modelTracePrefix = "@@DEXSTER_MODEL@@"

type modelTraceEvent struct {
	Event string `json:"event"`
	Text  string `json:"text"`
}

func (c *controller) addModelEvent(line string) {
	var evt modelTraceEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, modelTracePrefix))), &evt); err != nil {
		c.addLog("[WEB] invalid model trace event\n")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch evt.Event {
	case "start":
		if c.modelCount > 0 && c.modelCurrentStart < len(c.s.ModelLog) {
			// A retry starts a fresh generation, replacing the failed partial
			// response. No tool was executed on that incomplete response.
			if len(c.modelAnswer.String()) > 0 {
				c.s.ModelLog += "\n[model stream restarted before tool execution]\n"
			}
		}
		c.modelCount++
		c.modelAnswer.Reset()
		c.s.ModelLog += fmt.Sprintf("\n── МОДЕЛЬ · ВЫЗОВ %d ──\n", c.modelCount)
		c.modelCurrentStart = len(c.s.ModelLog)
	case "chunk":
		if c.modelAnswer.Len()+len(evt.Text) > 16<<20 {
			return
		}
		c.modelAnswer.WriteString(evt.Text)
		c.s.ModelLog += evt.Text
	case "done":
		response := c.modelAnswer.String()
		formatted := response
		if call, ok := llm.Parse(response); ok {
			if call.Name == "write_file" {
				formatted = "ФАЙЛ: " + call.PathArg() + "\n" + call.Args["content"]
			} else if call.Name == "copy_file" {
				formatted = "COPY: " + call.Args["source"] + " → " + call.PathArg()
			} else {
				formatted = "ДЕЙСТВИЕ: " + call.Name + " " + call.PathArg() + "\n" + response
			}
		}
		if c.modelCurrentStart <= len(c.s.ModelLog) {
			c.s.ModelLog = c.s.ModelLog[:c.modelCurrentStart] + formatted + "\n"
		}
		c.modelAnswer.Reset()
	case "retry":
		c.s.ModelLog += "\n[Временный сбой Ollama — повтор запроса]\n"
	case "error":
		c.s.ModelLog += "\n[Ошибка генерации: " + evt.Text + "]\n"
		c.modelAnswer.Reset()
	}
	// Keep a rolling completed history. Never truncate the current JSON
	// stream mid-generation or subsequent completion would be malformed.
	const maxModelLog = 2 << 20
	if c.modelAnswer.Len() == 0 && len(c.s.ModelLog) > maxModelLog {
		c.s.ModelLog = "... предыдущие ответы модели сокращены ...\n" + c.s.ModelLog[len(c.s.ModelLog)-maxModelLog:]
		c.modelCurrentStart = len(c.s.ModelLog)
	}
}

func (c *controller) addLog(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Log += line
	if len(c.s.Log) > c.maxLog {
		c.s.Log = "... earlier output truncated ...\n" + c.s.Log[len(c.s.Log)-c.maxLog:]
	}
}

// canonicalPath normalizes symlinks to prevent path-alias bypasses.
func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func overlaps(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))))
}
func sourceRoot() string {
	// Web app is built from the same source tree; this detects the dev installation.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	candidate := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(candidate, "cmd", "dexster", "main.go")); err == nil {
		return candidate
	}
	return ""
}

// validateProject rejects the DEXSTER source tree, nested directories and ancestors.
// The same check also runs in the CLI Router, not only in this web handler.
func validateProject(root string, autoCommit bool) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("выбери папку проекта перед START")
	}
	abs, err := canonicalPath(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("папка проекта недоступна: %w", err)
	}
	if !info.IsDir() {
		return errors.New("путь проекта не является каталогом")
	}
	for _, self := range []string{os.Getenv("DEXSTER_SELF_ROOT"), sourceRoot()} {
		if self == "" {
			continue
		}
		real, err := canonicalPath(self)
		if err != nil {
			continue
		}
		if overlaps(abs, real) {
			return errors.New("защита: нельзя изменять собственные исходники DEXSTER или WEB; выбери независимый проект")
		}
	}
	// Reject independently copied DEXSTER repositories as a conservative safeguard.
	for dir := abs; ; dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(data), "module dexster") {
			for _, target := range []string{filepath.Join(dir, "cmd", "dexster", "main.go"), filepath.Join(dir, "cmd", "dexster-web", "main.go")} {
				if _, e := os.Stat(target); e == nil {
					return errors.New("защита: исходники DEXSTER/WEB доступны только для чтения")
				}
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if autoCommit {
		// A missing .git is valid: the CLI creates a local baseline automatically
		// before the agent starts. If Git already exists, still require the
		// selected project to be that repository's exact root.
		if _, statErr := os.Lstat(filepath.Join(abs, ".git")); statErr == nil {
			cmd := exec.Command("git", "rev-parse", "--show-toplevel")
			cmd.Dir = abs
			out, err := cmd.Output()
			if err != nil {
				return errors.New("Git metadata exists but repository is unreadable")
			}
			top, err := canonicalPath(strings.TrimSpace(string(out)))
			if err != nil {
				return err
			}
			if top != abs {
				return errors.New("папка проекта должна совпадать с корнем Git-репозитория")
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	return nil
}

// krakenSummary returns metadata only. It never exposes memory contents or arbitrary paths.
type memorySummary struct {
	Root   string              `json:"root"`
	Counts map[string]int      `json:"counts"`
	Recent map[string][]string `json:"recent"`
	Error  string              `json:"error,omitempty"`
}

func summarizeKraken(root string) memorySummary {
	out := memorySummary{Root: root, Counts: map[string]int{}, Recent: map[string][]string{}}
	if root == "" {
		return out
	}
	for _, name := range []string{"events", "checkpoints", "decisions", "lessons", "experiences"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			if !os.IsNotExist(err) {
				out.Error = "KRAKEN недоступен"
			}
			continue
		}
		files := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, entry.Name())
			}
		}
		out.Counts[name] = len(files)
		sort.Strings(files)
		if len(files) > 5 {
			files = files[len(files)-5:]
		}
		out.Recent[name] = files
	}
	// Project restore points are nested by stable ProjectID. Count metadata
	// only; never expose paths or snapshot contents in the Web summary.
	points := 0
	_ = filepath.WalkDir(filepath.Join(root, "projects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			points++
		}
		return nil
	})
	out.Counts["project_points"] = points
	return out
}

func (c *controller) start(cfg config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return errors.New("a task is already running")
	}
	if c.analystBusy {
		return errors.New("Аналитик ещё готовит план")
	}
	// Explicit rollback commands are controller/KRAKEN operations. They must
	// bypass Analyst and Worker so the selected host project is restored
	// directly, with zero model calls.
	_, rollbackTask := memory.ParseRollbackIntent(cfg.Task)
	if rollbackTask {
		cfg.Runtime = "host"
		cfg.Analyst = false
	}
	// No implicit fallback: a worker task can only run in the isolated Worker.
	if cfg.Runtime != "worker" && cfg.Runtime != "host" {
		return errors.New("invalid runtime: no HOST fallback")
	}
	if cfg.Runtime == "worker" {
		if cfg.Provider != "ollama" {
			return errors.New("Worker permits only Ollama through Model Bridge; other providers require HOST")
		}
		if cfg.HTTPVerify {
			return errors.New("Worker HTTP Gate is not supported in this release")
		}
		if c.bridge == nil {
			return errors.New("Worker model bridge unavailable (fail closed)")
		}
		if err := c.bridge.Check(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.Task) == "" {
		return errors.New("task is required")
	}
	if cfg.Provider == "" {
		cfg.Provider = "ollama"
	}
	if cfg.Provider != "ollama" && cfg.Provider != "openai" && cfg.Provider != "llamacpp" && cfg.Provider != "unsloth" && cfg.Provider != "strata" {
		return errors.New("invalid model provider")
	}
	if cfg.Provider == "openai" && os.Getenv("OPENAI_API_KEY") == "" {
		return errors.New("OPENAI_API_KEY is not configured on the server")
	}
	if cfg.Provider == "unsloth" && strings.TrimSpace(os.Getenv("DEXSTER_UNSLOTH_URL")) == "" {
		return errors.New("DEXSTER_UNSLOTH_URL is not configured on the server")
	}
	if cfg.Provider == "strata" || (cfg.Analyst && cfg.AnalystProvider == "strata") {
		base, err := strataAPIURL(cfg.StrataURL)
		if err != nil {
			return err
		}
		cfg.StrataURL = base
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("model is required")
	}
	if cfg.Provider == "strata" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := requireStrataLoaded(ctx, cfg.StrataURL)
		cancel()
		if err != nil {
			return err
		}
		// Even when the shared UI checkbox was previously checked for llama.cpp,
		// STRATA rotations never unload the expensive 128K model.
		cfg.Unload = false
	}
	if err := validateProject(cfg.Root, cfg.AutoCommit && cfg.Runtime == "host" && !rollbackTask); err != nil {
		return err
	}
	abs, err := filepath.Abs(cfg.Root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("project root is not a directory")
	}
	cfg.Root = abs
	if cfg.Kraken == "" {
		cfg.Kraken = filepath.Join(filepath.Dir(abs), "kraken-web")
	}
	cfg.Kraken, err = filepath.Abs(cfg.Kraken)
	if err != nil {
		return err
	}
	if cfg.Runtime == "host" {
		if err = os.MkdirAll(filepath.Join(cfg.Kraken, "events"), 0700); err != nil {
			return err
		}
	}
	if cfg.Context < 1024 || cfg.Context > 262144 {
		return errors.New("invalid context")
	}
	if cfg.Budget < 0 || cfg.Budget > cfg.Context {
		return errors.New("invalid prompt budget")
	}
	if cfg.MaxIterations < 1 || cfg.MaxIterations > 500 {
		return errors.New("invalid max iterations")
	}
	effectiveTask := cfg.Task
	var contractWriteScope []string
	if cfg.Analyst {
		plan, e := readAnalystPlan(cfg)
		if e != nil {
			return fmt.Errorf("analyst: %w", e)
		}
		if plan.ID != cfg.AnalystPlanID {
			return errors.New("analyst: план устарел; нажми Анализ ещё раз")
		}
		effectiveTask = composeAnalystTask(cfg.Task, plan)
		contractWriteScope = append(contractWriteScope, plan.Contract.WriteScope...)
	}
	args := []string{"-model-trace=true", "-provider", cfg.Provider, "-root", cfg.Root, "-kraken-root", cfg.Kraken, "-model", cfg.Model, "-task", effectiveTask, "-num-ctx", strconv.Itoa(cfg.Context), "-prompt-budget", strconv.Itoa(cfg.Budget), "-rotate-at", strconv.Itoa(cfg.RotateAt), "-max-iterations", strconv.Itoa(cfg.MaxIterations), "-auto-verify=" + strconv.FormatBool(cfg.AutoVerify), "-auto-commit=" + strconv.FormatBool(cfg.AutoCommit), "-unload-on-rotate=" + strconv.FormatBool(cfg.Unload)}
	if cfg.Provider == "strata" {
		args = append(args, "-strata-url", cfg.StrataURL)
	}
	for _, scope := range contractWriteScope {
		args = append(args, "-scope-write", scope)
	}
	if cfg.HTTPVerify {
		args = append(args, "-verify-mode", "http_server", "-verify-http-url", cfg.HTTPURL, "-verify-http-contains", cfg.HTTPContains)
	}
	var cmd *exec.Cmd
	if cfg.Runtime == "worker" {
		payload, e := stageProject(cfg.Root)
		if e != nil {
			return fmt.Errorf("Worker staging refused: %w", e)
		}
		if e := saveInitialSnapshot(cfg.Root, cfg.Task, payload); e != nil {
			return fmt.Errorf("review snapshot: %w", e)
		}
		id := workID(cfg.Root)
		workerArgs := []string{"-n", "-u", "dexster-worker", "/usr/local/libexec/dexster-worker-run",
			"-id", id, "-socket", c.bridge.Socket, "-model", cfg.Model, "-task", effectiveTask,
			"-ctx", strconv.Itoa(cfg.Context), "-budget", strconv.Itoa(cfg.Budget),
			"-rotate", strconv.Itoa(cfg.RotateAt), "-iterations", strconv.Itoa(cfg.MaxIterations),
			"-verify=" + strconv.FormatBool(cfg.AutoVerify), "-commit=" + strconv.FormatBool(cfg.AutoCommit),
			"-unload=" + strconv.FormatBool(cfg.Unload)}
		for _, scope := range contractWriteScope {
			workerArgs = append(workerArgs, "-scope-write", scope)
		}
		cmd = exec.Command("sudo", workerArgs...)
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
		cmd.Dir = "/"
		cfg.Kraken = "/home/dexster-worker/projects/" + id + "/.kraken"
	} else {
		cmd = exec.Command(c.binary, args...)
		cmd.Env = append(os.Environ(), "DEXSTER_SELF_ROOT="+sourceRoot())
		cmd.Dir = cfg.Root
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	rd, wr, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = wr
	cmd.Stderr = wr
	if err = cmd.Start(); err != nil {
		rd.Close()
		wr.Close()
		return err
	}
	wr.Close()
	c.cmd = cmd
	c.s = state{Status: "running", PID: cmd.Process.Pid, Started: time.Now().Format(time.RFC3339), Config: cfg, Log: "", ModelLog: ""}
	c.modelCount = 0
	c.modelCurrentStart = 0
	c.modelAnswer.Reset()
	go func() {
		scanner := bufio.NewReader(rd)
		for {
			line, e := scanner.ReadString('\n')
			if len(line) > 0 {
				if strings.HasPrefix(line, modelTracePrefix) {
					c.addModelEvent(line)
				} else {
					c.addLog(line)
				}
			}
			if e != nil {
				if e != io.EOF {
					c.addLog(fmt.Sprintf("\n[WEB] log read error: %v\n", e))
				}
				break
			}
		}
		rd.Close()
	}()
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.cmd = nil
		c.s.Finished = time.Now().Format(time.RFC3339)
		if err != nil {
			c.s.Status = "failed"
			if e, ok := err.(*exec.ExitError); ok {
				c.s.ExitCode = e.ExitCode()
			} else {
				c.s.ExitCode = -1
			}
		} else {
			c.s.Status = "done"
			c.s.ExitCode = 0
		}
	}()
	return nil
}
func (c *controller) stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return errors.New("no running task")
	}
	c.s.Status = "stopping"
	return syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
}
func requireLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != "127.0.0.1:8767" && host != "localhost:8767" {
			http.Error(w, "invalid host", 403)
			return
		}
		if r.Method == "POST" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://127.0.0.1:8767" && origin != "http://localhost:8767" {
				http.Error(w, "invalid origin", 403)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// listOllamaModels queries only the loopback Ollama daemon. A slow or absent daemon
// must never stall the UI or prevent selecting a model manually.
func listOllamaModels(endpoint string) ([]string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(payload.Models))
	found := make(map[string]bool)
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = strings.TrimSpace(m.Model)
		}
		if name != "" && !found[name] {
			names = append(names, name)
			found[name] = true
		}
	}
	return names, nil
}

func (c *controller) routes() http.Handler {
	mux := http.NewServeMux()
	c.registerProjectFolderRoutes(mux)
	c.registerSavedProjectsRoutes(mux)
	c.registerLlamaModelRoutes(mux)
	c.registerStrataModelRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.snapshot())
	})
	mux.HandleFunc("/api/kraken", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		snap := c.snapshot()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(summarizeKraken(snap.Config.Kraken))
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		names, err := listOllamaModels("http://127.0.0.1:11434/api/tags")
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"models": []string{}, "error": "Ollama unavailable; enter a model manually"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": names})
	})
	mux.HandleFunc("/api/worker/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sudo", "-n", "-u", "dexster-worker", "/usr/local/libexec/dexster-worker-probe")
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
		out, err := cmd.CombinedOutput()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil || ctx.Err() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"ready": false, "detail": "Worker sandbox unavailable (fail closed)", "error": strings.TrimSpace(string(out))})
			return
		}
		if !strings.Contains(string(out), "DEXSTER_SANDBOX_PASS") {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"ready": false, "detail": "Sandbox did not pass checks"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ready": true, "detail": "Bubblewrap PASS: UID=1001, home hidden, workspace writable, network namespace isolated"})
	})
	c.reviewRoutes(mux)
	c.analystRoutes(mux)
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
		var cfg config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := c.start(cfg); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		if err := c.stop(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": c.token})
	})
	return requireLocal(mux)
}
func main() {
	binary := flag.String("dexster", "", "path to built dexster executable")
	flag.Parse()
	if *binary == "" {
		log.Fatal("supply -dexster PATH")
	}
	abs, err := filepath.Abs(*binary)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		log.Fatal(err)
	}
	c := newController(abs)
	bridge, err := newModelBridge()
	if err != nil {
		log.Fatal("Model Bridge: ", err)
	}
	defer bridge.Close()
	c.bridge = bridge
	l, err := net.Listen("tcp", "127.0.0.1:8767")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("DEXSTER WEB: http://127.0.0.1:8767/ (local only)")
	log.Fatal(http.Serve(l, c.routes()))
}
