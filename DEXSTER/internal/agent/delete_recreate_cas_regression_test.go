package agent

import (
	"dexster/internal/constraint"
	"dexster/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The production Router enforces CAS. A prior delete must not supply its old hash.
func TestDeleteRecreateInvalidatesTrustedSnapshot(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "task.go")
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, root, "http://127.0.0.1:1", 8)
	a.gate = &constraint.Gate{Root: root}
	a.State = &TaskState{Evidence: Evidence{ReadFiles: map[string]string{"task.go": "old"}}}
	del := llm.Call{Name: "delete_path", Args: map[string]string{"path": "task.go"}}
	result := a.execute(del)
	if !result.OK {
		t.Fatalf("delete: %s", result.Error)
	}
	a.applyEvidence(del, result, 1)
	if _, ok := a.State.Evidence.ReadFiles["task.go"]; ok {
		t.Fatal("old snapshot retained after deletion")
	}
	a.record(1, "delete_path", "task.go", "deleted", true, 0)
	wr := llm.Call{Name: "write_file", Args: map[string]string{"path": "task.go", "content": "new"}}
	result = a.execute(wr)
	if !result.OK {
		t.Fatalf("recreate: %s", result.Error)
	}
	a.applyEvidence(wr, result, 2)
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "new" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// The user's persisted state is older than this fix: delete succeeded but
// read_files still contains the deleted source. Recovery must be automatic.
func TestDeleteRecreateFromLegacyPersistedSnapshot(t *testing.T) {
	root := t.TempDir()
	a := newAgent(t, root, "http://127.0.0.1:1", 8)
	a.gate = &constraint.Gate{Root: root}
	a.State = &TaskState{
		Evidence: Evidence{ReadFiles: map[string]string{"task.go": "old"}},
		Journal: []ActionRecord{
			{Tool: "read_file", Path: "task.go", OK: true},
			{Tool: "delete_path", Path: "task.go", OK: true},
			{Tool: "write_file", Path: "task.go", OK: false, Summary: "new file must be written without a base hash"},
		},
	}
	wr := llm.Call{Name: "write_file", Args: map[string]string{"path": "task.go", "content": "recovered"}}
	res := a.execute(wr)
	if !res.OK {
		t.Fatalf("legacy resume recovery: %s", res.Error)
	}
	data, err := os.ReadFile(filepath.Join(root, "task.go"))
	if err != nil || string(data) != "recovered" {
		t.Fatalf("data %q err=%v", data, err)
	}
}

// Deleting a path does not allow bypassing CAS if another file has appeared.
func TestDeleteRecreateDoesNotOverwriteExternalFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "task.go")
	if err := os.WriteFile(p, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, root, "http://127.0.0.1:1", 8)
	a.gate = &constraint.Gate{Root: root}
	a.State = &TaskState{Evidence: Evidence{ReadFiles: map[string]string{"task.go": "old"}}, Journal: []ActionRecord{{Tool: "delete_path", Path: "task.go", OK: true}}}
	wr := llm.Call{Name: "write_file", Args: map[string]string{"path": "task.go", "content": "unsafe"}}
	res := a.execute(wr)
	if res.OK || !strings.Contains(res.Error, "stale snapshot") {
		t.Fatalf("expected CAS refusal, got %+v", res)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "external" {
		t.Fatal("external file overwritten")
	}
}
