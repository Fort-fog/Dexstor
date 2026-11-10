package agent

import (
	"dexster/internal/llm"
	"encoding/json"
	"kraken/memory"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSTRATAProviderEndToEndRepairWithKRAKEN(t *testing.T) {
	root := newFixture(t)
	gitBaseline(t, root)
	memroot := t.TempDir()
	if err := os.Mkdir(filepath.Join(memroot, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(memroot)
	if err != nil {
		t.Fatal(err)
	}
	calls, unloads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/unload" {
			unloads++
			w.Write([]byte(`{"loaded":false}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Messages) != 1 {
			t.Fatal("no prompt")
		}
		prompt := req.Messages[0].Content
		response := `{"name":"read_file","path":"math.go","content":"","source":""}`
		if strings.Contains(prompt, "reads=[math.go]") {
			response = `{"name":"write_file","path":"math.go","content":"package main\nfunc Add(a,b int) int { return 10 }\n","source":""}`
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 256, "completion_tokens": 24}})
		w.Write(data)
	}))
	defer server.Close()
	client := llm.New(server.URL + "/v1")
	client.Provider = "strata"
	a, err := New(root, "test-model", client, 12)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory = store
	a.AutoVerify = true
	task := "Fix math.go Add(2,3) and verify Go tests"
	if err := a.Run(task); err != nil {
		t.Fatal(err)
	}
	if !a.State.Done {
		t.Fatal("not done")
	}
	manifest, err := store.ReadTask(root, task)
	if err != nil || !manifest.Done || manifest.Verified == nil {
		t.Fatalf("missing KRAKEN verification %+v %v", manifest, err)
	}
	if calls > 4 || unloads != 0 {
		t.Fatalf("calls=%d unloads=%d", calls, unloads)
	}
}
