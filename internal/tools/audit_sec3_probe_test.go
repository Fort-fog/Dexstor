// internal/tools/audit_sec3_probe_test.go
//
// SEC-3 AUDIT PROBES (read-only audit, production code untouched).
// These tests document the ACTUAL behaviour of Router.Exec / AssertOutput /
// safePath against escape, symlink and shell-indirection scenarios.
// Every probe asserts the observed outcome so the report is reproducible.
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newAuditPair creates a sandbox where files directly under base are
// reachable from the Router root via "..":
//
//	base/escape.txt   -> "../escape.txt" as seen from project/
//	base/project/     -> Router root
//	base/outside/     -> extra dir (symlink targets)
func newAuditPair(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "project")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

// escapeFile writes base/escape.txt; relative to root it is "../escape.txt".
func escapeFile(t *testing.T, root, content string) (abs, relFromRoot string) {
	t.Helper()
	abs = filepath.Join(filepath.Dir(root), "escape.txt")
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs, "../escape.txt"
}

func newAuditRouter(t *testing.T) (*Router, string, string) {
	t.Helper()
	root, outside := newAuditPair(t)
	r := New(root)
	return r, root, outside
}

// Scenario 1: Exec reading a file OUTSIDE r.Root via ../ relative path.
func TestAuditExecReadOutsideRoot(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	_, rel := escapeFile(t, root, "TOP-SECRET")

	res := r.Exec("cat " + rel)
	t.Logf("exit=%d status=%s output=%q error=%q", res.Exit, res.Status, res.Output, res.Error)

	// OBSERVED: cmd.Dir=r.Root does NOT confine the shell; "../" resolves
	// above the root, the content is leaked and the result is PASS.
	if !res.OK || !strings.Contains(res.Output, "TOP-SECRET") {
		t.Fatalf("expected escape-read leak to be reproducible, got %+v", res)
	}
}

// Scenario 2: Exec writing OUTSIDE r.Root via absolute path.
func TestAuditExecWriteAbsoluteOutsideRoot(t *testing.T) {
	r, _, outside := newAuditRouter(t)
	victim := filepath.Join(outside, "victim.txt")

	res := r.Exec("echo pwned > " + victim)
	data, err := os.ReadFile(victim)
	t.Logf("exec exit=%d status=%s; outside file written=%q readErr=%v",
		res.Exit, res.Status, string(data), err)

	// OBSERVED: absolute path is NOT checked by Exec at all; write succeeds.
	if err != nil || strings.TrimSpace(string(data)) != "pwned" {
		t.Fatalf("expected outside write to succeed, got %q (%v)", data, err)
	}
	if !res.OK {
		t.Fatalf("expected exec to report PASS for the escaping write, got %+v", res)
	}
}

// Scenario 3: Read tool (safePath) with ../ — must be blocked.
func TestAuditReadToolDotDotBlocked(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	_, rel := escapeFile(t, root, "TOP-SECRET")
	res := r.Read(rel)
	t.Logf("Read(../secret.txt): status=%s error=%q", res.Status, res.Error)
	if res.OK {
		t.Fatalf("expected safePath to block ../ read, got OK %+v", res)
	}
}

// Scenario 4: Write tool with ../ — must be blocked.
func TestAuditWriteToolDotDotBlocked(t *testing.T) {
	r, _, outside := newAuditRouter(t)
	res := r.Write("../evil.go", "package main")
	t.Logf("Write(../evil.go): status=%s error=%q", res.Status, res.Error)
	if res.OK {
		t.Fatalf("expected safePath to block ../ write, got OK %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.go")); err == nil {
		t.Fatalf("file was created outside root despite safePath")
	}
}

// Scenario 5: symlink INSIDE root pointing OUTSIDE.
// 5a: Read tool must refuse symlink component.
// 5b: Exec through the symlink escapes anyway (guard only knows protected files).
func TestAuditSymlinkEscape(t *testing.T) {
	r, root, outside := newAuditRouter(t)
	secret := filepath.Join(outside, "secret.txt") // absolute symlink target
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	readRes := r.Read("link.txt")
	t.Logf("Read(link.txt): status=%s error=%q", readRes.Status, readRes.Error)
	if readRes.OK {
		t.Fatalf("expected safePath to reject symlinked path, got OK")
	}

	execRes := r.Exec("cat link.txt")
	t.Logf("Exec(cat link.txt): ok=%v output=%q", execRes.OK, execRes.Output)
	// OBSERVED: Exec follows the symlink and leaks the outside content.
	if !execRes.OK || !strings.Contains(execRes.Output, "TOP-SECRET") {
		t.Fatalf("expected Exec symlink leak to be reproducible, got %+v", execRes)
	}
}

// Scenario 5c: Exec WRITES through symlink into an outside file that is not
// in the protected snapshot -> silent escape with PASS.
func TestAuditSymlinkWriteEscapeSilent(t *testing.T) {
	r, root, outside := newAuditRouter(t)
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "w.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	res := r.Exec("echo tampered > w.txt")
	data, _ := os.ReadFile(victim)
	t.Logf("exec ok=%v; outside victim now %q", res.OK, string(data))
	// OBSERVED: write-through-symlink succeeds, result PASS, no violation.
	if strings.TrimSpace(string(data)) != "tampered" || !res.OK {
		t.Fatalf("expected silent symlink write escape, got exec=%+v victim=%q", res, data)
	}
}

// Scenario 6: shell indirection around the text pre-check.
func TestAuditShellIndirectionPreCheckBypass(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	// test.pl exists and is protected.
	if err := os.WriteFile(filepath.Join(root, "test.pl"), []byte("print 'PASS';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ProtectedPath["test.pl"] = true

	vars := map[string]string{
		// "variable" and "subshell" still literally contain "test.pl", so the
		// text pre-check catches them; kept as baseline behaviour.
		"variable":    `f=test.pl; echo x > $f`,
		"subshell":    `echo x > $(echo test.pl)`,
		"brace":       `f=test${EMPTY}.pl; echo x > ${f}`,
		"glob":        `echo x > te*t.pl`,
		"split-token": `echo x >> tes""t.pl`,
	}
	for name, cmd := range vars {
		pre, blocked := r.commandTargetsProtectedPath(cmd)
		res := r.Exec(cmd)
		data, _ := os.ReadFile(filepath.Join(root, "test.pl"))

		if blocked {
			// Baseline: literal token present -> pre-check blocks before exec.
			t.Logf("[%s] precheck-blocked=%v(%q) exec-ok=%v error=%q final-content=%q",
				name, blocked, pre, res.OK, res.Error, string(data))
			if res.OK {
				t.Errorf("[%s] blocked command must not report OK", name)
			}
			continue
		}

		t.Logf("[%s] precheck-blocked=false exec-ok=%v integrity-error=%q final-content=%q",
			name, res.OK, res.Error, string(data))

		// Indirection evaded the text match.
		if name == "glob" {
			// AUDIT FINDING VULN-4 (SEC-2 GAP): the snapshot tracks literal
			// protected paths; "te*t.pl" mutates test.pl but neither layer
			// fires and Exec reports PASS. Documented, not asserted green.
			if res.OK {
				t.Logf("[%s] KNOWN GAP: glob bypass produced OK/PASS while test.pl=%q", name, string(data))
			} else {
				t.Errorf("[%s] glob unexpectedly blocked — update audit findings", name)
			}
			continue
		}
		// All other non-literal indirections MUST be caught by SEC-2.
		if res.OK || res.Exit == 0 {
			t.Errorf("[%s] expected integrity guard to force FAIL, got %+v", name, res)
		}
		if !strings.Contains(res.Error, "integrity violation") {
			t.Errorf("[%s] expected integrity violation message, got %q", name, res.Error)
		}
		// NOTE: file on disk IS already modified — guard is report-only.
	}
}

// Scenario 7: Exec deleting the protected file entirely.
func TestAuditExecDeleteProtectedDetected(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	if err := os.WriteFile(filepath.Join(root, "solution_test.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.Exec("rm solution_test.go")
	t.Logf("rm protected: ok=%v error=%q", res.OK, res.Error)
	if res.OK || !strings.Contains(res.Error, "integrity violation") {
		t.Fatalf("expected deletion to be detected as violation, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "solution_test.go")); err == nil {
		t.Logf("file still present")
	} else {
		t.Logf("OBSERVED: file remains DELETED on disk after detection (report-only)")
	}
}

// Scenario 8: deep nested *_test.go beyond the 2-level walk limit.
func TestAuditDeepTestFileNotSnapshotted(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	deep := filepath.Join(root, "a", "b", "c", "deep_test.go") // depth 3 below root
	if err := os.MkdirAll(filepath.Dir(deep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deep, []byte("package c\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := r.Exec("echo tampered > a/b/c/deep_test.go")
	data, _ := os.ReadFile(deep)
	t.Logf("deep mutation: exec-ok=%v content=%q", res.OK, string(data))
	// OBSERVED: maxDepth caps the walk; deeper files are invisible to the guard.
	if res.OK && strings.TrimSpace(string(data)) == "tampered" {
		t.Logf("CONFIRMED: *_test.go deeper than 2 levels is NOT protected by SEC-2 snapshot")
	}
}

// Scenario 9: AssertOutput parity with Exec.
func TestAuditAssertOutputParity(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	_, rel := escapeFile(t, root, "TOP-SECRET")

	// Escape read: same PASS-with-leak behaviour as Exec.
	res := r.AssertOutput("cat "+rel, "TOP-SECRET")
	t.Logf("AssertOutput escape-read: ok=%v output=%q", res.OK, res.Output)
	if !res.OK {
		t.Fatalf("expected identical escape behaviour vs Exec, got %+v", res)
	}

	// Protected mutation WITHOUT a literal protected token ("split-token"
	// form): pre-check cannot fire, so BOTH tools must fail through the same
	// SEC-2 integrity layer — this is what proves behavioural parity.
	if err := os.WriteFile(filepath.Join(r.Root, "test.pl"), []byte("print 'PASS';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ProtectedPath["test.pl"] = true
	cmd := `echo x >> tes""t.pl`
	execRes := r.Exec(cmd)
	assertRes := r.AssertOutput(cmd, "whatever")
	t.Logf("parity: exec ok=%v err=%q | assert_output ok=%v err=%q",
		execRes.OK, execRes.Error, assertRes.OK, assertRes.Error)
	for _, tc := range []struct {
		tool string
		res  Result
	}{{"exec", execRes}, {"assert_output", assertRes}} {
		if tc.res.OK || !strings.Contains(tc.res.Error, "integrity violation") {
			t.Errorf("%s: expected identical integrity-violation FAIL, got %+v", tc.tool, tc.res)
		}
	}
}

// Scenario 10: Exec touching nothing protected -> normal workflow intact.
func TestAuditNormalWorkflowUnaffected(t *testing.T) {
	r, root, _ := newAuditRouter(t)
	if err := os.WriteFile(filepath.Join(root, "math.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.Exec("echo 'package main' > math.go && go vet ./... 2>/dev/null; true")
	t.Logf("normal workflow: ok=%v", res.OK)
	if !res.OK {
		t.Fatalf("normal allowed command broke: %+v", res)
	}
}
