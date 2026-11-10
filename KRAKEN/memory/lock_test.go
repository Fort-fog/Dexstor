package memory

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestExclusiveStoreLock(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	first, err := s.LockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.LockExclusive(); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected busy, got %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := s.LockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}
func TestLockCrossProcessAndCrashRelease(t *testing.T) {
	if os.Getenv("KRAKEN_LOCK_CHILD") == "1" {
		s := &Store{Root: os.Getenv("KRAKEN_LOCK_ROOT")}
		l, e := s.LockExclusive()
		if e != nil {
			os.Exit(7)
		}
		defer l.Close()
		os.Exit(0)
	}
	root := t.TempDir()
	s := &Store{Root: root}
	l, err := s.LockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(os.Args[0], "-test.run=^TestLockCrossProcessAndCrashRelease$")
	c.Env = append(os.Environ(), "KRAKEN_LOCK_CHILD=1", "KRAKEN_LOCK_ROOT="+root)
	if err := c.Run(); err == nil {
		t.Fatal("child acquired held lock")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	c = exec.Command(os.Args[0], "-test.run=^TestLockCrossProcessAndCrashRelease$")
	c.Env = append(os.Environ(), "KRAKEN_LOCK_CHILD=1", "KRAKEN_LOCK_ROOT="+root)
	if output, err := c.CombinedOutput(); err != nil {
		t.Fatalf("lock not released after process exit: %s %v", output, err)
	}
}
