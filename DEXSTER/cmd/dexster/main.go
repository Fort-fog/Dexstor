// Command dexster runs the DEXSTER autonomous code agent on a local project.
//
// Usage:
//
//	dexster -root DIR -task "..." [-model qwen3:8b] \
//	    [-ollama-url http://localhost:11434] [-max-iterations 20]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dexster/internal/agent"
	"dexster/internal/llm"
	"kraken/memory"
)

type protectedRules []string

func (r *protectedRules) String() string         { return strings.Join(*r, ",") }
func (r *protectedRules) Set(value string) error { *r = append(*r, value); return nil }

func runGit(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ensureManagedGit makes the Web/CLI Git checkbox self-contained. It never
// resets or cleans user files: the current project state becomes the baseline
// commit before DEXSTER starts modifying source.
func ensureManagedGit(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if st, err := os.Lstat(filepath.Join(abs, ".git")); os.IsNotExist(err) {
		if _, err := runGit(abs, "init"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !st.IsDir() && !st.Mode().IsRegular() {
		return fmt.Errorf("project .git is not a normal Git directory/file")
	}
	top, err := runGit(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(string(top)))
	if err != nil {
		return err
	}
	if filepath.Clean(topAbs) != filepath.Clean(abs) {
		return fmt.Errorf("project root %s differs from Git root %s", abs, topAbs)
	}
	// Local identity only; never touches global Git configuration.
	if _, err := runGit(abs, "config", "user.name", "DEXSTER"); err != nil {
		return err
	}
	if _, err := runGit(abs, "config", "user.email", "dexster@localhost"); err != nil {
		return err
	}
	// Agent state is KRAKEN/DEXSTER runtime metadata, never project source.
	// Ignore it locally without modifying the user's .gitignore. Previously
	// tracked state must also be removed from the index (not from disk), or
	// renaming state.json on the next task dirties the Git baseline.
	exclude := filepath.Join(abs, ".git", "info", "exclude")
	if st, err := os.Lstat(filepath.Join(abs, ".git")); err == nil && st.IsDir() {
		data, err := os.ReadFile(exclude)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if !strings.Contains("\n"+string(data)+"\n", "\n/.dexster/\n") {
			f, err := os.OpenFile(exclude, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			if _, err = f.WriteString("\n# DEXSTER local runtime state (not project source)\n/.dexster/\n"); err != nil {
				f.Close()
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	if _, err := runGit(abs, "rm", "-r", "--cached", "--ignore-unmatch", "--", ".dexster"); err != nil {
		return err
	}
	if _, err := runGit(abs, "add", "-A"); err != nil {
		return err
	}
	// Commit current state when HEAD is missing or the index/worktree has data.
	_, headErr := runGit(abs, "rev-parse", "--verify", "HEAD")
	status, statusErr := runGit(abs, "status", "--porcelain")
	if statusErr != nil {
		return statusErr
	}
	if headErr != nil || strings.TrimSpace(string(status)) != "" {
		if _, err := runGit(abs, "commit", "--allow-empty", "-m", "DEXSTER baseline before task"); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	var protected protectedRules
	var writeScope protectedRules
	flag.Var(&writeScope, "scope-write", "freeze allowed source write path for this task (repeatable); omitted allows legacy writes")
	flag.Var(&protected, "protect", "protect file or file:GoFunction (repeatable)")
	provider := flag.String("provider", "ollama", "model provider: ollama, openai, llamacpp, unsloth, strata")
	model := flag.String("model", "qwen3:8b", "Ollama model name")
	strataURL := flag.String("strata-url", "", "STRATA OpenAI-compatible /v1 endpoint (overrides DEXSTER_STRATA_URL)")
	root := flag.String("root", ".", "project directory (sandbox root)")
	task := flag.String("task", "", "task description for the agent (required)")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama-compatible API base URL")
	krakenRoot := flag.String("kraken-root", "", "KRAKEN memory directory; empty disables event recording")
	numCtx := flag.Int("num-ctx", 16384, "Ollama num_ctx per request")
	rotateAt := flag.Int("rotate-at", 0, "Ollama measured prompt token threshold for cold rotation; 0 uses 85% of num-ctx")
	unloadOnRotate := flag.Bool("unload-on-rotate", true, "release model from VRAM after a checkpointed rotation")
	promptBudget := flag.Int("prompt-budget", 9000, "estimated prompt token ceiling before compact cold resume; 0 disables")
	autoCommit := flag.Bool("auto-commit", false, "manage a local project Git baseline and commit verified task changes after all gates")
	verifyGoTags := flag.String("verify-go-tags", "", "optional additional required go test tag (e.g. dexster_extra), after normal tests and before DONE")
	verifyMode := flag.String("verify-mode", "auto", "verification: auto (legacy) or http_server")
	verifyHTTPURL := flag.String("verify-http-url", "http://127.0.0.1:8080/", "HTTP verification loopback URL")
	verifyHTTPContains := flag.String("verify-http-contains", "", "optional required text in HTTP response body (http_server mode)")
	autoVerify := flag.Bool("auto-verify", true, "run mandatory git_diff and tests automatically after write")
	maxIter := flag.Int("max-iterations", 20, "maximum model iterations per run")
	modelTrace := flag.Bool("model-trace", false, "emit dedicated model output events for DEXSTER Web")
	flag.Parse()

	if *task == "" {
		fmt.Fprintln(os.Stderr, `usage: dexster -root DIR -task "..." [-model qwen3:8b] [-ollama-url http://localhost:11434] [-max-iterations 20]`)
		os.Exit(2)
	}

	// Rollback is a controller/KRAKEN operation, never an LLM repair task.
	// Detect it before constructing a model client or Agent.
	if rollback, ok := memory.ParseRollbackIntent(*task); ok {
		if *krakenRoot == "" {
			fmt.Fprintln(os.Stderr, "dexster: rollback requires -kraken-root")
			os.Exit(2)
		}
		store, err := memory.New(*krakenRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dexster:", err)
			os.Exit(1)
		}
		lock, err := store.LockExclusive()
		if err != nil {
			fmt.Fprintln(os.Stderr, "dexster: KRAKEN concurrent access:", err)
			os.Exit(1)
		}
		defer lock.Close()
		plan, err := store.PreviewProjectRollback(*root, rollback.Mode)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dexster: rollback preview:", err)
			os.Exit(1)
		}
		fmt.Printf("[DEXSTER] ROLLBACK mode=%s strategy=%s target_task=%s\n", rollback.Mode, plan.Strategy, plan.TargetTaskID)
		result, err := store.ApplyProjectRollback(plan)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dexster: rollback:", err)
			os.Exit(1)
		}
		absRoot, _ := filepath.Abs(*root)
		_ = store.Record(memory.Event{Type: "project_rollback", Task: *task, Project: absRoot, Summary: fmt.Sprintf("mode=%s strategy=%s target=%s safety=%s", rollback.Mode, result.Strategy, result.TargetTaskID, result.SafetySnapshot), OK: true})
		fmt.Printf("[DEXSTER] ROLLBACK DONE restored=%d deleted=%d safety=%s\n", result.RestoredFiles, result.DeletedFiles, result.SafetySnapshot)
		if result.ArchivedState != "" {
			fmt.Printf("[DEXSTER] previous agent state archived=%s\n", result.ArchivedState)
		}
		fmt.Println("[DEXSTER] LLM USAGE calls=0 prompt_tokens=0 generated_tokens=0 total_tokens=0 peak_prompt_tokens=0 prompt_bytes=0")
		return
	}

	if *provider != "ollama" && *provider != "openai" && *provider != "llamacpp" && *provider != "unsloth" && *provider != "strata" {
		fmt.Fprintln(os.Stderr, "dexster: invalid provider")
		os.Exit(2)
	}
	if *autoCommit {
		if err := ensureManagedGit(*root); err != nil {
			fmt.Fprintln(os.Stderr, "dexster: Git baseline:", err)
			os.Exit(1)
		}
	}
	client := llm.New(*ollamaURL)
	client.Provider = *provider
	if *provider == "llamacpp" || *provider == "unsloth" || *provider == "strata" {
		client.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if *provider == "openai" {
		client.APIKey = os.Getenv("OPENAI_API_KEY")
		client.BaseURL = "https://api.openai.com/v1"
		if client.APIKey == "" {
			fmt.Fprintln(os.Stderr, "dexster: OPENAI_API_KEY is required")
			os.Exit(2)
		}
	}
	if *provider == "llamacpp" {
		client.BaseURL = os.Getenv("DEXSTER_LLAMACPP_URL")
		if client.BaseURL == "" {
			client.BaseURL = "http://127.0.0.1:8080/v1"
		}
		client.APIKey = os.Getenv("DEXSTER_LLAMACPP_API_KEY")
	}
	if *provider == "unsloth" {
		client.BaseURL = os.Getenv("DEXSTER_UNSLOTH_URL")
		if client.BaseURL == "" {
			fmt.Fprintln(os.Stderr, "dexster: set DEXSTER_UNSLOTH_URL to the running Unsloth OpenAI-compatible /v1 API endpoint")
			os.Exit(2)
		}
		client.APIKey = os.Getenv("DEXSTER_UNSLOTH_API_KEY")
	}
	if *provider == "strata" {
		client.BaseURL = strings.TrimSpace(*strataURL)
		if client.BaseURL == "" {
			client.BaseURL = strings.TrimSpace(os.Getenv("DEXSTER_STRATA_URL"))
		}
		if client.BaseURL == "" {
			fmt.Fprintln(os.Stderr, "dexster: set -strata-url or DEXSTER_STRATA_URL to running STRATA /v1 API (e.g. http://127.0.0.1:8081/v1)")
			os.Exit(2)
		}
		client.APIKey = os.Getenv("DEXSTER_STRATA_API_KEY")
	}
	client.NumCtx = *numCtx
	if *modelTrace {
		client.Trace = func(event, text string) {
			payload, err := json.Marshal(struct {
				Event string `json:"event"`
				Text  string `json:"text"`
			}{Event: event, Text: text})
			if err == nil {
				fmt.Printf("@@DEXSTER_MODEL@@%s\n", payload)
			}
		}
	}
	a, err := agent.New(*root, *model, client, *maxIter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dexster:", err)
		os.Exit(1)
	}

	a.MaxPromptTokens = *promptBudget
	a.RotateAtTokens = *rotateAt
	if a.RotateAtTokens == 0 && *numCtx > 0 {
		a.RotateAtTokens = *numCtx * 85 / 100
	}
	a.UnloadOnRotate = *unloadOnRotate
	a.AutoVerify = *autoVerify
	a.Router.VerifyMode = *verifyMode
	a.Router.VerifyHTTPURL = *verifyHTTPURL
	a.Router.VerifyHTTPContains = *verifyHTTPContains
	a.AutoCommit = *autoCommit
	a.VerifyGoTags = *verifyGoTags
	a.Protected = protected
	a.WriteScope = writeScope
	if *krakenRoot != "" {
		store, err := memory.New(*krakenRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dexster:", err)
			os.Exit(1)
		}
		a.Memory = store
	}

	fmt.Printf("[DEXSTER] root=%s model=%s max-iterations=%d\n", a.Root, *model, *maxIter)
	if err := a.Run(*task); err != nil {
		fmt.Fprintln(os.Stderr, "dexster:", err)
		os.Exit(1)
	}
}
