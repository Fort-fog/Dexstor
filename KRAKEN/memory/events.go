// Package memory provides an append-only event store for DEXSTER runs.
// It does not modify the agent project, manage Git, or perform rollback.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Event struct {
	Type      string    `json:"type"`
	Time      time.Time `json:"time"`
	Task      string    `json:"task"`
	Project   string    `json:"project"`
	Iteration int       `json:"iteration,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Path      string    `json:"path,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	OK        bool      `json:"ok"`
	Exit      int       `json:"exit,omitempty"`
}

type Store struct{ Root string }

func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("kraken: root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("kraken: root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("kraken: root is not a directory")
	}
	if _, err := os.Stat(filepath.Join(abs, "events")); err != nil {
		return nil, fmt.Errorf("kraken: events directory missing: %w", err)
	}
	return &Store{Root: abs}, nil
}

// Record atomically publishes one unique event in KRAKEN/events.
// A failed write never replaces an earlier event.
func (s *Store) Record(ev Event) error {
	if s == nil {
		return fmt.Errorf("kraken: nil store")
	}
	if ev.Type == "" || ev.Task == "" || ev.Project == "" {
		return fmt.Errorf("kraken: type, task and project are required")
	}
	ev.Time = time.Now().UTC()
	dir := filepath.Join(s.Root, "events")
	data, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return fmt.Errorf("kraken: marshal event: %w", err)
	}
	f, err := os.CreateTemp(dir, ".event-*")
	if err != nil {
		return fmt.Errorf("kraken: create event: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	dest := tmp + ".json"
	if err = os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("kraken: commit event: %w", err)
	}
	// The append-only event is authoritative. Experience memory is a derived
	// durable index of failures that later received a successful repair.
	if err = s.observeExperience(ev); err != nil {
		return fmt.Errorf("kraken: experience memory: %w", err)
	}
	return nil
}
