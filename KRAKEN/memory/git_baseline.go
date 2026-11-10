package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitBaseline pins a clean repository commit under a local KRAKEN-only Git ref.
// It does not stage, commit, switch branches, or touch the working tree.
// Uncommitted/untracked files are deliberately rejected: HEAD does not hold them.
type GitBaseline struct {
	Ref    string
	Commit string
}

func gitRead(ctx context.Context, project string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", project}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// PinGitBaseline creates an immutable local reference for a task's initial HEAD.
// A repeated call for the same task returns the existing ref only if it still
// points at the same commit. Never force-updates an earlier task baseline.
func PinGitBaseline(project, task string) (GitBaseline, error) {
	if strings.TrimSpace(task) == "" {
		return GitBaseline{}, errors.New("task ID required")
	}
	project, err := filepath.Abs(project)
	if err != nil {
		return GitBaseline{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, err := gitRead(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitBaseline{}, err
	}
	if root != project {
		return GitBaseline{}, errors.New("project is not the Git worktree root")
	}
	dirty, err := gitRead(ctx, project, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return GitBaseline{}, err
	}
	if dirty != "" {
		return GitBaseline{}, errors.New("working tree is dirty: commit or back up changes before pinning a Git baseline")
	}
	head, err := gitRead(ctx, project, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return GitBaseline{}, err
	}
	hash := sha256.Sum256([]byte(task))
	ref := "refs/kraken/baselines/" + hex.EncodeToString(hash[:12])
	prior, err := gitRead(ctx, project, "rev-parse", "--verify", ref)
	if err == nil {
		if prior != head {
			return GitBaseline{}, fmt.Errorf("baseline already exists at %s (current HEAD differs)", prior)
		}
		return GitBaseline{Ref: ref, Commit: head}, nil
	}
	if _, err := gitRead(ctx, project, "update-ref", ref, head, strings.Repeat("0", 40)); err != nil {
		return GitBaseline{}, err
	}
	return GitBaseline{Ref: ref, Commit: head}, nil
}
