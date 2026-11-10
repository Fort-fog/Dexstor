package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ResumeCapsule is a bounded, human-inspectable continuation hint. It is NOT
// authoritative: the DEXSTER journal and KRAKEN manifest remain the evidence.
type ResumeCapsule struct {
	Iteration     int       `json:"iteration"`
	StateSHA256   string    `json:"state_sha256"`
	NextStep      string    `json:"next_step"`
	RecentActions []string  `json:"recent_actions,omitempty"`
	ReadPaths     []string  `json:"read_paths,omitempty"`
	Updated       time.Time `json:"updated"`
}

func StateDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SaveResume never advances the acknowledged iteration: a capsule is a cache
// and must only describe an already acknowledged state.
func (s *Store) SaveResume(project, task string, c ResumeCapsule) error {
	m, err := s.ReadTask(project, task)
	if err != nil {
		return err
	}
	if m.Pending != nil || c.Iteration != m.AcknowledgedIteration || len(c.StateSHA256) != 64 {
		return errors.New("resume capsule: unacknowledged or invalid state")
	}
	if len(c.NextStep) > 600 || len(c.RecentActions) > 4 || len(c.ReadPaths) > 30 {
		return errors.New("resume capsule: size limits exceeded")
	}
	for _, v := range append(append([]string{c.NextStep}, c.RecentActions...), c.ReadPaths...) {
		if len(v) > 300 || strings.ContainsRune(v, '\x00') {
			return errors.New("resume capsule: invalid entry")
		}
	}
	c.Updated = time.Now().UTC()
	m.Resume = &c
	return s.writeTask(m)
}
