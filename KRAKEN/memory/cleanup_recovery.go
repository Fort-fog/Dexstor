package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cleanupIntent is written and fsynced BEFORE altering the task manifest.
// A crash may leave either the reference or the checkpoint, but recovery can
// finish the operation without deleting a checkpoint used by a different task.
type cleanupIntent struct {
	Version     int                 `json:"version"`
	TaskID      string              `json:"task_id"`
	Project     string              `json:"project"`
	Checkpoints []cleanupCheckpoint `json:"checkpoints"`
}
type cleanupCheckpoint struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (s *Store) cleanupIntentPath() string { return filepath.Join(s.Root, ".cleanup-pending.json") }
func (s *Store) readCleanupIntent() (cleanupIntent, error) {
	p := s.cleanupIntentPath()
	fi, err := os.Lstat(p)
	if err != nil {
		return cleanupIntent{}, err
	}
	if !fi.Mode().IsRegular() {
		return cleanupIntent{}, errors.New("unsafe cleanup intent")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cleanupIntent{}, err
	}
	var in cleanupIntent
	if err = json.Unmarshal(b, &in); err != nil {
		return in, err
	}
	if in.Version != 1 || in.TaskID == "" || in.Project == "" || len(in.Checkpoints) == 0 || len(in.TaskID) != 32 {
		return in, errors.New("invalid cleanup intent")
	}
	seen := map[string]bool{}
	dir := filepath.Join(s.Root, "checkpoints")
	for _, cp := range in.Checkpoints {
		if !filepath.IsAbs(cp.Path) || filepath.Dir(filepath.Clean(cp.Path)) != dir || !strings.HasSuffix(cp.Path, ".json") || len(cp.SHA256) != 64 || seen[cp.Path] {
			return in, errors.New("unsafe cleanup intent checkpoint")
		}
		seen[cp.Path] = true
	}
	return in, nil
}
func (s *Store) writeCleanupIntent(in cleanupIntent) error {
	p := s.cleanupIntentPath()
	if _, err := os.Lstat(p); err == nil {
		return errors.New("pending cleanup must be recovered first")
	} else if !os.IsNotExist(err) {
		return err
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Root, ".cleanup-intent-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), p); err != nil {
		return err
	}
	return syncDirectory(s.Root)
}
func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) finishCleanupIntent() (int, error) {
	in, err := s.readCleanupIntent()
	if err != nil {
		return 0, err
	}
	path := filepath.Join(s.Root, "tasks", in.TaskID+".json")
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, errors.New("unsafe task manifest")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var m TaskManifest
	if err = json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	if m.ID != in.TaskID || m.Project != in.Project || m.ID != TaskID(m.Project, m.Task) || m.Version != 1 || !m.Done || m.Pending != nil {
		return 0, errors.New("cleanup task identity/status changed")
	}
	chosen := map[string]bool{}
	for _, cp := range in.Checkpoints {
		chosen[cp.Path] = true
	}
	// Check every task manifest for a second owner before removing anything.
	entries, err := os.ReadDir(filepath.Join(s.Root, "tasks"))
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if !e.Type().IsRegular() {
			return 0, errors.New("unsafe task manifest")
		}
		data, err := os.ReadFile(filepath.Join(s.Root, "tasks", e.Name()))
		if err != nil {
			return 0, err
		}
		var other TaskManifest
		if err = json.Unmarshal(data, &other); err != nil {
			return 0, err
		}
		if other.ID != strings.TrimSuffix(e.Name(), ".json") || other.ID != TaskID(other.Project, other.Task) {
			return 0, errors.New("invalid task manifest identity")
		}
		if other.ID != in.TaskID {
			for _, ref := range other.Checkpoints {
				if chosen[ref] {
					return 0, errors.New("checkpoint now referenced by another task")
				}
			}
			if other.Pending != nil && chosen[other.Pending.Checkpoint] {
				return 0, errors.New("checkpoint has pending write")
			}
		}
	}
	// Preflight the entire batch before changing the manifest. Missing checkpoints
	// indicate they were already unlinked before the crash and are acceptable.
	for _, cp := range in.Checkpoints {
		fi, err := os.Lstat(cp.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !fi.Mode().IsRegular() {
			return 0, errors.New("unsafe checkpoint")
		}
		raw, err := os.ReadFile(cp.Path)
		if err != nil {
			return 0, err
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != cp.SHA256 {
			return 0, errors.New("checkpoint changed during cleanup")
		}
	}
	remaining := make([]string, 0, len(m.Checkpoints))
	change := false
	for _, cp := range m.Checkpoints {
		if chosen[cp] {
			change = true
		} else {
			remaining = append(remaining, cp)
		}
	}
	if change {
		m.Checkpoints = remaining
		if err = s.writeTask(m); err != nil {
			return 0, err
		}
	}
	count := 0
	for _, cp := range in.Checkpoints {
		if err = os.Remove(cp.Path); err == nil {
			count++
		} else if !os.IsNotExist(err) {
			return count, err
		}
	}
	if err = syncDirectory(filepath.Join(s.Root, "checkpoints")); err != nil {
		return count, err
	}
	if err = os.Remove(s.cleanupIntentPath()); err != nil {
		return count, err
	}
	if err = syncDirectory(s.Root); err != nil {
		return count, err
	}
	return count, nil
}

// RecoverCleanup requires an explicit operator invocation and stopped agents.
// It is idempotent after successful completion. It never guesses on corruption.
func (s *Store) RecoverCleanup() (int, error) {
	if s == nil {
		return 0, errors.New("nil store")
	}
	if _, err := os.Lstat(s.cleanupIntentPath()); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	return s.finishCleanupIntent()
}
func (s *Store) beginCleanupGroup(id, project string, paths []string) (int, error) {
	sort.Strings(paths)
	in := cleanupIntent{Version: 1, TaskID: id, Project: project}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return 0, err
		}
		sha := sha256.Sum256(raw)
		in.Checkpoints = append(in.Checkpoints, cleanupCheckpoint{Path: p, SHA256: hex.EncodeToString(sha[:])})
	}
	if err := s.writeCleanupIntent(in); err != nil {
		return 0, err
	}
	return s.finishCleanupIntent()
}
func (s *Store) checkNoPendingCleanup() error {
	_, err := os.Lstat(s.cleanupIntentPath())
	if err == nil {
		return fmt.Errorf("cleanup interrupted; use -recover-cleanup -apply after stopping agents")
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
