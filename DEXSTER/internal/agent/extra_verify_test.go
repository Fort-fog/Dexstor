package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dexster/internal/llm"
)

func makeExtraAgent(t *testing.T, src string) *Agent {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module extratest\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample_test.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := New(root, "dummy", llm.New("http://localhost:11434"), 20)
	if err != nil {
		t.Fatal(err)
	}
	a.VerifyGoTags = "dexster_extra"
	a.AutoVerify = true
	a.State = &TaskState{Evidence: Evidence{Inspected: true, WrotePath: "sample.go", LastWriteIter: 1, LastDiffIter: 2, LastTestIter: 3}}
	return a
}

func TestExtraGoTestsRequiredBeforeDone(t *testing.T) {
	a := makeExtraAgent(t, "//go:build dexster_extra\n\npackage extratest\nimport \"testing\"\nfunc TestExtra(t *testing.T) {}\n")
	if a.gateComplete() {
		t.Fatal("DONE before extra tests")
	}
	if check, ok := a.nextVerification(); !ok || check != "go_test_extra" {
		t.Fatalf("next=%q %v", check, ok)
	}
	r := a.runExtraGoTests()
	if !r.OK {
		t.Fatalf("extra checks failed: %s %s", r.Output, r.Error)
	}
	a.applyEvidence(llm.Call{Name: "go_test_extra"}, r, 4)
	if !a.gateComplete() {
		t.Fatal("DONE should be allowed after extra tests PASS")
	}
	a.State.Evidence.LastWriteIter = 5
	a.State.Evidence.LastDiffIter = 6
	a.State.Evidence.LastTestIter = 7
	if a.gateComplete() {
		t.Fatal("stale extra PASS accepted after new write")
	}
}

func TestExtraGoTestsFailsClosed(t *testing.T) {
	a := makeExtraAgent(t, "package extratest\nimport \"testing\"\nfunc TestOrdinary(t *testing.T) {}\n")
	r := a.runExtraGoTests()
	if r.OK || !strings.Contains(r.Error, "no test file") {
		t.Fatalf("missing tagged test: %+v", r)
	}
}
func TestExtraGoTestsRejectsFailure(t *testing.T) {
	a := makeExtraAgent(t, "//go:build dexster_extra\n\npackage extratest\nimport \"testing\"\nfunc TestExtra(t *testing.T) {t.Fatal(\"boundary rejected\")}\n")
	r := a.runExtraGoTests()
	if r.OK || !strings.Contains(r.Output, "boundary rejected") {
		t.Fatalf("unexpected result: %+v", r)
	}
	a.record(4, "go_test_extra", "", r.Output, false, 1)
	if a.gateComplete() {
		t.Fatal("DONE on failed extra tests")
	}
	if _, yes := a.nextVerification(); yes {
		t.Fatal("repeated failed extra test without a write")
	}
}
