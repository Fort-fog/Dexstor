package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskManifest is the durable control record for one task. Writes use atomic
// replacement. The agent's .dexster/state.json remains the execution journal.
// The manifest records the last journal iteration acknowledged by KRAKEN.
type TaskManifest struct {
	Version               int            `json:"version"`
	Created               time.Time      `json:"created,omitempty"`
	ID                    string         `json:"id"`
	RunID                 string         `json:"run_id,omitempty"`
	StartPoint            string         `json:"start_point,omitempty"`
	Task                  string         `json:"task"`
	Project               string         `json:"project"`
	Baseline              GitBaseline    `json:"baseline"`
	BaselineStatus        string         `json:"baseline_status"`
	Checkpoints           []string       `json:"checkpoints"`
	Pending               *PendingWrite  `json:"pending,omitempty"`
	AcknowledgedIteration int            `json:"acknowledged_iteration"`
	Done                  bool           `json:"done"`
	Resume                *ResumeCapsule `json:"resume,omitempty"`
	Verified              *VerifiedState `json:"verified,omitempty"`
	Updated               time.Time      `json:"updated"`
}

// PendingWrite is persisted BEFORE the file mutation. It carries enough
// evidence to reconcile a crash without guessing whether the write happened.
type PendingWrite struct {
	Iteration   int    `json:"iteration"`
	Checkpoint  string `json:"checkpoint"`
	AfterSHA256 string `json:"after_sha256"`
}

func TaskID(project, task string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + task))
	return hex.EncodeToString(sum[:16])
}
func (s *Store) manifestPath(project, task string) (string, error) {
	if s == nil || strings.TrimSpace(project) == "" || strings.TrimSpace(task) == "" {
		return "", errors.New("missing store/project/task")
	}
	return filepath.Join(s.Root, "tasks", TaskID(project, task)+".json"), nil
}
func (s *Store) ReadTask(project, task string) (TaskManifest, error) {
	path, err := s.manifestPath(project, task)
	if err != nil {
		return TaskManifest{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return TaskManifest{}, err
	}
	if !info.Mode().IsRegular() {
		return TaskManifest{}, errors.New("manifest not regular")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return TaskManifest{}, err
	}
	var m TaskManifest
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Version != 1 || m.ID != TaskID(project, task) || m.Task != task || m.Project != project {
		return m, errors.New("manifest identity/version mismatch")
	}
	return m, nil
}
func (s *Store) writeTask(m TaskManifest) error {
	dir := filepath.Join(s.Root, "tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe tasks directory")
	}
	path, err := s.manifestPath(m.Project, m.Task)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("unsafe manifest target")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	m.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".task-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err == nil {
		defer d.Close()
		_ = d.Sync()
	}
	return nil
}

// newRunID identifies one execution attempt. TaskID intentionally remains the
// stable project+prompt key for compatibility; RunID distinguishes generations
// after the user clears a project or intentionally starts the same task anew.
func newRunID(project, task string, created time.Time) string {
	sum := sha256.Sum256([]byte(project + "\x00" + task + "\x00" + created.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) archiveTaskManifest(m TaskManifest) error {
	if s == nil || m.ID == "" || m.Project == "" || m.Task == "" {
		return errors.New("kraken: invalid task manifest archive")
	}
	dir := filepath.Join(s.Root, "runs", ProjectID(m.Project))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	run := m.RunID
	if run == "" {
		run = "legacy"
	}
	dest := filepath.Join(dir, m.ID+"-"+run+"-"+stamp+".json")
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	return err
}

func (s *Store) createTaskManifest(abs, task string) (TaskManifest, error) {
	created := time.Now().UTC()
	m := TaskManifest{Version: 1, Created: created, ID: TaskID(abs, task), RunID: newRunID(abs, task, created), Task: task, Project: abs, Checkpoints: []string{}}

	// KRAKEN's project point is captured before DEXSTER touches source or Git.
	// It remains available even if the user later empties this directory and
	// starts a completely different project at the same path.
	point, err := s.CaptureProjectPoint(abs, "pre_dexster", "dexster", task, m.RunID, 0, false)
	if err != nil {
		return TaskManifest{}, fmt.Errorf("kraken: project baseline snapshot: %w", err)
	}
	m.StartPoint = point.ID

	baseline, err := PinGitBaseline(abs, m.ID)
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(abs, ".git")); os.IsNotExist(statErr) {
			m.BaselineStatus = "no_git"
		} else {
			return TaskManifest{}, fmt.Errorf("cannot pin clean Git baseline: %w", err)
		}
	} else {
		m.Baseline = baseline
		m.BaselineStatus = "pinned"
	}
	if err := s.writeTask(m); err != nil {
		return TaskManifest{}, err
	}
	return m, nil
}

// BeginTask preserves the historical resume semantics used by older callers.
func (s *Store) BeginTask(project, task string) (TaskManifest, error) {
	return s.BeginTaskRun(project, task, false)
}

// BeginTaskRun starts a fresh run when fresh=true. If the same project+prompt
// had an older manifest, that manifest is archived rather than reused. This is
// what lets a user delete all project files, start again in the same directory,
// and still keep the old KRAKEN history without a journal-ahead collision.
func (s *Store) BeginTaskRun(project, task string, fresh bool) (TaskManifest, error) {
	abs, err := filepath.Abs(project)
	if err != nil {
		return TaskManifest{}, err
	}
	old, readErr := s.ReadTask(abs, task)
	if readErr == nil {
		if !fresh {
			return old, nil
		}
		// Migrate any verified snapshot produced by an earlier v32 build into
		// project history before rotating the active manifest.
		if old.Done && old.Verified != nil {
			if _, err := s.RegisterVerifiedProjectPoint(abs, old.Task, old.RunID, old.AcknowledgedIteration, *old.Verified); err != nil {
				return TaskManifest{}, fmt.Errorf("kraken: migrate previous verified run: %w", err)
			}
		}
		if err := s.archiveTaskManifest(old); err != nil {
			return TaskManifest{}, fmt.Errorf("kraken: archive previous run: %w", err)
		}
		return s.createTaskManifest(abs, task)
	}
	if !os.IsNotExist(readErr) {
		return TaskManifest{}, readErr
	}
	return s.createTaskManifest(abs, task)
}

// PrepareWrite registers an unarmed pre-write checkpoint and intended hash.
// It must be called after the snapshot exists but before writing the target.
func (s *Store) PrepareWrite(project, task string, iteration int, cpFile string, after []byte) error {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return err
	}
	if m.Pending != nil {
		return errors.New("unresolved pending write")
	}
	if iteration <= m.AcknowledgedIteration {
		return errors.New("write iteration already acknowledged")
	}
	root := filepath.Join(s.Root, "checkpoints")
	abs, err := filepath.Abs(cpFile)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) != root {
		return errors.New("checkpoint outside store")
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsafe checkpoint")
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return err
	}
	if cp.Project != project || cp.AfterSHA256 != "" {
		return errors.New("invalid pending checkpoint")
	}
	sum := digest(after)
	m.Pending = &PendingWrite{Iteration: iteration, Checkpoint: abs, AfterSHA256: sum}
	return s.writeTask(m)
}

// ReconcilePending returns "before" if the mutation did not happen, "after"
// if the exact intended bytes reached disk, or an error on any conflict.
// It never replaces files. When "after", it arms the checkpoint and records
// the reference, but the caller must still confirm its agent journal state.
func (s *Store) ReconcilePending(project, task string) (string, error) {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return "", err
	}
	if m.Pending == nil {
		return "none", nil
	}
	pending := m.Pending
	info, err := os.Lstat(pending.Checkpoint)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("unsafe pending checkpoint")
	}
	b, err := os.ReadFile(pending.Checkpoint)
	if err != nil {
		return "", err
	}
	var cp Checkpoint
	if err = json.Unmarshal(b, &cp); err != nil {
		return "", err
	}
	if cp.Project != project || cp.SHA256 != digest(cp.Content) {
		return "", errors.New("invalid pending preimage")
	}
	target, err := targetPath(project, cp.Path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(target)
	if os.IsNotExist(err) && !cp.Existed {
		m.Pending = nil
		return "before", s.writeTask(m)
	}
	if err != nil {
		return "", err
	}
	if digest(raw) == pending.AfterSHA256 {
		if cp.AfterSHA256 == "" {
			if err := s.ArmCheckpoint(pending.Checkpoint, raw); err != nil {
				return "", err
			}
		}
		seen := false
		for _, p := range m.Checkpoints {
			if p == pending.Checkpoint {
				seen = true
			}
		}
		if !seen {
			m.Checkpoints = append(m.Checkpoints, pending.Checkpoint)
		}
		m.Pending = nil
		if err := s.writeTask(m); err != nil {
			return "", err
		}
		return "after", nil
	}
	if cp.Existed && digest(raw) == cp.SHA256 {
		m.Pending = nil
		return "before", s.writeTask(m)
	}
	return "", errors.New("pending write conflicts with current file: manual recovery required")
}

// Acknowledge records a persisted agent-journal iteration and its checkpoint.
// Repeated calls for the same iteration are idempotent. A checkpoint may be
// supplied only when a successful write has already armed it.
func (s *Store) Acknowledge(project, task string, iteration int, checkpoint string, done bool) error {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return err
	}
	if m.Pending != nil {
		return errors.New("cannot acknowledge unresolved pending write")
	}
	if iteration < 0 || iteration < m.AcknowledgedIteration {
		return errors.New("iteration regression")
	}
	if checkpoint != "" {
		cp, err := s.loadCheckpoint(checkpoint, project)
		if err != nil {
			return fmt.Errorf("checkpoint not confirmed: %w", err)
		}
		_ = cp
		found := false
		for _, p := range m.Checkpoints {
			if p == checkpoint {
				found = true
				break
			}
		}
		if !found {
			m.Checkpoints = append(m.Checkpoints, checkpoint)
		}
	}
	m.AcknowledgedIteration = iteration
	if done {
		m.Done = true
	}
	return s.writeTask(m)
}

// ReviewTask verifies every armed checkpoint before claiming that the task's
// changed files are still exactly as the agent left them.
func (s *Store) ReviewTask(project, task string) (TaskManifest, error) {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return m, err
	}
	if len(m.Checkpoints) == 0 {
		return m, nil
	}
	// Several writes to one path are valid; task rollback needs a distinct
	// chain-aware protocol. Do not incorrectly claim group rollback is safe.
	seen := map[string]bool{}
	for _, p := range m.Checkpoints {
		cp, err := s.loadCheckpoint(p, project)
		if err != nil {
			return m, err
		}
		if seen[cp.Path] {
			return m, fmt.Errorf("multiple writes to %s: reverse-chain recovery required", cp.Path)
		}
		seen[cp.Path] = true
	}
	_, err = s.PreviewTaskRollback(m.Checkpoints, project)
	return m, err
}

// VerifyWriteForRecovery proves an unacknowledged DEXSTER write is already
// safely persisted. This is read-only: it never rewrites source files or
// trusts a path supplied only by the agent journal.
func (s *Store) VerifyWriteForRecovery(project, task, checkpoint, path string) error {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return err
	}
	if m.Pending != nil {
		return errors.New("pending write must be reconciled first")
	}
	found := false
	for _, item := range m.Checkpoints {
		if item == checkpoint {
			found = true
			break
		}
	}
	if !found {
		return errors.New("checkpoint not recorded in KRAKEN manifest")
	}
	cp, err := s.loadCheckpoint(checkpoint, project)
	if err != nil {
		return err
	}
	if cp.Path != filepath.Clean(path) || path == "" {
		return errors.New("journal path and checkpoint path differ")
	}
	target, err := targetPath(project, cp.Path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("recovery target is not regular")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if digest(raw) != cp.AfterSHA256 {
		return errors.New("file content no longer matches checkpoint after-hash")
	}
	return nil
}
