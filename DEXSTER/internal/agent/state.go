package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dexster/internal/constraint"
)

// ActionRecord is one journal entry: a tool execution or a parse rejection.
type ActionRecord struct {
	Iteration  int    `json:"iteration"`
	Tool       string `json:"tool"`
	Path       string `json:"path,omitempty"`
	Source     string `json:"source,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Summary    string `json:"summary,omitempty"`
	OK         bool   `json:"ok"`
	Exit       int    `json:"exit"`
}

// Evidence is the completion-gate state: what has been verified and when.
// Iteration numbers make ordering explicit (diff/test must follow the last
// write), which replaces bratan-go's rigid FSM transitions with a simple,
// auditable policy.
type Evidence struct {
	Inspected          bool              `json:"inspected"`
	ReadFiles          map[string]string `json:"read_files"` // path -> content (trusted CAS snapshot)
	WrotePath          string            `json:"wrote_path,omitempty"`
	LastWriteIter      int               `json:"last_write_iter"`
	LastDiffIter       int               `json:"last_diff_iter"`
	LastTestIter       int               `json:"last_test_iter"`
	LastHTMLVerifyIter int               `json:"last_html_verify_iter,omitempty"`
	LastReviewIter     int               `json:"last_review_iter,omitempty"`
	LastExtraIter      int               `json:"last_extra_iter,omitempty"`
	DeclaredIter       int               `json:"declared_iter"`
}

// UsageMetrics stores Ollama-reported token usage across interrupted runs.
// Zero counters from test mocks are allowed; estimates never masquerade as real tokens.
type UsageMetrics struct {
	Requests             int   `json:"requests"`
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	PromptBytes          int64 `json:"prompt_bytes"`
	PeakPromptTokens     int   `json:"peak_prompt_tokens"`
	PeakPromptBytes      int   `json:"peak_prompt_bytes"`
	LastPromptTokens     int   `json:"last_prompt_tokens"`
	LastCompletionTokens int   `json:"last_completion_tokens"`
}

// TaskState is the persisted, crash-safe run state.
type TaskState struct {
	Version               int               `json:"version"`
	Task                  string            `json:"task"`
	TaskHash              string            `json:"task_hash"`
	Iteration             int               `json:"iteration"`
	Done                  bool              `json:"done"`
	GitCommit             string            `json:"git_commit,omitempty"`
	Usage                 UsageMetrics      `json:"usage,omitempty"`
	VerifyGoTags          string            `json:"verify_go_tags,omitempty"`
	WriteScope            []string          `json:"write_scope,omitempty"`
	TaskMode              string            `json:"task_mode,omitempty"`
	PlanStage             string            `json:"plan_stage,omitempty"`
	ContextCompact        bool              `json:"context_compact,omitempty"`
	ContextEpoch          int               `json:"context_epoch,omitempty"`
	LastRotationIteration int               `json:"last_rotation_iteration,omitempty"`
	ReviewBaselines       map[string]string `json:"review_baselines,omitempty"` // original HTML; never replaced by later reads
	Evidence              Evidence          `json:"evidence"`
	Journal               []ActionRecord    `json:"journal"`
	Constraints           []constraint.Rule `json:"constraints,omitempty"`
	ProtectedTests        []string          `json:"protected_tests,omitempty"` // test files frozen at original task start
}

const stateVersion = 1

func taskHash(task string) string {
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:])
}

func stateDir(root string) string  { return filepath.Join(root, ".dexster") }
func statePath(root string) string { return filepath.Join(stateDir(root), "state.json") }

// saveState persists the run state atomically (temp file + rename).
func saveState(root string, st *TaskState) error {
	dir := stateDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("dexster: create state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("dexster: encode state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-tmp-*")
	if err != nil {
		return fmt.Errorf("dexster: create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("dexster: write temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("dexster: sync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("dexster: close temp state: %w", err)
	}
	if err := os.Rename(tmpName, statePath(root)); err != nil {
		return fmt.Errorf("dexster: rename state into place: %w", err)
	}
	return nil
}

// loadState restores the persisted run state for task.
//
// Returns (state, resumed, error):
//   - no state file            -> nil, false, nil  (fresh start)
//   - corrupt JSON             -> quarantined to state.json.corrupt-<ts>, fresh start
//   - different task hash      -> quarantined to state.json.stale-<ts>, fresh start
//   - matching, not done       -> restored, true, nil
func loadState(root, task string) (*TaskState, bool, error) {
	data, err := os.ReadFile(statePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("dexster: read state: %w", err)
	}

	var st TaskState
	if err := json.Unmarshal(data, &st); err != nil {
		if qerr := quarantine(root, "corrupt"); qerr != nil {
			fmt.Printf("[DEXSTER] warning: cannot quarantine corrupt state: %v\n", qerr)
		} else {
			fmt.Println("[DEXSTER] state file was corrupt; quarantined and starting fresh")
		}
		return nil, false, nil
	}

	if st.TaskHash != taskHash(task) || st.Task != task {
		if qerr := quarantine(root, "stale"); qerr != nil {
			fmt.Printf("[DEXSTER] warning: cannot quarantine stale state: %v\n", qerr)
		} else {
			fmt.Println("[DEXSTER] state file belongs to a different task; quarantined and starting fresh")
		}
		return nil, false, nil
	}

	if st.ReviewBaselines == nil {
		st.ReviewBaselines = map[string]string{}
	}
	if st.Evidence.ReadFiles == nil {
		st.Evidence.ReadFiles = map[string]string{}
	}
	return &st, true, nil
}

func quarantine(root, kind string) error {
	suffix := time.Now().Format("20060102-150405")
	target := fmt.Sprintf("%s.%s-%s", statePath(root), kind, suffix)
	if err := os.Rename(statePath(root), target); err != nil {
		return err
	}
	fmt.Printf("[DEXSTER] quarantined %s state to %s\n", kind, filepath.Base(target))
	return nil
}
