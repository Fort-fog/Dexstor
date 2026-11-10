package memory

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VerifiedState is an exact KRAKEN-owned source snapshot captured only after
// DEXSTER's completion gate has passed. It is independent from the project's
// Git branch/index and therefore also works for non-Git projects.
type VerifiedState struct {
	Version       int       `json:"version"`
	Created       time.Time `json:"created"`
	Iteration     int       `json:"iteration"`
	Snapshot      string    `json:"snapshot"`
	TreeSHA256    string    `json:"tree_sha256"`
	ArchiveSHA256 string    `json:"archive_sha256"`
}

var snapshotExcludedDirs = map[string]bool{
	".git": true, ".dexster": true, ".kraken": true,
	"node_modules": true, ".cache": true, ".go-cache": true,
	".dexster-worker-staged": true,
}

func snapshotPathAllowed(rel string) bool {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".git" || part == ".dexster" || part == ".kraken" {
			return false
		}
	}
	return true
}

func sourceFiles(root string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("kraken: verified snapshot refuses symlink %s", rel)
		}
		if d.IsDir() {
			if snapshotExcludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !snapshotPathAllowed(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("kraken: verified snapshot refuses non-regular file %s", rel)
		}
		files = append(files, filepath.Clean(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func treeDigest(root string, files []string) (string, error) {
	h := sha256.New()
	for _, rel := range files {
		if !snapshotPathAllowed(rel) {
			return "", fmt.Errorf("kraken: unsafe snapshot path %q", rel)
		}
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("kraken: non-regular snapshot path %s", rel)
		}
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", filepath.ToSlash(rel), info.Mode().Perm(), info.Size())
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) createVerifiedArchive(project, prefix string, iteration int) (VerifiedState, error) {
	var out VerifiedState
	if s == nil {
		return out, errors.New("kraken: nil store")
	}
	project, err := filepath.Abs(project)
	if err != nil {
		return out, err
	}
	files, err := sourceFiles(project)
	if err != nil {
		return out, err
	}
	tree, err := treeDigest(project, files)
	if err != nil {
		return out, err
	}
	dir := filepath.Join(s.Root, "verified")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return out, err
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return out, errors.New("kraken: unsafe verified directory")
	}
	f, err := os.CreateTemp(dir, ".verified-*.tar.gz")
	if err != nil {
		return out, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return out, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		path := filepath.Join(project, rel)
		info, err := os.Lstat(path)
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return out, err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return out, err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = time.Unix(0, 0).UTC()
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return out, err
		}
		in, err := os.Open(path)
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return out, err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil || closeErr != nil {
			tw.Close()
			gz.Close()
			f.Close()
			if copyErr != nil {
				return out, copyErr
			}
			return out, closeErr
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		return out, err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return out, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return out, err
	}
	if err := f.Close(); err != nil {
		return out, err
	}
	stamp := time.Now().UTC()
	name := fmt.Sprintf("%s-%s-i%06d-%d.tar.gz", prefix, tree[:16], iteration, stamp.UnixNano())
	dest := filepath.Join(dir, name)
	if err := os.Rename(tmp, dest); err != nil {
		return out, err
	}
	archiveHash, err := fileDigest(dest)
	if err != nil {
		return out, err
	}
	out = VerifiedState{Version: 1, Created: stamp, Iteration: iteration, Snapshot: dest, TreeSHA256: tree, ArchiveSHA256: archiveHash}
	return out, nil
}

// PrepareVerifiedSnapshot captures the exact source state after the evidence
// gate passed. The caller later atomically attaches it to the task manifest.
func (s *Store) PrepareVerifiedSnapshot(project, task string, iteration int) (VerifiedState, error) {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return VerifiedState{}, err
	}
	if m.Pending != nil {
		return VerifiedState{}, errors.New("kraken: cannot snapshot unresolved pending write")
	}
	if iteration < m.AcknowledgedIteration {
		return VerifiedState{}, errors.New("kraken: verified iteration regression")
	}
	return s.createVerifiedArchive(project, "task-"+m.ID, iteration)
}

func (s *Store) validateVerifiedState(project string, v VerifiedState) error {
	if v.Version != 1 || v.Iteration < 0 || v.Created.IsZero() || len(v.TreeSHA256) != 64 || len(v.ArchiveSHA256) != 64 || v.Snapshot == "" {
		return errors.New("kraken: invalid verified state")
	}
	root, err := filepath.Abs(filepath.Join(s.Root, "verified"))
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(v.Snapshot)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) != root || !strings.HasSuffix(abs, ".tar.gz") {
		return errors.New("kraken: verified snapshot outside store")
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("kraken: verified snapshot not regular")
	}
	hash, err := fileDigest(abs)
	if err != nil {
		return err
	}
	if hash != v.ArchiveSHA256 {
		return errors.New("kraken: verified snapshot hash mismatch")
	}
	_ = project
	return nil
}

// CompleteVerifiedTask publishes DONE and its exact verified snapshot in the
// same manifest update.
func (s *Store) CompleteVerifiedTask(project, task string, iteration int, v VerifiedState) error {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return err
	}
	if m.Pending != nil {
		return errors.New("kraken: cannot complete unresolved pending write")
	}
	if iteration < m.AcknowledgedIteration {
		return errors.New("kraken: completion iteration regression")
	}
	if err := s.validateVerifiedState(project, v); err != nil {
		return err
	}
	if v.Iteration != iteration {
		return errors.New("kraken: verified snapshot iteration mismatch")
	}
	m.AcknowledgedIteration = iteration
	m.Done = true
	m.Verified = &v
	return s.writeTask(m)
}
