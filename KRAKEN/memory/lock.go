package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// StoreLock is a process-wide advisory OS lock. Keep it until the entire
// agent run or maintenance transaction ends. The lock file is never removed.
type StoreLock struct{ file *os.File }

// LockExclusive refuses competing KRAKEN writers/maintenance without waiting.
// flock locks are automatically released on process exit, including SIGKILL.
// All participants must voluntarily use this protocol.
func (s *Store) LockExclusive() (*StoreLock, error) {
	if s == nil || s.Root == "" {
		return nil, errors.New("kraken: missing store")
	}
	root, err := os.Lstat(s.Root)
	if err != nil {
		return nil, err
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("kraken: unsafe store root")
	}
	p := filepath.Join(s.Root, ".kraken.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		f.Close()
		return nil, errors.New("kraken: unsafe lock file")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("kraken: store busy (another agent or maintenance process holds lock): %w", err)
		}
		return nil, err
	}
	return &StoreLock{file: f}, nil
}

func (l *StoreLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
