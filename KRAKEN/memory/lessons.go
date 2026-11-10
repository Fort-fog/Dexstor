package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Lesson is a human-reviewed, project-scoped note. It is untrusted context,
// never an instruction to bypass the agent's tools or safety policy.
type Lesson struct {
	Project  string `json:"project"`
	Title    string `json:"title"`
	Note     string `json:"note"`
	Approved bool   `json:"approved"`
}

// Lessons returns a bounded set of reviewed notes for exactly this project.
// Symlinks are ignored to prevent reading arbitrary files from the host.
func (s *Store) Lessons(project string, max int) ([]Lesson, error) {
	if s == nil {
		return nil, fmt.Errorf("kraken: nil store")
	}
	if max <= 0 {
		return nil, nil
	}
	if max > 8 {
		max = 8
	}
	dir := filepath.Join(s.Root, "lessons")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]Lesson, 0, max)
	for _, e := range entries {
		if len(result) >= max {
			break
		}
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() > 8192 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var lesson Lesson
		if json.Unmarshal(raw, &lesson) != nil || !lesson.Approved || lesson.Project != project || lesson.Title == "" || lesson.Note == "" {
			continue
		}
		if len(lesson.Title) > 120 || len(lesson.Note) > 1200 {
			continue
		}
		result = append(result, lesson)
	}
	return result, nil
}
