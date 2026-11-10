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

// ProjectPoint is KRAKEN's long-lived, Git-independent history for a project
// path. Unlike a task manifest it survives new prompts, deleted agent state,
// emptied project directories, and later generations created in the same root.
type ProjectPoint struct {
	Version   int           `json:"version"`
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Project   string        `json:"project"`
	Kind      string        `json:"kind"`
	Source    string        `json:"source"`
	Task      string        `json:"task,omitempty"`
	RunID     string        `json:"run_id,omitempty"`
	Iteration int           `json:"iteration,omitempty"`
	Verified  bool          `json:"verified"`
	Created   time.Time     `json:"created"`
	Snapshot  VerifiedState `json:"snapshot"`
}

func ProjectID(project string) string {
	abs, err := filepath.Abs(project)
	if err == nil {
		project = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(project)))
	return hex.EncodeToString(sum[:16])
}

func newProjectPointID(project, kind, task, runID string, created time.Time) string {
	sum := sha256.Sum256([]byte(project + "\x00" + kind + "\x00" + task + "\x00" + runID + "\x00" + created.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

func validProjectPointKind(kind string) bool {
	switch kind {
	case "pre_dexster", "verified_dexster", "analyst", "rollback_safety":
		return true
	default:
		return false
	}
}

func (s *Store) projectPointDir(project string) (string, error) {
	if s == nil {
		return "", errors.New("kraken: nil store")
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, "projects", ProjectID(abs), "points"), nil
}

func (s *Store) writeProjectPoint(point ProjectPoint) error {
	if point.Version != 1 || point.ID == "" || point.ProjectID == "" || point.Project == "" || !validProjectPointKind(point.Kind) || point.Created.IsZero() {
		return errors.New("kraken: invalid project point")
	}
	if point.ProjectID != ProjectID(point.Project) {
		return errors.New("kraken: project point identity mismatch")
	}
	if err := s.validateVerifiedState(point.Project, point.Snapshot); err != nil {
		return fmt.Errorf("kraken: invalid project point snapshot: %w", err)
	}
	dir, err := s.projectPointDir(point.Project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("kraken: unsafe project history directory")
	}
	dest := filepath.Join(dir, point.ID+".json")
	if st, err := os.Lstat(dest); err == nil {
		if !st.Mode().IsRegular() {
			return errors.New("kraken: unsafe project point target")
		}
		// Idempotent publish: an already-written identical point is success.
		raw, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		var existing ProjectPoint
		if json.Unmarshal(raw, &existing) == nil && existing.ID == point.ID && existing.Snapshot.ArchiveSHA256 == point.Snapshot.ArchiveSHA256 {
			return nil
		}
		return errors.New("kraken: project point collision")
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := json.MarshalIndent(point, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".point-*.tmp")
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

// CaptureProjectPoint stores an exact project archive before a component acts.
// It is independent of DEXSTER/Analyst Git and therefore survives Git cleanup.
func (s *Store) CaptureProjectPoint(project, kind, source, task, runID string, iteration int, verified bool) (ProjectPoint, error) {
	var point ProjectPoint
	if !validProjectPointKind(kind) {
		return point, errors.New("kraken: invalid project point kind")
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return point, err
	}
	prefix := "project-" + ProjectID(abs) + "-" + strings.ReplaceAll(kind, "_", "-")
	snapshot, err := s.createVerifiedArchive(abs, prefix, iteration)
	if err != nil {
		return point, err
	}
	created := snapshot.Created
	point = ProjectPoint{
		Version: 1, ProjectID: ProjectID(abs), Project: abs, Kind: kind,
		Source: source, Task: task, RunID: runID, Iteration: iteration,
		Verified: verified, Created: created, Snapshot: snapshot,
	}
	point.ID = newProjectPointID(abs, kind, task, runID, created)
	if err := s.writeProjectPoint(point); err != nil {
		_ = os.Remove(snapshot.Snapshot)
		return ProjectPoint{}, err
	}
	return point, nil
}

// RegisterVerifiedProjectPoint attaches a task's already-created DONE archive to
// long-lived project history without duplicating the archive bytes.
func (s *Store) RegisterVerifiedProjectPoint(project, task, runID string, iteration int, snapshot VerifiedState) (ProjectPoint, error) {
	var point ProjectPoint
	abs, err := filepath.Abs(project)
	if err != nil {
		return point, err
	}
	if err := s.validateVerifiedState(abs, snapshot); err != nil {
		return point, err
	}
	// Avoid duplicate history points if completion is reconciled after a crash.
	points, err := s.ProjectPoints(abs)
	if err != nil {
		return point, err
	}
	for _, p := range points {
		if p.Kind == "verified_dexster" && p.Snapshot.ArchiveSHA256 == snapshot.ArchiveSHA256 {
			return p, nil
		}
	}
	created := snapshot.Created
	point = ProjectPoint{
		Version: 1, ProjectID: ProjectID(abs), Project: abs,
		Kind: "verified_dexster", Source: "dexster", Task: task, RunID: runID,
		Iteration: iteration, Verified: true, Created: created, Snapshot: snapshot,
	}
	point.ID = newProjectPointID(abs, point.Kind, task, runID, created)
	return point, s.writeProjectPoint(point)
}

func (s *Store) ProjectPoints(project string) ([]ProjectPoint, error) {
	dir, err := s.projectPointDir(project)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []ProjectPoint
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var p ProjectPoint
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("kraken: invalid project point %s: %w", e.Name(), err)
		}
		if p.Version != 1 || p.ID+".json" != e.Name() || p.ProjectID != ProjectID(p.Project) {
			return nil, fmt.Errorf("kraken: invalid project point identity %s", e.Name())
		}
		abs, _ := filepath.Abs(project)
		if p.Project != abs {
			continue
		}
		if err := s.validateVerifiedState(abs, p.Snapshot); err != nil {
			return nil, fmt.Errorf("kraken: project point %s: %w", p.ID, err)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out, nil
}

func (s *Store) VerifiedProjectPoints(project string) ([]ProjectPoint, error) {
	points, err := s.ProjectPoints(project)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectPoint, 0, len(points))
	for _, p := range points {
		if p.Verified && p.Kind == "verified_dexster" {
			out = append(out, p)
		}
	}
	return out, nil
}
