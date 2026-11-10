package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

type reviewFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff"`
}
type reviewInfo struct {
	Files  []reviewFile `json:"files"`
	Token  string       `json:"token"`
	Detail string       `json:"detail"`
}

func reviewDirectory() (string, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	d := filepath.Join(home, ".local", "share", "dexster-web", "review-snapshots")
	if e = os.MkdirAll(d, 0700); e != nil {
		return "", e
	}
	return d, nil
}
func reviewSnapshotPath(root, task string) (string, error) {
	d, e := reviewDirectory()
	if e != nil {
		return "", e
	}
	hash := sha256.Sum256([]byte(root + "\x00" + task))
	return filepath.Join(d, hex.EncodeToString(hash[:])+".tar"), nil
}
func saveInitialSnapshot(root, task string, payload []byte) error {
	p, e := reviewSnapshotPath(root, task)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(p); e == nil {
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	dir := filepath.Dir(p)
	f, e := os.CreateTemp(dir, ".snapshot-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(payload); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// Never overwrite an existing snapshot on task resume.
	return os.Link(f.Name(), p)
}
func readArchive(raw []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(raw))
	var total int64
	for {
		h, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, e
		}
		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}
		if !safeRelative(name) {
			return nil, fmt.Errorf("unsafe path %q", name)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("unsafe archive type %q", name)
		}
		if h.Size < 0 || h.Size > 48<<20 {
			return nil, errors.New("oversized archive file")
		}
		total += h.Size
		if total > 48<<20 {
			return nil, errors.New("oversized archive")
		}
		if _, ok := out[name]; ok {
			return nil, errors.New("duplicate archive entry")
		}
		b, e := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if e != nil {
			return nil, e
		}
		if int64(len(b)) != h.Size {
			return nil, errors.New("archive size mismatch")
		}
		out[name] = b
	}
	return out, nil
}
func safeRelative(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return false
	}
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if p == "." || p == ".." || p == "" || p == ".git" || p == ".dexster" || p == ".kraken" || p == ".go-cache" || p == ".dexster-worker-staged" {
			return false
		}
	}
	return true
}
func exportWorker(root string) ([]byte, error) {
	cmd := exec.Command("sudo", "-n", "-u", "dexster-worker", "/usr/local/libexec/dexster-worker-export", "-id", workID(root))
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("export unavailable: %w", e)
	}
	if len(b) > 50<<20 {
		return nil, errors.New("Worker export too large")
	}
	return b, nil
}
func isText(b []byte) bool { return utf8.Valid(b) && !bytes.ContainsRune(b, '\x00') }
func diffText(name string, old, new []byte) string {
	a, e := os.CreateTemp("", "dexster-old-")
	if e != nil {
		return "diff unavailable"
	}
	defer os.Remove(a.Name())
	defer a.Close()
	b, e := os.CreateTemp("", "dexster-new-")
	if e != nil {
		return "diff unavailable"
	}
	defer os.Remove(b.Name())
	defer b.Close()
	a.Write(old)
	b.Write(new)
	a.Close()
	b.Close()
	cmd := exec.Command("/usr/bin/diff", "-u", "--label", "original/"+name, "--label", "worker/"+name, a.Name(), b.Name())
	data, _ := cmd.Output()
	if len(data) > 32000 {
		return string(data[:32000]) + "\n...diff truncated..."
	}
	return string(data)
}
func loadReview(root, task string) (reviewInfo, map[string][]byte, map[string][]byte, error) {
	var out reviewInfo
	p, e := reviewSnapshotPath(root, task)
	if e != nil {
		return out, nil, nil, e
	}
	raw, e := os.ReadFile(p)
	if e != nil {
		return out, nil, nil, fmt.Errorf("нет исходного снимка: запуск задачи должен быть из v2.9: %w", e)
	}
	base, e := readArchive(raw)
	if e != nil {
		return out, nil, nil, e
	}
	exported, e := exportWorker(root)
	if e != nil {
		return out, nil, nil, e
	}
	current, e := readArchive(exported)
	if e != nil {
		return out, nil, nil, e
	}
	keys := map[string]bool{}
	for k := range base {
		keys[k] = true
	}
	for k := range current {
		keys[k] = true
	}
	list := make([]string, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	sort.Strings(list)
	digest := sha256.New()
	digest.Write(raw)
	digest.Write(exported)
	out.Token = hex.EncodeToString(digest.Sum(nil))
	for _, k := range list {
		a, aok := base[k]
		b, bok := current[k]
		if aok && bok && bytes.Equal(a, b) {
			continue
		}
		if !safeRelative(k) {
			return out, nil, nil, errors.New("unsafe changed path")
		}
		if !bok {
			return out, nil, nil, fmt.Errorf("удаление %q требует отдельной операции (не применяется автоматически)", k)
		}
		if !isText(b) || !isText(a) {
			return out, nil, nil, fmt.Errorf("бинарный файл %q нельзя автоматически применить", k)
		}
		kind := "modified"
		if !aok {
			kind = "new"
		}
		out.Files = append(out.Files, reviewFile{Path: k, Kind: kind, Diff: diffText(k, a, b)})
		if len(out.Files) > 100 {
			return out, nil, nil, errors.New("more than 100 changed files")
		}
	}
	if len(out.Files) == 0 {
		out.Detail = "Изменений относительно исходной копии нет"
	} else {
		out.Detail = fmt.Sprintf("Изменено файлов: %d · исходник не изменён", len(out.Files))
	}
	return out, base, current, nil
}
func validateTarget(root, rel string, exists bool) (string, error) {
	if !safeRelative(rel) {
		return "", errors.New("unsafe path")
	}
	path := filepath.Join(root, rel)
	realRoot, e := canonicalPath(root)
	if e != nil {
		return "", e
	}
	if realRoot != root {
		return "", errors.New("source project path changed; refusing apply")
	}
	parent := filepath.Dir(path)
	for d := parent; d != root; d = filepath.Dir(d) {
		info, e := os.Lstat(d)
		if e != nil {
			return "", fmt.Errorf("parent missing: %w", e)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe parent directory")
		}
	}
	info, e := os.Lstat(path)
	if exists {
		if e != nil || !info.Mode().IsRegular() {
			return "", errors.New("source file changed type or disappeared")
		}
	}
	if !exists && !os.IsNotExist(e) {
		return "", errors.New("new file path already exists")
	}
	return path, nil
}
func applyReview(root string, base, current map[string][]byte) error {
	type update struct {
		path   string
		data   []byte
		exists bool
		mode   os.FileMode
	}
	var todo []update
	for rel, now := range current {
		before, was := base[rel]
		if was && bytes.Equal(before, now) {
			continue
		}
		path, e := validateTarget(root, rel, was)
		if e != nil {
			return fmt.Errorf("%s: %w", rel, e)
		}
		if was {
			f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if e != nil {
				return e
			}
			existing, e := io.ReadAll(io.LimitReader(f, 49<<20))
			f.Close()
			if e != nil {
				return e
			}
			if !bytes.Equal(existing, before) {
				return fmt.Errorf("исходник изменился: %s; применение отменено", rel)
			}
		}
		mode := os.FileMode(0600)
		if was {
			info, e := os.Lstat(path)
			if e != nil {
				return e
			}
			mode = info.Mode().Perm() & 0700
		}
		todo = append(todo, update{path, now, was, mode})
	}
	for rel := range base {
		if _, ok := current[rel]; !ok {
			return fmt.Errorf("delete refused: %s", rel)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].path < todo[j].path })
	// All validation happens before writing; no overwrite on any detected conflict.
	for _, u := range todo {
		dir := filepath.Dir(u.path)
		f, e := os.CreateTemp(dir, ".dexster-apply-*")
		if e != nil {
			return e
		}
		temp := f.Name()
		if e = f.Chmod(u.mode); e != nil {
			f.Close()
			os.Remove(temp)
			return e
		}
		if _, e = f.Write(u.data); e != nil {
			f.Close()
			os.Remove(temp)
			return e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			os.Remove(temp)
			return e
		}
		if e = f.Close(); e != nil {
			os.Remove(temp)
			return e
		}
		// Do not silently overwrite a symlink created while we prepared the temp file.
		if _, e = validateTarget(root, strings.TrimPrefix(strings.TrimPrefix(u.path, root), string(filepath.Separator)), u.exists); e != nil {
			os.Remove(temp)
			return e
		}
		if e = os.Rename(temp, u.path); e != nil {
			os.Remove(temp)
			return e
		}
	}
	return nil
}
func (c *controller) reviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/review", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		s := c.snapshot()
		if s.Status != "done" || s.Config.Runtime != "worker" || !s.Config.AutoCommit || !strings.Contains(s.Log, "verified Git commit") {
			http.Error(w, "Нет проверенного завершённого Worker-задания", 409)
			return
		}
		result, _, _, e := loadReview(s.Config.Root, s.Config.Task)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/api/review/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Dexster-Token") != c.token {
			http.Error(w, "forbidden", 403)
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if e := json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&req); e != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cmd != nil || c.s.Status != "done" || c.s.Config.Runtime != "worker" || !c.s.Config.AutoCommit || !strings.Contains(c.s.Log, "verified Git commit") {
			http.Error(w, "Worker task not ready", 409)
			return
		}
		info, base, current, e := loadReview(c.s.Config.Root, c.s.Config.Task)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if req.Token == "" || req.Token != info.Token {
			http.Error(w, "Review stale; reload diff before apply", 409)
			return
		}
		if len(info.Files) == 0 {
			http.Error(w, "Nothing to apply", 409)
			return
		}
		if e = applyReview(c.s.Config.Root, base, current); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
