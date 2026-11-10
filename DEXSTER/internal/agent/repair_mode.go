package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dexster/internal/llm"
)

const (
	taskModeBuild  = "build_new"
	taskModeRepair = "repair_existing"
)

var repairMutationWords = []string{
	"исправ", "почин", "ремонт", "замен", "измени", "изменить", "обнов", "доработ",
	"удал", "убер", "встав", "добав", "подключ", "перенес", "перемест", "переимен",
	"fix", "repair", "replace", "modify", "update", "remove", "delete", "insert", "add ",
	"connect", "wire", "rename", "move ", "change ",
}

var strongBuildWords = []string{
	"с нуля", "новый проект", "новое приложение", "новый сайт", "создай проект", "создай сайт",
	"создай приложение", "create a new project", "create a new site", "create a new app", "from scratch",
	"build a new project", "build a new site", "build a new app",
}

func containsAnyFold(s string, needles []string) bool {
	s = strings.ToLower(s)
	for _, needle := range needles {
		if strings.Contains(s, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// classifyTaskMode is deliberately domain-agnostic. Existing source plus a
// mutation request means repair, whether the project is HTML, Go, Python,
// configuration, data files, assets, or something else. Strong "new/from
// scratch" wording wins only when the task actually asks to replace the
// project rather than alter existing source.
func (a *Agent) classifyTaskMode(task string) string {
	if !a.hasExistingProjectSource() {
		return taskModeBuild
	}
	lower := strings.ToLower(task)
	if strings.Contains(lower, "существующ") || strings.Contains(lower, "existing") {
		return taskModeRepair
	}
	if containsAnyFold(task, repairMutationWords) {
		return taskModeRepair
	}
	if containsAnyFold(task, strongBuildWords) {
		return taskModeBuild
	}
	// A structured Planner contract with a TARGET/ACTION over a non-empty
	// project is an incremental task unless it explicitly asks for a new build.
	if strings.Contains(lower, "target:") && strings.Contains(lower, "action:") {
		return taskModeRepair
	}
	return taskModeBuild
}

func (a *Agent) hasExistingProjectSource() bool {
	entries, err := os.ReadDir(a.Root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".git" || name == ".kraken" || strings.HasPrefix(name, ".dexster") {
			continue
		}
		// An empty assets/cache directory alone is not a built project.
		if e.IsDir() {
			if name == "assets" {
				children, _ := os.ReadDir(filepath.Join(a.Root, name))
				if len(children) == 0 {
					continue
				}
			}
			return true
		}
		return true
	}
	return false
}

func (a *Agent) repairMode() bool {
	return a.State != nil && a.State.TaskMode == taskModeRepair
}

// repairSourcePaths extracts only path-like values from SOURCE:. It never
// guesses domain concepts. Candidates are accepted later only when they exist
// on disk, so prose such as "current project" is harmless.
func repairSourcePaths(task string) []string {
	lower := strings.ToLower(task)
	idx := strings.Index(lower, "source:")
	if idx < 0 {
		return nil
	}
	source := task[idx+len("source:"):]
	end := len(source)
	for _, label := range []string{"\nTARGET:", "\nACTION:", "\nVERIFY:", "\nREPORT:"} {
		if i := strings.Index(strings.ToUpper(source), label); i >= 0 && i < end {
			end = i
		}
	}
	source = source[:end]

	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		raw = strings.Trim(raw, "`\"'()[]{}<>,;:.!?")
		if raw == "" || raw == "." || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, raw)
	}

	backtick := regexp.MustCompile("`([^`]+)`")
	for _, m := range backtick.FindAllStringSubmatch(source, -1) {
		add(m[1])
	}
	// Absolute paths or relative paths containing a slash. Bare filenames are
	// intentionally left to the model because SOURCE prose often contains words
	// that look like filenames but are not authoritative paths.
	pathLike := regexp.MustCompile(`(?:/[A-Za-z0-9._~@%+,:=-]+)+(?:/)?|(?:[A-Za-z0-9._~@%+=:-]+/)+(?:[A-Za-z0-9._~@%+=:-]+)?`)
	for _, m := range pathLike.FindAllString(source, -1) {
		add(m)
	}
	sort.Strings(out)
	return out
}

func (a *Agent) sourceAlreadyInspected(path string) bool {
	if a.State == nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, rec := range a.State.Journal {
		if rec.OK && rec.Tool == "list_files" && filepath.Clean(rec.Path) == clean {
			return true
		}
	}
	return false
}

// nextRepairPreflight deterministically inspects an explicitly named SOURCE
// directory before spending a model call. This implements SOURCE -> TARGET ->
// ACTION ordering without hard-coding websites, languages, or asset types.
func (a *Agent) nextRepairPreflight() (llm.Call, bool) {
	if !a.repairMode() {
		return llm.Call{}, false
	}
	for _, candidate := range repairSourcePaths(a.State.Task) {
		if a.sourceAlreadyInspected(candidate) {
			continue
		}
		path := candidate
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(a.Root, path)
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		return llm.Call{Name: "list_files", Args: map[string]string{"path": path}}, true
	}
	return llm.Call{}, false
}

// repairReplaceFailures counts exact-edit failures for a target since the most
// recent successful mutation of that target. Successful reads do not reset the
// budget; this is what prevents read -> replace FAIL -> read -> replace FAIL
// from bypassing the repair policy.
func (a *Agent) repairReplaceFailures(path string) int {
	if a.State == nil || path == "" {
		return 0
	}
	path = filepath.Clean(path)
	count := 0
	for i := len(a.State.Journal) - 1; i >= 0; i-- {
		rec := a.State.Journal[i]
		if filepath.Clean(rec.Path) != path {
			continue
		}
		if rec.OK && isMutationTool(rec.Tool) {
			break
		}
		if !rec.OK && rec.Tool == "replace_in_file" {
			count++
		}
	}
	return count
}

func (a *Agent) repairMutationFailures(tool, path string) int {
	if a.State == nil || tool == "" || path == "" {
		return 0
	}
	path = filepath.Clean(path)
	count := 0
	for i := len(a.State.Journal) - 1; i >= 0; i-- {
		rec := a.State.Journal[i]
		if a.casFuseResetThrough > 0 && rec.Iteration <= a.casFuseResetThrough {
			break
		}
		if rec.OK && isMutationTool(rec.Tool) {
			break
		}
		if !rec.OK && rec.Tool == tool && filepath.Clean(rec.Path) == path {
			count++
		}
	}
	return count
}

func (a *Agent) repairFallbackPath() string {
	if !a.repairMode() || a.State == nil {
		return ""
	}
	seen := map[string]bool{}
	for i := len(a.State.Journal) - 1; i >= 0; i-- {
		rec := a.State.Journal[i]
		if rec.Path == "" {
			continue
		}
		p := filepath.Clean(rec.Path)
		if seen[p] {
			continue
		}
		seen[p] = true
		if a.repairReplaceFailures(p) >= 2 {
			return p
		}
	}
	return ""
}

func (a *Agent) repairPolicyText() string {
	if !a.repairMode() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nREPAIR MODULE: REPAIR_EXISTING is active. Preserve unrelated working behavior. Follow SOURCE -> TARGET -> ACTION -> VERIFY -> REPORT. Do not rebuild the project just to make a small repair.\n")
	if paths := repairSourcePaths(a.State.Task); len(paths) > 0 {
		fmt.Fprintf(&b, "REPAIR SOURCE candidates from the task: %s. Use actual tool results; never invent missing files.\n", strings.Join(paths, ", "))
	}
	if p := a.repairFallbackPath(); p != "" {
		fmt.Fprintf(&b, "REPAIR FALLBACK REQUIRED for %s: replace_in_file has already failed twice for this target. Do NOT call replace_in_file again and do NOT reread an unchanged trusted snapshot. Produce one complete corrected write_file for %s using the trusted read shown in this prompt, then let the controller verify it.\n", p, p)
	}
	return b.String()
}

func (a *Agent) repairReportFields() (mode string, sources []string) {
	if !a.repairMode() || a.State == nil {
		return "", nil
	}
	seen := map[string]bool{}
	for _, rec := range a.State.Journal {
		if rec.OK && rec.Tool == "copy_file" && rec.Source != "" && !seen[rec.Source] {
			seen[rec.Source] = true
			sources = append(sources, rec.Source)
		}
	}
	sort.Strings(sources)
	return taskModeRepair, sources
}
