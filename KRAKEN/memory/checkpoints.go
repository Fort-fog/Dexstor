package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Checkpoint describes a read-only pre-write snapshot. Restoring is intentionally
// not automatic: callers must first verify the current file and request approval.
type Checkpoint struct {
	Created     time.Time `json:"created"`
	Project     string    `json:"project"`
	Path        string    `json:"path"`
	SHA256      string    `json:"sha256"`
	Existed     bool      `json:"existed"`
	Content     []byte    `json:"content,omitempty"`
	AfterSHA256 string    `json:"after_sha256,omitempty"`
}

// SaveCheckpoint captures the bytes supplied by a trusted file reader. The
// checkpoint is stored outside the source repository and never overwrites one.
func (s *Store) SaveCheckpoint(project, path string, existed bool, content []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("kraken: nil store")
	}
	if project == "" || path == "" || filepath.IsAbs(path) || filepath.Clean(path) == ".." || strings.HasPrefix(filepath.Clean(path), ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("kraken: invalid checkpoint path")
	}
	dir := filepath.Join(s.Root, "checkpoints")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	cp := Checkpoint{Created: time.Now().UTC(), Project: project, Path: filepath.Clean(path), SHA256: hex.EncodeToString(sum[:]), Existed: existed}
	if existed {
		cp.Content = append([]byte(nil), content...)
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return "", err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	dest := name + ".json"
	if err = os.Rename(name, dest); err != nil {
		return "", err
	}
	return dest, nil
}
