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
)

// RollbackPlan is a read-only preview of a checkpoint guarded by SHA-256.
type RollbackPlan struct {
	Path           string
	Created        bool
	ExpectedSHA256 string
	RestoredSHA256 string
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func (s *Store) loadCheckpoint(file, project string) (Checkpoint, error) {
	if s == nil {
		return Checkpoint{}, errors.New("nil store")
	}
	root, err := filepath.Abs(filepath.Join(s.Root, "checkpoints"))
	if err != nil {
		return Checkpoint{}, err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return Checkpoint{}, err
	}
	if filepath.Dir(abs) != root || !strings.HasSuffix(filepath.Base(abs), ".json") {
		return Checkpoint{}, errors.New("checkpoint outside checkpoint directory")
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return Checkpoint{}, err
	}
	if !st.Mode().IsRegular() {
		return Checkpoint{}, errors.New("checkpoint must be a regular file")
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return Checkpoint{}, err
	}
	var cp Checkpoint
	if err = json.Unmarshal(raw, &cp); err != nil {
		return cp, err
	}
	if cp.Project != project || cp.Path == "" || filepath.IsAbs(cp.Path) || filepath.Clean(cp.Path) != cp.Path || cp.Path == "." || cp.Path == ".." || strings.HasPrefix(cp.Path, ".."+string(os.PathSeparator)) {
		return cp, errors.New("checkpoint project/path mismatch")
	}
	if cp.AfterSHA256 == "" {
		return cp, errors.New("checkpoint not armed: write was not confirmed")
	}
	if cp.SHA256 != digest(cp.Content) {
		return cp, errors.New("checkpoint preimage hash mismatch")
	}
	if !cp.Existed && len(cp.Content) > 0 {
		return cp, errors.New("new file checkpoint contains preimage")
	}
	for _, part := range strings.Split(filepath.ToSlash(cp.Path), "/") {
		if part == ".git" || part == ".dexster" {
			return cp, errors.New("protected internal path")
		}
	}
	return cp, nil
}

// targetPath checks every existing component for symlinks, avoiding path escape.
func targetPath(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("project root is not a plain directory")
	}
	target := absRoot
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink in target path")
		}
	}
	return target, nil
}

// PreviewRollback never modifies files. It refuses modifications since the recorded write.
func (s *Store) PreviewRollback(file, project string) (RollbackPlan, error) {
	project, err := filepath.Abs(project)
	if err != nil {
		return RollbackPlan{}, err
	}
	cp, err := s.loadCheckpoint(file, project)
	if err != nil {
		return RollbackPlan{}, err
	}
	target, err := targetPath(project, cp.Path)
	if err != nil {
		return RollbackPlan{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return RollbackPlan{}, fmt.Errorf("target missing or unreadable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return RollbackPlan{}, errors.New("target not regular")
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return RollbackPlan{}, err
	}
	if digest(current) != cp.AfterSHA256 {
		return RollbackPlan{}, errors.New("target changed since checkpoint: rollback refused")
	}
	return RollbackPlan{Path: cp.Path, Created: !cp.Existed, ExpectedSHA256: cp.AfterSHA256, RestoredSHA256: cp.SHA256}, nil
}

// ApplyRollback requires explicit invocation and compares current contents before writing.
// It does not invoke git reset/checkout or silently overwrite newer changes.
func (s *Store) ApplyRollback(file, project string) error {
	plan, err := s.PreviewRollback(file, project)
	if err != nil {
		return err
	}
	cp, err := s.loadCheckpoint(file, project)
	if err != nil {
		return err
	}
	target, err := targetPath(project, plan.Path)
	if err != nil {
		return err
	}
	// Re-read immediately before applying; if content differs, refuse.
	now, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if digest(now) != plan.ExpectedSHA256 {
		return errors.New("target changed before restore")
	}
	if plan.Created {
		return os.Remove(target)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".kraken-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(cp.Content); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// ArmCheckpoint publishes a post-write hash after a successful source write.
// Unarmed snapshots can never be used for rollback.
func (s *Store) ArmCheckpoint(file string, after []byte) error {
	if s == nil {
		return errors.New("nil store")
	}
	root, err := filepath.Abs(filepath.Join(s.Root, "checkpoints"))
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(file)
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
		return errors.New("not regular checkpoint")
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	var cp Checkpoint
	if err = json.Unmarshal(raw, &cp); err != nil {
		return err
	}
	if cp.AfterSHA256 != "" {
		return errors.New("checkpoint already armed")
	}
	cp.AfterSHA256 = digest(after)
	raw, err = json.Marshal(cp)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".arming-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), abs)
}
