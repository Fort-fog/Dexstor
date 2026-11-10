package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CleanupCandidate is a checkpoint that may be pruned after explicit approval.
// Events, lessons, unfinished tasks, and manifests are never candidates.
type CleanupCandidate struct {
	TaskID     string
	Checkpoint string
	Project    string
}

type CleanupPlan struct {
	Candidates []CleanupCandidate
	Protected  int
}

// PreviewCleanup fails closed on corrupt/unsafe task metadata or checkpoint
// references. Only checkpoints owned exclusively by old, completed manifests
// are eligible; standalone checkpoints remain untouched.
func (s *Store) PreviewCleanup(olderThan time.Duration, now time.Time) (CleanupPlan, error) {
	var plan CleanupPlan
	if s == nil || olderThan <= 0 {
		return plan, errors.New("cleanup requires store and positive retention")
	}
	if err := s.checkNoPendingCleanup(); err != nil {
		return plan, err
	}
	taskDir := filepath.Join(s.Root, "tasks")
	cpDir := filepath.Join(s.Root, "checkpoints")
	for _, dir := range []string{taskDir, cpDir} {
		st, err := os.Lstat(dir)
		if err != nil {
			return plan, err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return plan, fmt.Errorf("unsafe directory: %s", dir)
		}
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return plan, err
	}
	manifests := make([]TaskManifest, 0)
	refs := map[string]int{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() {
			return plan, fmt.Errorf("unsafe task manifest %s", e.Name())
		}
		path := filepath.Join(taskDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return plan, err
		}
		var m TaskManifest
		if err = json.Unmarshal(b, &m); err != nil {
			return plan, fmt.Errorf("invalid manifest %s: %w", path, err)
		}
		if m.Version != 1 || m.ID == "" || m.ID != TaskID(m.Project, m.Task) || e.Name() != m.ID+".json" || m.Project == "" || m.Task == "" {
			return plan, fmt.Errorf("invalid manifest identity %s", path)
		}
		all := append([]string{}, m.Checkpoints...)
		if m.Pending != nil {
			all = append(all, m.Pending.Checkpoint)
		}
		for _, p := range all {
			if !filepath.IsAbs(p) || filepath.Dir(filepath.Clean(p)) != cpDir || !strings.HasSuffix(p, ".json") {
				return plan, fmt.Errorf("unsafe checkpoint reference %q", p)
			}
			refs[p]++
		}
		manifests = append(manifests, m)
	}
	for _, m := range manifests {
		if !m.Done || m.Pending != nil || m.Updated.IsZero() || now.Sub(m.Updated) < olderThan {
			plan.Protected += len(m.Checkpoints)
			continue
		}
		for _, p := range m.Checkpoints {
			if refs[p] != 1 {
				plan.Protected++
				continue
			}
			info, err := os.Lstat(p)
			if err != nil {
				return CleanupPlan{}, err
			}
			if !info.Mode().IsRegular() {
				return CleanupPlan{}, fmt.Errorf("unsafe checkpoint %s", p)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return CleanupPlan{}, err
			}
			var cp Checkpoint
			if err = json.Unmarshal(b, &cp); err != nil {
				return CleanupPlan{}, fmt.Errorf("corrupt checkpoint %s: %w", p, err)
			}
			if cp.Project != m.Project || cp.Created.IsZero() || now.Sub(cp.Created) < olderThan || cp.AfterSHA256 == "" {
				plan.Protected++
				continue
			}
			plan.Candidates = append(plan.Candidates, CleanupCandidate{TaskID: m.ID, Checkpoint: p, Project: m.Project})
		}
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].Checkpoint < plan.Candidates[j].Checkpoint })
	return plan, nil
}

// ApplyCleanup rechecks the plan and removes manifest references BEFORE unlinking
// checkpoints, so interruption can leave a harmless orphan, not a dangling
// manifest reference. Run only when agents sharing this KRAKEN store are stopped.
func (s *Store) ApplyCleanup(olderThan time.Duration, now time.Time, preview CleanupPlan) (int, error) {
	latest, err := s.PreviewCleanup(olderThan, now)
	if err != nil {
		return 0, err
	}
	if len(latest.Candidates) != len(preview.Candidates) {
		return 0, errors.New("cleanup plan changed; preview again")
	}
	for i, c := range latest.Candidates {
		if c != preview.Candidates[i] {
			return 0, errors.New("cleanup plan changed; preview again")
		}
	}
	groups := map[string][]string{}
	for _, c := range latest.Candidates {
		groups[c.TaskID] = append(groups[c.TaskID], c.Checkpoint)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	removed := 0
	for _, id := range keys {
		path := filepath.Join(s.Root, "tasks", id+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			return removed, err
		}
		var m TaskManifest
		if err = json.Unmarshal(raw, &m); err != nil {
			return removed, err
		}
		if m.ID != id || !m.Done || m.Pending != nil {
			return removed, errors.New("task changed during cleanup")
		}
		n, err := s.beginCleanupGroup(id, m.Project, groups[id])
		removed += n
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}
