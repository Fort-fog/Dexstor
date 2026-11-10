package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if res := r.Read("../outside.txt"); res.OK || !strings.Contains(res.Error, "escapes") {
		t.Fatalf("traversal read not rejected: %+v", res)
	}
	if res := r.Write("../../etc/evil", "x", ""); res.OK {
		t.Fatal("traversal write must be rejected")
	}
	if res := r.Write("/etc/passwd", "x", ""); res.OK || !strings.Contains(res.Error, "absolute") {
		t.Fatalf("absolute path not rejected: %+v", res)
	}
}

func TestTestFilesCanBeModified(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "math_test.go", "original oracle\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result := r.Write("math_test.go", "new tests\n", hashOf("original oracle\n"))
	if !result.OK {
		t.Fatalf("test files should be writable: %+v", result)
	}
	data, _ := os.ReadFile(filepath.Join(root, "math_test.go"))
	if string(data) != "new tests\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}
func TestNestedTestFilesCanBeModified(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, filepath.Join("pkg", "math_test.go"), "original\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.IsProtected("pkg/math_test.go") || r.IsProtected("math_test.go") {
		t.Fatal("test files should not be protected")
	}
	if result := r.Write("pkg/math_test.go", "new\n", hashOf("original\n")); !result.OK {
		t.Fatalf("nested test write: %+v", result)
	}
}

func TestCASWriteExistingRequiresSnapshot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "v1\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	res := r.Write("a.txt", "v2\n", "")
	if res.OK || !strings.Contains(res.Error, "trusted read snapshot") {
		t.Fatalf("write without snapshot must fail: %+v", res)
	}

	res = r.Write("a.txt", "v2\n", hashOf("stale-content"))
	if res.OK || !strings.Contains(res.Error, "stale snapshot") {
		t.Fatalf("stale snapshot must fail: %+v", res)
	}

	res = r.Write("a.txt", "v2\n", hashOf("v1\n"))
	if !res.OK {
		t.Fatalf("valid CAS write failed: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "v2\n" {
		t.Fatal("file content not updated")
	}
}

func TestNewFileWriteRules(t *testing.T) {
	root := t.TempDir()
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Write("new.txt", "hello\n", "")
	if !res.OK {
		t.Fatalf("new file write failed: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "new.txt"))
	if string(data) != "hello\n" {
		t.Fatal("content mismatch")
	}

	res = r.Write("other.txt", "x\n", hashOf("anything"))
	if res.OK || !strings.Contains(res.Error, "without a base hash") {
		t.Fatalf("new file with base hash must fail: %+v", res)
	}
}

func TestIdempotentWrite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "same\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Write("a.txt", "same\n", hashOf("same\n"))
	if !res.OK || !strings.Contains(res.Output, "no-op") {
		t.Fatalf("idempotent write: %+v", res)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "one\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Write("a.txt", "two\n", hashOf("one\n"))
	if !res.OK {
		t.Fatalf("first write failed: %+v", res)
	}
	for i := 0; i < 4; i++ {
		// Subsequent writes use the new content's hash (idempotent no-ops).
		res = r.Write("a.txt", "two\n", hashOf("two\n"))
		if !res.OK {
			t.Fatalf("write %d failed: %+v", i, res)
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dexster-tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "two\n" {
		t.Fatal("final content wrong")
	}
}

func TestGitDiffFallbackWithoutGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "keep.txt", "stable\n")
	writeFile(t, root, "change.txt", "before\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	res := r.GitDiff()
	if !res.OK || res.Output != "no project changes" {
		t.Fatalf("clean diff: %+v", res)
	}

	writeFile(t, root, "change.txt", "after\n")
	writeFile(t, root, "added.txt", "new\n")
	res = r.GitDiff()
	if !res.OK || !strings.Contains(res.Output, "modified: change.txt") ||
		!strings.Contains(res.Output, "added: added.txt") {
		t.Fatalf("diff fallback wrong: %+v", res)
	}
}

func TestRunTestsGoTestFixture(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "math.go", "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n")
	writeFile(t, root, "math_test.go", "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 10 {\n\t\tt.Fatalf(\"Add(2,3) = %d, want 10\", Add(2, 3))\n\t}\n}\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	res := r.RunTests()
	if res.OK || !strings.Contains(res.Output, "FAIL") {
		t.Fatalf("expected failing test: %+v", res)
	}

	writeFile(t, root, "math.go", "package main\n\nfunc Add(a, b int) int {\n\treturn 10\n}\n")
	res = r.RunTests()
	if !res.OK || !strings.Contains(res.Output, "PASS") {
		t.Fatalf("expected passing test: %+v", res)
	}
}

func TestRunTestsStandaloneProgram(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"PASS\")\n}\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.RunTests()
	if !res.OK || !strings.Contains(res.Output, "PASS") {
		t.Fatalf("standalone PASS program: %+v", res)
	}

	writeFile(t, root, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"WRONG\")\n\tpanic(\"test failed\")\n}\n")
	res = r.RunTests()
	if res.OK {
		t.Fatalf("panicking program must fail: %+v", res)
	}
}

func TestRunTestsNoTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "notes.txt", "no code here\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := r.RunTests()
	if res.OK || !strings.Contains(res.Error, "no test target") {
		t.Fatalf("expected no-test-target error: %+v", res)
	}
}

func TestListFilesSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "x\n")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, filepath.Join(".git", "HEAD"), "ref: refs/heads/main\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".git") || strings.Contains(f, string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
			t.Fatalf(".git must be skipped: %v", files)
		}
	}
	found := false
	for _, f := range files {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a.go missing from listing: %v", files)
	}
}

func TestRejectSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := r.Write("external/evil.go", "BAD", ""); result.OK {
		t.Fatalf("symlink write accepted: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.go")); !os.IsNotExist(err) {
		t.Fatalf("escaped file appeared: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := r.Read("external/secret.go"); result.OK {
		t.Fatalf("symlink read accepted: %+v", result)
	}
}

func TestNestedTestsWritable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0700); err != nil {
		t.Fatal(err)
	}
	path := "internal/oracle_test.go"
	writeFile(t, root, path, "ORACLE")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := r.Write(path, "NEW", hashOf("ORACLE")); !result.OK {
		t.Fatalf("nested test should be writable: %+v", result)
	}
}

func TestV31FileOperationsAndExternalAssetImport(t *testing.T) {
	root := t.TempDir()
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	asset := filepath.Join(external, "photo.bin")
	want := []byte{0, 1, 2, 0xff, 0x7f, 0x00}
	if err := os.WriteFile(asset, want, 0o644); err != nil {
		t.Fatal(err)
	}

	listed := r.ListPath(external)
	if !listed.OK || !strings.Contains(listed.Output, "photo.bin") {
		t.Fatalf("external list failed: %+v", listed)
	}
	if got := r.MakeDir("assets"); !got.OK {
		t.Fatalf("mkdir: %+v", got)
	}
	if got := r.Copy(asset, "assets/photo.bin"); !got.OK {
		t.Fatalf("copy: %+v", got)
	}
	gotBytes, err := os.ReadFile(filepath.Join(root, "assets", "photo.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, want) {
		t.Fatalf("binary copy mismatch: %v", gotBytes)
	}
	if got := r.Move("assets/photo.bin", "assets/renamed.bin"); !got.OK {
		t.Fatalf("move: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "renamed.bin")); err != nil {
		t.Fatal(err)
	}
	if got := r.Delete("assets/renamed.bin"); !got.OK {
		t.Fatalf("delete: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "renamed.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted file, err=%v", err)
	}

	outside := filepath.Join(t.TempDir(), "escape.bin")
	if got := r.Copy(asset, outside); got.OK {
		t.Fatalf("copy destination escaped root: %+v", got)
	}
	if got := r.Delete("."); got.OK {
		t.Fatalf("project-root deletion allowed: %+v", got)
	}
}
