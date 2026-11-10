package agent

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// commitVerifiedChanges commits only successful write_file paths of this task.
// It is opt-in and only called after all completion gates passed.  A task
// hash trailer detects a successful commit if the process dies before DONE
// is persisted. This is a local Git commit; nothing is pushed.
func (a *Agent) commitVerifiedChanges(task string) (string, error) {
	if a.State.GitCommit != "" {
		return a.State.GitCommit, nil
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.Root
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	root, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("auto-commit requires a Git repository: %w", err)
	}
	if filepath.Clean(root) != filepath.Clean(a.Root) {
		return "", fmt.Errorf("auto-commit requires project root to equal Git top-level; got %s", root)
	}
	// Only task-successful writes are eligible; no patterns or model-provided shell.
	pathsMap := map[string]bool{}
	for _, rec := range a.State.Journal {
		if (rec.Tool == "write_file" || rec.Tool == "make_dir" || rec.Tool == "copy_file" || rec.Tool == "move_file" || rec.Tool == "delete_path") && rec.OK {
			p := filepath.Clean(rec.Path)
			if p == "." || filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("unsafe Git path: %s", rec.Path)
			}
			pathsMap[p] = true
			if rec.Tool == "move_file" && rec.Source != "" {
				src := filepath.Clean(rec.Source)
				if src == "." || filepath.IsAbs(src) || src == ".." || strings.HasPrefix(src, ".."+string(filepath.Separator)) {
					return "", fmt.Errorf("unsafe Git source path: %s", rec.Source)
				}
				pathsMap[src] = true
			}
		}
	}
	paths := make([]string, 0, len(pathsMap))
	for p := range pathsMap {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return "", fmt.Errorf("no successful writes to commit")
	}
	headMessage, _ := run("log", "-1", "--format=%B")
	trailer := "DEXSTER-Task-SHA256: " + taskHash(task)
	// Crash recovery: if the previous run committed but died before state write,
	// HEAD identifies the exact task and affected path list.
	marker := "DEXSTER-Paths: " + strings.Join(paths, ",")
	if containsExactLine(headMessage, trailer) && containsExactLine(headMessage, marker) {
		hash, err := run("rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		return hash, nil
	}
	staged, err := run("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return "", err
	}
	if staged != "" {
		return "", fmt.Errorf("Git index already contains staged changes; refusing to commit unrelated work")
	}
	// Require all paths under the requested project and no symlinks.
	for _, p := range paths {
		if err := a.Router.ValidatePath(p); err != nil {
			return "", err
		}
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err = run(args...); err != nil {
		return "", err
	}
	// Always unstage only our paths on any failure. Never reset the worktree.
	committed := false
	defer func() {
		if !committed {
			_, _ = run(append([]string{"reset", "-q", "--"}, paths...)...)
		}
	}()
	stagedNames, err := run("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{}
	for _, p := range paths {
		allowed[p] = true
	}
	count := 0
	for _, p := range strings.Split(stagedNames, "\x00") {
		if p == "" {
			continue
		}
		count++
		if !allowed[p] {
			return "", fmt.Errorf("unexpected staged file: %s", p)
		}
	}
	if count == 0 {
		return "", fmt.Errorf("no Git changes for task paths; refusing empty commit")
	}
	message := fmt.Sprintf("DEXSTER: verified task %s\n\n%s\n%s\n", taskHash(task)[:12], trailer, marker)
	cmd := exec.Command("git", "-c", "commit.gpgsign=false", "commit", "-m", message)
	cmd.Dir = a.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit: %s: %w", bytes.TrimSpace(out), err)
	}
	committed = true
	hash, err := run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return hash, nil
}

func containsExactLine(message, line string) bool {
	for _, candidate := range strings.Split(message, "\n") {
		if candidate == line {
			return true
		}
	}
	return false
}
