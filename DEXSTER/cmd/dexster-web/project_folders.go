package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type projectFolderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type projectFolderListing struct {
	Path    string               `json:"path"`
	Parent  string               `json:"parent"`
	Folders []projectFolderEntry `json:"folders"`
}

// The folder chooser is restricted to the user's home. The existing manual
// project path input and validateProject rules remain unchanged.
func browserHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(home)
}

func browseFolderPath(raw string) (string, string, error) {
	home, err := browserHome()
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(raw) == "" {
		raw = filepath.Join(home, "DEXSTER-Projects")
		if st, err := os.Stat(raw); err != nil || !st.IsDir() {
			raw = home
		}
	}
	if !filepath.IsAbs(raw) {
		return "", "", errors.New("absolute directory path required")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(home, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("folder outside home is not browsable; enter path manually")
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", errors.New("not a directory")
	}
	return resolved, home, nil
}

func listProjectFolders(raw string) (projectFolderListing, error) {
	dir, home, err := browseFolderPath(raw)
	if err != nil {
		return projectFolderListing{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return projectFolderListing{}, err
	}
	out := projectFolderListing{Path: dir, Parent: dir, Folders: []projectFolderEntry{}}
	if dir != home {
		out.Parent = filepath.Dir(dir)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// Avoid listing symlinks: don't offer a path that could escape the sandbox.
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		out.Folders = append(out.Folders, projectFolderEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out.Folders, func(i, j int) bool {
		return strings.ToLower(out.Folders[i].Name) < strings.ToLower(out.Folders[j].Name)
	})
	return out, nil
}

func createProjectFolder(parent, name string) (string, error) {
	dir, _, err := browseFolderPath(parent)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") {
		return "", errors.New("invalid new folder name")
	}
	path := filepath.Join(dir, name)
	if err := os.Mkdir(path, 0700); err != nil {
		return "", fmt.Errorf("create folder: %w", err)
	}
	return path, nil
}

func (c *controller) registerProjectFolderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/project-folders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		listing, err := listProjectFolders(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(listing)
	})
	mux.HandleFunc("/api/project-folders/mkdir", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var p struct {
			Parent string `json:"parent"`
			Name   string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		path, err := createProjectFolder(p.Parent, p.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": path})
	})
}
