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
	"time"
)

// Experience is KRAKEN's durable error -> recovery -> verification memory.
// It is advisory context only; DEXSTER's controller/tool gates remain authoritative.
type Experience struct {
	Version           int       `json:"version"`
	ID                string    `json:"id"`
	Created           time.Time `json:"created"`
	Updated           time.Time `json:"updated"`
	Project           string    `json:"project"`
	Task              string    `json:"task"`
	FailureIteration  int       `json:"failure_iteration"`
	FailureTool       string    `json:"failure_tool"`
	Path              string    `json:"path,omitempty"`
	FailureSummary    string    `json:"failure_summary"`
	Occurrences       int       `json:"occurrences"`
	RecoveryIteration int       `json:"recovery_iteration,omitempty"`
	RecoveryTool      string    `json:"recovery_tool,omitempty"`
	RecoverySummary   string    `json:"recovery_summary,omitempty"`
	VerificationTool  string    `json:"verification_tool,omitempty"`
	Resolved          bool      `json:"resolved"`
	Verified          bool      `json:"verified"`
}

func experienceID(ev Event) string {
	sum := sha256.Sum256([]byte(ev.Project + "\x00" + ev.Task + "\x00" + ev.Tool + "\x00" + ev.Path + "\x00" + ev.Summary + "\x00" + fmt.Sprint(ev.Iteration) + "\x00" + ev.Time.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) experienceDir() string { return filepath.Join(s.Root, "experiences") }

func (s *Store) writeExperience(e Experience) error {
	if e.Version != 1 || e.ID == "" || e.Project == "" || e.Task == "" || e.FailureTool == "" || e.FailureSummary == "" || e.Created.IsZero() {
		return errors.New("kraken: invalid experience")
	}
	dir := s.experienceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dest := filepath.Join(dir, e.ID+".json")
	e.Updated = time.Now().UTC()
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".experience-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
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
	return os.Rename(tmp, dest)
}

func (s *Store) allExperiences() ([]Experience, error) {
	dir := s.experienceDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Experience, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() || ent.Type()&os.ModeSymlink != 0 || !ent.Type().IsRegular() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		var e Experience
		if json.Unmarshal(raw, &e) != nil || e.Version != 1 || e.ID+".json" != ent.Name() {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.Before(out[j].Updated) })
	return out, nil
}

func isExperienceMutation(tool string) bool {
	switch tool {
	case "write_file", "replace_in_file", "copy_file", "move_file", "delete_file", "make_dir":
		return true
	default:
		return false
	}
}
func isExperienceVerification(tool string) bool {
	switch tool {
	case "git_diff", "run_tests", "go_test_extra", "validate_html", "review_task", "verify_http":
		return true
	default:
		return false
	}
}

// observeExperience is called only after the append-only Event itself is durable.
func (s *Store) observeExperience(ev Event) error {
	if s == nil || ev.Tool == "" {
		return nil
	}
	exps, err := s.allExperiences()
	if err != nil {
		return err
	}
	if !ev.OK {
		// Coalesce immediately repeated identical failures instead of creating noise.
		for i := len(exps) - 1; i >= 0; i-- {
			e := exps[i]
			if e.Resolved {
				continue
			}
			if e.Project == ev.Project && e.Task == ev.Task && e.FailureTool == ev.Tool && e.Path == ev.Path && e.FailureSummary == ev.Summary {
				e.Occurrences++
				return s.writeExperience(e)
			}
		}
		e := Experience{Version: 1, ID: experienceID(ev), Created: ev.Time, Updated: ev.Time, Project: ev.Project, Task: ev.Task, FailureIteration: ev.Iteration, FailureTool: ev.Tool, Path: ev.Path, FailureSummary: ev.Summary, Occurrences: 1}
		return s.writeExperience(e)
	}

	// A successful mutation can resolve a failed mutation on the same file.
	if isExperienceMutation(ev.Tool) {
		for i := len(exps) - 1; i >= 0; i-- {
			e := exps[i]
			if e.Resolved || e.Project != ev.Project || e.Task != ev.Task || e.Path == "" || e.Path != ev.Path {
				continue
			}
			if !isExperienceMutation(e.FailureTool) {
				continue
			}
			e.Resolved = true
			e.RecoveryIteration, e.RecoveryTool, e.RecoverySummary = ev.Iteration, ev.Tool, ev.Summary
			return s.writeExperience(e)
		}
	}

	// A verification success resolves the same failed gate and certifies all
	// already-resolved repairs from this task.
	if isExperienceVerification(ev.Tool) {
		changed := false
		for i := range exps {
			e := exps[i]
			if e.Project != ev.Project || e.Task != ev.Task {
				continue
			}
			if !e.Resolved && e.FailureTool == ev.Tool {
				e.Resolved = true
				e.RecoveryIteration, e.RecoveryTool, e.RecoverySummary = ev.Iteration, ev.Tool, ev.Summary
			}
			if e.Resolved && !e.Verified {
				e.Verified = true
				e.VerificationTool = ev.Tool
			}
			if e.Resolved || e.Verified {
				if err := s.writeExperience(e); err != nil {
					return err
				}
				changed = true
			}
		}
		_ = changed
	}
	return nil
}

// ExperienceHints returns compact, verified recovery patterns relevant to the
// failure the model has just hit. Same-project memories rank above global ones.
func (s *Store) ExperienceHints(project, tool, path string, max int) ([]string, error) {
	if s == nil || max <= 0 || tool == "" {
		return nil, nil
	}
	if max > 4 {
		max = 4
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	exps, err := s.allExperiences()
	if err != nil {
		return nil, err
	}
	type scored struct {
		e     Experience
		score int
	}
	var items []scored
	for _, e := range exps {
		if !e.Resolved || !e.Verified {
			continue
		}
		score := 0
		if e.Project == abs {
			score += 12
		}
		if e.FailureTool == tool {
			score += 10
		}
		if path != "" && e.Path == path {
			score += 8
		}
		if score < 10 {
			continue
		}
		items = append(items, scored{e: e, score: score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].e.Updated.After(items[j].e.Updated)
		}
		return items[i].score > items[j].score
	})
	if len(items) > max {
		items = items[:max]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		e := item.e
		f := strings.TrimSpace(e.FailureSummary)
		r := strings.TrimSpace(e.RecoverySummary)
		if len(f) > 220 {
			f = f[:220]
		}
		if len(r) > 220 {
			r = r[:220]
		}
		where := "global"
		if e.Project == abs {
			where = "this project"
		}
		out = append(out, fmt.Sprintf("%s: %s failed on %s (%s); recovery=%s (%s); verified=%s", where, e.FailureTool, e.Path, f, e.RecoveryTool, r, e.VerificationTool))
	}
	return out, nil
}
