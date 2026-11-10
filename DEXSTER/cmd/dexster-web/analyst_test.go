package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const analystFakeResponse = `{"response":"{\"goal\":\"Build project dashboard\",\"contract\":{\"source\":\"existing project\",\"target\":\"existing project\",\"action\":\"implement dashboard\",\"verify\":\"Go tests pass\",\"report\":\"summarize changes\"},\"steps\":[{\"title\":\"Structure\",\"action\":\"Create Go HTTP server and HTML\",\"done_when\":\"GET / returns 200\"},{\"title\":\"Carousel\",\"action\":\"Implement requested dashboard behavior\",\"done_when\":\"Requested behavior works\"},{\"title\":\"Verify\",\"action\":\"Run Go tests and verify static paths\",\"done_when\":\"Tests pass\"}],\"constraints\":[\"No npm\"],\"research_queries\":[\"vanilla javascript dashboard\"]}","done":true}`

func makeInput(t *testing.T) analystPlanInput {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := normalizeAnalystInput(analystPlanInput{Root: root, Kraken: filepath.Join(base, "kraken"), Task: "Build existing project dashboard", Model: "qwen3.8:27b-iq4xs", AnalystModel: "qwen3:4b-instruct", AnalystContext: 8192})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnalystSeparateModelResearchCacheAndResume(t *testing.T) {
	input := makeInput(t)
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("wrong Ollama path: %s", r.URL.Path)
		}
		calls++
		var request struct {
			Model     string `json:"model"`
			Prompt    string `json:"prompt"`
			Stream    bool   `json:"stream"`
			Format    string `json:"format"`
			KeepAlive int    `json:"keep_alive"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid request")
		}
		if request.Model != "qwen3:4b-instruct" || !strings.Contains(request.Prompt, "Build existing project dashboard") || request.Stream || request.Format != "json" || request.KeepAlive != 0 {
			t.Errorf("wrong planner request: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, analystFakeResponse)
	}))
	defer model.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "vanilla javascript dashboard") {
			t.Errorf("GitHub query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[{"full_name":"test/css-3d-carousel","html_url":"https://github.com/test/css-3d-carousel","description":"Sample"},{"full_name":"evil","html_url":"https://malware.invalid/code","description":"ignored"}]}`)
	}))
	defer github.Close()
	c := newController("/bin/true")
	c.analystOllamaURL = model.URL + "/api/generate"
	c.analystGitHubURL = github.URL + "/search"
	request, _ := json.Marshal(input)
	do := func() (int, analystPlan) {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/analyst/plan", bytes.NewReader(request))
		req.Header.Set("X-Dexster-Token", c.token)
		w := httptest.NewRecorder()
		c.routes().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("planner HTTP %d: %s", w.Code, w.Body.String())
		}
		var p analystPlan
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return w.Code, p
	}
	_, plan := do()
	if plan.ID != planKey(input) || len(plan.Steps) != 3 || len(plan.References) != 1 || !strings.Contains(plan.ResearchStatus, "GitHub Search API") {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(input.Root, ".dexster")); !os.IsNotExist(err) {
		t.Fatalf("analyst wrote in project: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	_, again := do()
	if again.ID != plan.ID || calls != 1 {
		t.Fatal("analysis cache did not prevent second LLM request")
	}
	restored, err := loadAnalystPlan(input)
	if err != nil || restored.ID != plan.ID {
		t.Fatalf("KRAKEN restore: %v", err)
	}
	task := composeAnalystTask(input.Task, restored)
	for _, want := range []string{input.Task, "Carousel", "https://github.com/test/css-3d-carousel", "Evidence Gate"} {
		if !strings.Contains(task, want) {
			t.Fatalf("missing in task: %s", want)
		}
	}
}

func TestAnalystStartRefusesAbsentOrStalePlan(t *testing.T) {
	input := makeInput(t)
	c := newController("/bin/true")
	cfg := config{Runtime: "host", Provider: "ollama", Root: input.Root, Kraken: input.Kraken, Task: input.Task, Model: input.Model, Context: 16384, Budget: 9000, MaxIterations: 35, Analyst: true, AnalystModel: input.AnalystModel, AnalystPlanID: planKey(input)}
	if err := c.start(cfg); err == nil || !strings.Contains(err.Error(), "analyst") {
		t.Fatalf("allowed start without plan: %v", err)
	}
	p := analystPlan{ID: planKey(input), Goal: "G", Steps: []analystStep{{Title: "A", Action: "A", DoneWhen: "A"}, {Title: "B", Action: "B", DoneWhen: "B"}}, AnalystModel: input.AnalystModel, Contract: taskContract{Source: "existing project", Target: "existing project", Action: "implement", Verify: "go test", Report: "summary"}}
	if err := persistAnalystPlan(input, p); err != nil {
		t.Fatal(err)
	}
	cfg.AnalystPlanID = "wrong"
	if err := c.start(cfg); err == nil || !strings.Contains(err.Error(), "устарел") {
		t.Fatalf("allowed stale plan: %v", err)
	}
}

func TestAnalystPlanRequestRequiresTokenAndValidPlan(t *testing.T) {
	input := makeInput(t)
	c := newController("/bin/true")
	payload, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/analyst/plan", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	c.routes().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("missing token accepted %d", w.Code)
	}
	c.analystOllamaURL = "http://127.0.0.1:1/api/generate"
	req = httptest.NewRequest("POST", "http://127.0.0.1:8767/api/analyst/plan", bytes.NewReader(payload))
	req.Header.Set("X-Dexster-Token", c.token)
	w = httptest.NewRecorder()
	c.routes().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Fatalf("missing model accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(analystPlanPath(input)); !os.IsNotExist(err) {
		t.Fatalf("bad plan saved: %v", err)
	}
}

func TestAnalystUIIsTopRightAndSeparatelySelectable(t *testing.T) {
	for _, want := range []string{`class="right-stack"`, `id="analystModel"`, `id="analystPlan"`, `id="analystToggle"`, `id="analyzeBtn"`, `AnalystPlanID:preparedPlan`} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing web UI element %s", want)
		}
	}
}

func TestAnalystPlanSurvivesControllerRestartAndExecutes(t *testing.T) {
	input := makeInput(t)
	plan := analystPlan{ID: planKey(input), Goal: "Dashboard", Steps: []analystStep{{Title: "Plan", Action: "Make server", DoneWhen: "HTTP OK"}, {Title: "Test", Action: "Run tests", DoneWhen: "Tests OK"}}, AnalystModel: input.AnalystModel, Contract: taskContract{Source: "existing project", Target: "existing project", Action: "implement", Verify: "go test", Report: "summary"}}
	if err := persistAnalystPlan(input, plan); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "task-output.txt")
	script := filepath.Join(t.TempDir(), "fake-agent.sh")
	// The fake execution proves the original prompt and plan are passed to the CLI.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n if [ \"$1\" = '-task' ]; then shift; printf '%s' \"$1\" > \""+out+"\"; exit 0; fi\n shift\ndone\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := newController(script) // a new controller simulates a web restart
	cfg := config{Runtime: "host", Provider: "ollama", Root: input.Root, Kraken: input.Kraken, Task: input.Task, Model: input.Model, Context: 16384, Budget: 9000, MaxIterations: 35, Analyst: true, AnalystModel: input.AnalystModel, AnalystPlanID: plan.ID}
	if err := c.start(cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for c.snapshot().Status == "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), input.Task) || !strings.Contains(string(b), "Make server") {
		t.Fatalf("exec task lacked plan: %q", b)
	}
}
