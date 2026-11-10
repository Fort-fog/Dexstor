package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dexster/internal/tools"
	"kraken/memory"
)

// seedReviewBaselinesFromKRAKEN is read-only. For interrupted tasks created
// before this controller version, use the first verified pre-write checkpoint
// as the immutable HTML reference. Never silently promote damaged current
// content into a new baseline after a crash.
func (a *Agent) seedReviewBaselinesFromKRAKEN() {
	if a.State == nil || a.Memory == nil {
		return
	}
	if a.State.ReviewBaselines == nil {
		a.State.ReviewBaselines = map[string]string{}
	}
	for _, action := range a.State.Journal {
		p := filepath.Clean(action.Path)
		if action.Tool != "write_file" || !action.OK || !strings.EqualFold(filepath.Ext(p), ".html") || action.Checkpoint == "" {
			continue
		}
		if _, exists := a.State.ReviewBaselines[p]; exists {
			continue
		}
		root := filepath.Join(a.Memory.Root, "checkpoints")
		if filepath.Clean(filepath.Dir(action.Checkpoint)) != filepath.Clean(root) {
			continue
		}
		data, err := os.ReadFile(action.Checkpoint)
		if err != nil || len(data) > 8<<20 {
			continue
		}
		var cp memory.Checkpoint
		if json.Unmarshal(data, &cp) != nil || !cp.Existed || cp.Project != a.Root || cp.Path != p || cp.AfterSHA256 == "" {
			continue
		}
		digest := sha256.Sum256(cp.Content)
		if cp.SHA256 != hex.EncodeToString(digest[:]) {
			continue
		}
		a.State.ReviewBaselines[p] = string(cp.Content)
	}
}

// needsControllerReview is intentionally domain-agnostic. Any changed HTML is
// reviewed for syntax and preservation; there is no task-specific domain path.
func (a *Agent) needsControllerReview() bool {
	return a.State != nil && a.latestHTMLMutationIter() > 0
}

// reviewTask is a deterministic post-tool quality gate. It validates current
// HTML and preservation against the trusted baseline without task-specific domain heuristics.
func (a *Agent) reviewTask() tools.Result {
	if a.State == nil {
		return failResult("review_task", "", "missing task state")
	}
	paths := map[string]bool{}
	for _, action := range a.State.Journal {
		if action.OK && isMutationTool(action.Tool) && strings.EqualFold(filepath.Ext(action.Path), ".html") {
			paths[action.Path] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	for _, path := range ordered {
		current := a.Router.Read(path)
		if !current.OK {
			return failResult("review_task", path, "cannot read current HTML: "+current.Error)
		}
		result := a.Router.ValidateHTML(path)
		if !result.OK {
			return failResult("review_task", path, result.Error)
		}
	}
	return tools.Result{Tool: "review_task", Output: "PASS: HTML syntax and local asset checks", OK: true, Exit: 0}
}
