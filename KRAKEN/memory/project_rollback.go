package memory

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ProjectRollbackPlan struct {
	Project      string
	Mode         ProjectRollbackMode
	Strategy     string
	TargetTaskID string
	TargetTask   string
	Verified     *VerifiedState
	LegacyTasks  []LegacyRollbackTask
}

type LegacyRollbackTask struct {
	TaskID      string
	Task        string
	Checkpoints []string
	Done        bool
	Updated     time.Time
}

type ProjectRollbackResult struct {
	Strategy       string
	Mode           ProjectRollbackMode
	TargetTaskID   string
	TargetTask     string
	SafetySnapshot string
	RestoredFiles  int
	DeletedFiles   int
	ArchivedState  string
}

func manifestOrderTime(m TaskManifest) time.Time {
	if !m.Created.IsZero() {
		return m.Created
	}
	return m.Updated
}

func (s *Store) projectManifests(project string) ([]TaskManifest, error) {
	if s == nil {
		return nil, errors.New("kraken: nil store")
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	var files []string
	taskDir := filepath.Join(s.Root, "tasks")
	if entries, err := os.ReadDir(taskDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			files = append(files, filepath.Join(taskDir, e.Name()))
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Fresh runs with the same project+prompt archive the prior manifest here.
	// Include them so pre-v32 checkpoint chains remain available for recovery.
	runDir := filepath.Join(s.Root, "runs", ProjectID(abs))
	if entries, err := os.ReadDir(runDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			files = append(files, filepath.Join(runDir, e.Name()))
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var out []TaskManifest
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var m TaskManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("kraken: invalid task manifest %s: %w", file, err)
		}
		if m.Version != 1 || m.ID == "" || m.Project == "" || m.Task == "" || m.ID != TaskID(m.Project, m.Task) {
			return nil, fmt.Errorf("kraken: invalid task identity %s", file)
		}
		manifestProject, err := filepath.Abs(m.Project)
		if err != nil {
			return nil, err
		}
		if manifestProject == abs {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := manifestOrderTime(out[i]), manifestOrderTime(out[j])
		if a.Equal(b) {
			return out[i].Updated.Before(out[j].Updated)
		}
		return a.Before(b)
	})
	return out, nil
}

func (s *Store) verifiedHistory(project string, manifests []TaskManifest) ([]TaskManifest, error) {
	var out []TaskManifest
	for _, m := range manifests {
		if !m.Done || m.Verified == nil {
			continue
		}
		if err := s.validateVerifiedState(project, *m.Verified); err != nil {
			return nil, fmt.Errorf("kraken: verified state %s: %w", m.ID, err)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Verified.Created, out[j].Verified.Created
		if a.Equal(b) {
			return out[i].Updated.Before(out[j].Updated)
		}
		return a.Before(b)
	})
	return out, nil
}

// PreviewProjectRollback chooses a KRAKEN-owned verified state when available.
// For pre-v32 history it falls back to reversing existing armed checkpoint
// chains; no LLM is involved in either path.
func (s *Store) PreviewProjectRollback(project string, mode ProjectRollbackMode) (ProjectRollbackPlan, error) {
	var plan ProjectRollbackPlan
	if mode != RollbackPrevious && mode != RollbackFirst {
		return plan, errors.New("kraken: invalid project rollback mode")
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return plan, err
	}
	plan.Project, plan.Mode = abs, mode

	// Prefer KRAKEN's project-level history. Unlike task manifests this history
	// survives new prompts, cleared local agent state, and a completely emptied
	// project directory reused for another generation.
	points, err := s.VerifiedProjectPoints(abs)
	if err != nil {
		return plan, err
	}
	if len(points) > 0 {
		var chosen *ProjectPoint
		if mode == RollbackFirst {
			chosen = &points[0]
		} else {
			files, err := sourceFiles(abs)
			if err != nil {
				return plan, err
			}
			currentTree, err := treeDigest(abs, files)
			if err != nil {
				return plan, err
			}
			for i := len(points) - 1; i >= 0; i-- {
				if points[i].Snapshot.TreeSHA256 == currentTree {
					continue
				}
				chosen = &points[i]
				break
			}
			if chosen == nil {
				return plan, errors.New("kraken: no earlier verified project point")
			}
		}
		v := chosen.Snapshot
		plan.Strategy = "project_snapshot"
		plan.TargetTaskID = chosen.RunID
		if plan.TargetTaskID == "" {
			plan.TargetTaskID = chosen.ID
		}
		plan.TargetTask = chosen.Task
		plan.Verified = &v
		return plan, nil
	}

	manifests, err := s.projectManifests(abs)
	if err != nil {
		return plan, err
	}
	if len(manifests) == 0 {
		return plan, errors.New("kraken: no task history for this project")
	}

	verified, err := s.verifiedHistory(abs, manifests)
	if err != nil {
		return plan, err
	}
	if len(verified) > 0 {
		var chosen *TaskManifest
		if mode == RollbackFirst {
			chosen = &verified[0]
		} else {
			files, err := sourceFiles(abs)
			if err != nil {
				return plan, err
			}
			currentTree, err := treeDigest(abs, files)
			if err != nil {
				return plan, err
			}
			for i := len(verified) - 1; i >= 0; i-- {
				// If the working tree is exactly the latest DONE snapshot, rollback
				// means go to the previous verified state, not a no-op.
				if verified[i].Verified.TreeSHA256 == currentTree {
					continue
				}
				chosen = &verified[i]
				break
			}
			if chosen == nil {
				return plan, errors.New("kraken: no earlier verified working state")
			}
		}
		v := *chosen.Verified
		plan.Strategy = "verified_snapshot"
		plan.TargetTaskID = chosen.ID
		plan.TargetTask = chosen.Task
		plan.Verified = &v
		return plan, nil
	}

	// Legacy path: reverse the pre-write snapshots already stored by KRAKEN.
	plan.Strategy = "legacy_checkpoint_chain"
	if mode == RollbackPrevious {
		var lastMismatch error
		for i := len(manifests) - 1; i >= 0; i-- {
			if len(manifests[i].Checkpoints) == 0 {
				continue
			}
			m := manifests[i]
			candidate := plan
			candidate.TargetTaskID = m.ID
			candidate.TargetTask = m.Task
			candidate.LegacyTasks = []LegacyRollbackTask{{TaskID: m.ID, Task: m.Task, Checkpoints: append([]string(nil), m.Checkpoints...), Done: m.Done, Updated: m.Updated}}
			if err := s.preflightLegacyRollback(candidate); err == nil {
				return candidate, nil
			} else if strings.Contains(err.Error(), "legacy rollback chain mismatch") {
				lastMismatch = err
				continue
			} else {
				return ProjectRollbackPlan{}, err
			}
		}
		if lastMismatch != nil {
			return plan, fmt.Errorf("kraken: no earlier applicable checkpoint chain: %w", lastMismatch)
		}
		return plan, errors.New("kraken: no rollback checkpoints for this project")
	}

	firstDone := -1
	for i, m := range manifests {
		if m.Done {
			firstDone = i
			plan.TargetTaskID = m.ID
			plan.TargetTask = m.Task
			break
		}
	}
	if firstDone < 0 {
		return plan, errors.New("kraken: no completed working task to restore")
	}
	for i := len(manifests) - 1; i > firstDone; i-- {
		m := manifests[i]
		if len(m.Checkpoints) == 0 {
			continue
		}
		step := LegacyRollbackTask{TaskID: m.ID, Task: m.Task, Checkpoints: append([]string(nil), m.Checkpoints...), Done: m.Done, Updated: m.Updated}
		candidate := plan
		candidate.LegacyTasks = append(append([]LegacyRollbackTask(nil), plan.LegacyTasks...), step)
		if err := s.preflightLegacyRollback(candidate); err == nil {
			plan.LegacyTasks = candidate.LegacyTasks
			continue
		} else if strings.Contains(err.Error(), "legacy rollback chain mismatch") {
			// This later task is already rolled back in the current tree; skip it
			// and keep walking toward the first completed working task.
			continue
		} else {
			return ProjectRollbackPlan{}, err
		}
	}
	if len(plan.LegacyTasks) == 0 {
		return plan, errors.New("kraken: project is already at the first recorded working task or legacy history is insufficient")
	}
	if err := s.preflightLegacyRollback(plan); err != nil {
		return ProjectRollbackPlan{}, err
	}
	return plan, nil

}

type simulatedFile struct {
	exists bool
	hash   string
}

func (s *Store) preflightLegacyRollback(plan ProjectRollbackPlan) error {
	state := map[string]simulatedFile{}
	for _, task := range plan.LegacyTasks {
		for i := len(task.Checkpoints) - 1; i >= 0; i-- {
			cp, err := s.loadCheckpoint(task.Checkpoints[i], plan.Project)
			if err != nil {
				return fmt.Errorf("kraken: task %s checkpoint: %w", task.TaskID, err)
			}
			cur, ok := state[cp.Path]
			if !ok {
				target, err := targetPath(plan.Project, cp.Path)
				if err != nil {
					return err
				}
				raw, err := os.ReadFile(target)
				if err != nil {
					return fmt.Errorf("kraken: rollback target %s: %w", cp.Path, err)
				}
				cur = simulatedFile{exists: true, hash: digest(raw)}
			}
			if !cur.exists || cur.hash != cp.AfterSHA256 {
				return fmt.Errorf("kraken: legacy rollback chain mismatch at %s; current state is not the recorded task result", cp.Path)
			}
			if cp.Existed {
				state[cp.Path] = simulatedFile{exists: true, hash: cp.SHA256}
			} else {
				state[cp.Path] = simulatedFile{}
			}
		}
	}
	return nil
}

func extractVerifiedArchive(v VerifiedState, dest string) (map[string]os.FileMode, error) {
	f, err := os.Open(v.Snapshot)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]os.FileMode{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rel := filepath.Clean(filepath.FromSlash(h.Name))
		if !snapshotPathAllowed(rel) {
			return nil, fmt.Errorf("kraken: unsafe verified archive path %q", h.Name)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("kraken: unsupported verified archive entry %s", h.Name)
		}
		target := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
		if err != nil {
			return nil, err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		files[rel] = os.FileMode(h.Mode) & 0777
	}
	return files, nil
}

func copyAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".kraken-verified-restore-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode.Perm()); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (s *Store) restoreVerifiedArchive(project string, v VerifiedState) (int, int, error) {
	if err := s.validateVerifiedState(project, v); err != nil {
		return 0, 0, err
	}
	tmp, err := os.MkdirTemp("", "kraken-verified-restore-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	targetFiles, err := extractVerifiedArchive(v, tmp)
	if err != nil {
		return 0, 0, err
	}
	files, err := sourceFiles(tmp)
	if err != nil {
		return 0, 0, err
	}
	tree, err := treeDigest(tmp, files)
	if err != nil {
		return 0, 0, err
	}
	if tree != v.TreeSHA256 {
		return 0, 0, errors.New("kraken: verified snapshot tree hash mismatch")
	}
	current, err := sourceFiles(project)
	if err != nil {
		return 0, 0, err
	}
	deleted := 0
	for _, rel := range current {
		if _, keep := targetFiles[rel]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(project, rel)); err != nil {
			return 0, deleted, err
		}
		deleted++
	}
	restored := 0
	for _, rel := range files {
		if err := copyAtomic(filepath.Join(tmp, rel), filepath.Join(project, rel), targetFiles[rel]); err != nil {
			return restored, deleted, err
		}
		restored++
	}
	finalFiles, err := sourceFiles(project)
	if err != nil {
		return restored, deleted, err
	}
	finalTree, err := treeDigest(project, finalFiles)
	if err != nil {
		return restored, deleted, err
	}
	if finalTree != v.TreeSHA256 {
		return restored, deleted, errors.New("kraken: restored project does not match verified tree")
	}
	return restored, deleted, nil
}

func archiveAgentState(project string) (string, error) {
	state := filepath.Join(project, ".dexster", "state.json")
	if _, err := os.Lstat(state); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	dest := state + ".rollback-" + stamp
	if err := os.Rename(state, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// ApplyProjectRollback restores a selected working state without invoking an
// LLM. A full KRAKEN safety snapshot is captured first; if application fails,
// KRAKEN attempts to restore that safety snapshot before returning the error.
func (s *Store) ApplyProjectRollback(plan ProjectRollbackPlan) (ProjectRollbackResult, error) {
	var result ProjectRollbackResult
	result.Strategy, result.Mode, result.TargetTaskID, result.TargetTask = plan.Strategy, plan.Mode, plan.TargetTaskID, plan.TargetTask
	latest, err := s.PreviewProjectRollback(plan.Project, plan.Mode)
	if err != nil {
		return result, err
	}
	if latest.Strategy != plan.Strategy || latest.TargetTaskID != plan.TargetTaskID {
		return result, errors.New("kraken: rollback history changed; preview again")
	}
	safetyPoint, err := s.CaptureProjectPoint(plan.Project, "rollback_safety", "kraken", plan.TargetTask, "", 0, false)
	if err != nil {
		return result, fmt.Errorf("kraken: safety snapshot: %w", err)
	}
	safety := safetyPoint.Snapshot
	result.SafetySnapshot = safety.Snapshot
	rollbackErr := func() error {
		if plan.Strategy == "verified_snapshot" || plan.Strategy == "project_snapshot" {
			if plan.Verified == nil {
				return errors.New("kraken: missing verified rollback target")
			}
			r, d, err := s.restoreVerifiedArchive(plan.Project, *plan.Verified)
			result.RestoredFiles, result.DeletedFiles = r, d
			return err
		}
		if plan.Strategy != "legacy_checkpoint_chain" {
			return errors.New("kraken: unknown rollback strategy")
		}
		if err := s.preflightLegacyRollback(plan); err != nil {
			return err
		}
		for _, task := range plan.LegacyTasks {
			for i := len(task.Checkpoints) - 1; i >= 0; i-- {
				cp, err := s.loadCheckpoint(task.Checkpoints[i], plan.Project)
				if err != nil {
					return err
				}
				if err := s.ApplyRollback(task.Checkpoints[i], plan.Project); err != nil {
					return err
				}
				result.RestoredFiles++
				if !cp.Existed {
					result.DeletedFiles++
				}
			}
		}
		return nil
	}()
	if rollbackErr != nil {
		_, _, safetyErr := s.restoreVerifiedArchive(plan.Project, safety)
		if safetyErr != nil {
			return result, fmt.Errorf("kraken: rollback failed: %v; SAFETY RESTORE ALSO FAILED: %w", rollbackErr, safetyErr)
		}
		return result, fmt.Errorf("kraken: rollback failed and original state was restored from safety snapshot: %w", rollbackErr)
	}
	archived, err := archiveAgentState(plan.Project)
	if err != nil {
		return result, fmt.Errorf("kraken: rollback applied but agent state archive failed: %w", err)
	}
	result.ArchivedState = archived
	return result, nil
}
