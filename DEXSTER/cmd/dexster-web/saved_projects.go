package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type savedProject struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	SavedState   bool   `json:"saved_state"`
	StateUpdated string `json:"state_updated,omitempty"`
	Status       string `json:"status"`
}
type savedProjectsResponse struct {
	Base     string         `json:"base"`
	Projects []savedProject `json:"projects"`
}

// Only persisted DONE together with a Git commit is shown as completed.
// No project is edited, resumed or deleted while listing.
func listSavedProjects(base string) (savedProjectsResponse, error) {
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return savedProjectsResponse{}, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return savedProjectsResponse{Base: base, Projects: []savedProject{}}, nil
	}
	if err != nil {
		return savedProjectsResponse{}, err
	}
	out := savedProjectsResponse{Base: base, Projects: []savedProject{}}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(base, e.Name())
		p := savedProject{Name: e.Name(), Path: path, Status: "new"}
		if info, err := os.Stat(path); err == nil {
			p.StateUpdated = info.ModTime().Format(time.RFC3339Nano)
		}
		sp := filepath.Join(path, ".dexster", "state.json")
		if info, err := os.Stat(sp); err == nil && info.Mode().IsRegular() {
			p.SavedState = true
			if info.ModTime().After(time.Time{}) {
				p.StateUpdated = info.ModTime().Format(time.RFC3339Nano)
			}
			p.Status = "incomplete"
			if info.Size() > 0 && info.Size() <= 16*1024*1024 {
				if data, err := os.ReadFile(sp); err == nil {
					var st struct {
						Done      bool   `json:"done"`
						GitCommit string `json:"git_commit"`
					}
					if json.Unmarshal(data, &st) == nil && st.Done && strings.TrimSpace(st.GitCommit) != "" {
						p.Status = "completed"
					}
				}
			}
		} else {
			// Code files without current agent state are not presumed finished.
			files, err := os.ReadDir(path)
			if err == nil {
				for _, f := range files {
					if !f.IsDir() && !strings.HasPrefix(f.Name(), ".") {
						p.Status = "incomplete"
						break
					}
				}
			}
		}
		out.Projects = append(out.Projects, p)
	}
	sort.SliceStable(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i], out.Projects[j]
		if a.StateUpdated != b.StateUpdated {
			return a.StateUpdated > b.StateUpdated
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

func (c *controller) registerSavedProjectsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/saved-projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		home, err := browserHome()
		if err != nil {
			http.Error(w, "home unavailable", http.StatusInternalServerError)
			return
		}
		out, err := listSavedProjects(filepath.Join(home, "DEXSTER-Projects"))
		if err != nil {
			http.Error(w, "projects directory unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
}
