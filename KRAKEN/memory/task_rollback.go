package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// TaskRollbackPlan is a complete, read-only preflight over multiple checkpoints.
// An apply is intentionally NOT an atomic filesystem transaction: an I/O failure
// during application can leave some paths restored. No git reset is performed.
type TaskRollbackPlan struct{ Files []RollbackPlan }

type taskEntry struct {
	checkpoint Checkpoint
	plan       RollbackPlan
}

func (s *Store) taskPreflight(files []string, project string) ([]taskEntry, error) {
	if len(files) == 0 {
		return nil, errors.New("empty checkpoint set")
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	entries := make([]taskEntry, 0, len(files))
	for _, file := range files {
		cp, err := s.loadCheckpoint(file, abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if seen[cp.Path] {
			return nil, fmt.Errorf("duplicate checkpoint path %q", cp.Path)
		}
		seen[cp.Path] = true
		plan, err := s.PreviewRollback(file, abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cp.Path, err)
		}
		entries = append(entries, taskEntry{cp, plan})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].plan.Path < entries[j].plan.Path })
	return entries, nil
}

// PreviewTaskRollback validates ALL paths before permitting any modification.
func (s *Store) PreviewTaskRollback(files []string, project string) (TaskRollbackPlan, error) {
	entries, err := s.taskPreflight(files, project)
	if err != nil {
		return TaskRollbackPlan{}, err
	}
	plan := TaskRollbackPlan{Files: make([]RollbackPlan, 0, len(entries))}
	for _, e := range entries {
		plan.Files = append(plan.Files, e.plan)
	}
	return plan, nil
}

// ApplyTaskRollback first validates the complete set. It uses the existing
// guarded single-file restore for each item. Partial I/O failure is reported;
// callers must not claim a transactional or crash-atomic multi-file rollback.
func (s *Store) ApplyTaskRollback(files []string, project string) error {
	entries, err := s.taskPreflight(files, project)
	if err != nil {
		return err
	}
	// Stage temp copies of all preimages first. This catches permissions/disk
	// errors before changing any existing source file.
	type staged struct {
		file string
		tmp  string
	}
	stagedFiles := make([]staged, 0, len(entries))
	defer func() {
		for _, f := range stagedFiles {
			if f.tmp != "" {
				_ = os.Remove(f.tmp)
			}
		}
	}()
	for _, e := range entries {
		if !e.checkpoint.Existed {
			stagedFiles = append(stagedFiles, staged{file: e.plan.Path})
			continue
		}
		target, err := targetPath(project, e.plan.Path)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(target), ".kraken-task-restore-*")
		if err != nil {
			return err
		}
		name := f.Name()
		if err = f.Chmod(0644); err == nil {
			_, err = f.Write(e.checkpoint.Content)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
			return err
		}
		stagedFiles = append(stagedFiles, staged{file: e.plan.Path, tmp: name})
	}
	// Recheck the entire set after staging, before the first mutation.
	if _, err = s.taskPreflight(files, project); err != nil {
		return err
	}
	for i, e := range entries {
		target, err := targetPath(project, e.plan.Path)
		if err != nil {
			return fmt.Errorf("partial rollback after %d files: %w", i, err)
		}
		b, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("partial rollback after %d files: %w", i, err)
		}
		if digest(b) != e.plan.ExpectedSHA256 {
			return fmt.Errorf("partial rollback after %d files: concurrent modification detected", i)
		}
		if e.plan.Created {
			err = os.Remove(target)
		} else {
			err = os.Rename(stagedFiles[i].tmp, target)
			if err == nil {
				stagedFiles[i].tmp = ""
			}
		}
		if err != nil {
			return fmt.Errorf("partial rollback after %d files: %w", i, err)
		}
	}
	return nil
}
