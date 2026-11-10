package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/memory"
)

func TestMeasuredColdRotationAfterAcknowledgement(t *testing.T) {
	project := newFixture(t)
	krakenRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(krakenRoot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(krakenRoot)
	if err != nil {
		t.Fatal(err)
	}
	calls, unloads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt    string `json:"prompt"`
			KeepAlive *int   `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.KeepAlive != nil {
			unloads++
			m, e := store.ReadTask(project, "cold rotation")
			if e != nil || m.Resume == nil || m.AcknowledgedIteration != calls {
				t.Errorf("unload before checkpoint: manifest=%+v err=%v", m, e)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
			return
		}
		calls++
		if calls >= 2 && strings.Contains(req.Prompt, "FILES YOU HAVE READ") {
			t.Error("full snapshot leaked after rotation")
		}
		reply := evidenceDrivenMock()(req.Prompt)
		eval := 200
		if calls == 1 {
			eval = 850
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": reply, "prompt_eval_count": eval, "done": true})
	}))
	defer server.Close()
	a := newAgent(t, project, server.URL, 8)
	a.Memory = store
	a.RotateAtTokens = 800
	a.UnloadOnRotate = true
	if err := a.Run("cold rotation"); err != nil {
		t.Fatal(err)
	}
	if unloads != 1 {
		t.Fatalf("unload count=%d, want 1", unloads)
	}
	if _, err := os.Stat(filepath.Join(project, ".dexster", "state.json")); err != nil {
		t.Fatal(err)
	}
	m, err := store.ReadTask(project, "cold rotation")
	if err != nil || !m.Done {
		t.Fatalf("not completed: %+v, %v", m, err)
	}
}

// The entire logical task continues across repeated unloads, not merely one.
func TestRepeatedColdRotations(t *testing.T) {
	project := newFixture(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	calls, unloads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt    string `json:"prompt"`
			KeepAlive *int   `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.KeepAlive != nil {
			unloads++
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
			return
		}
		calls++
		if calls > 1 && !strings.Contains(req.Prompt, "COLD RESUME") {
			t.Error("missing cold resume marker")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": evidenceDrivenMock()(req.Prompt), "prompt_eval_count": 700, "eval_count": 180, "done": true})
	}))
	defer server.Close()
	a := newAgent(t, project, server.URL, 8)
	a.Memory = store
	a.RotateAtTokens = 800
	a.UnloadOnRotate = true
	if err := a.Run("repeated cold rotation"); err != nil {
		t.Fatal(err)
	}
	if unloads != 4 {
		t.Fatalf("rotations=%d, want 4", unloads)
	}
	if calls != 4 {
		t.Fatalf("model calls=%d, want 4", calls)
	}
}
