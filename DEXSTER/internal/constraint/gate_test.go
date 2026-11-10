package constraint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const initial = `package main
func Add(a,b int) int { return a+b }
func Multiply(a,b int) int { return a+b }
`

func makeGate(t *testing.T) (string, *Gate) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := Capture(root, []string{"main.go:Multiply"})
	if err != nil {
		t.Fatal(err)
	}
	return root, g
}
func TestFunctionGuardAllowsOtherChanges(t *testing.T) {
	_, g := makeGate(t)
	changed := strings.Replace(initial, "return a+b }", "return 10 }", 1)
	if err := g.CheckWrite("main.go", []byte(changed)); err != nil {
		t.Fatal(err)
	}
}
func TestFunctionGuardRejectsForbiddenModification(t *testing.T) {
	_, g := makeGate(t)
	changed := strings.Replace(initial, "func Multiply(a,b int) int { return a+b }", "func Multiply(a,b int) int { return a*b }", -1)
	if err := g.CheckWrite("main.go", []byte(changed)); err == nil {
		t.Fatal("should reject changed Multiply")
	}
}
func TestDetectExternalEditsOnCompletion(t *testing.T) {
	root, g := makeGate(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Replace(initial, "return a+b }", "return a*b }", -1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.Verify(); err == nil {
		t.Fatal("should reject external modification")
	}
}
func TestRestoreUsesOldBaseline(t *testing.T) {
	root, g := makeGate(t)
	saved := append([]Rule(nil), g.Rules...)
	r, err := Restore(root, saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Replace(initial, "func Multiply(a,b int) int { return a+b }", "func Multiply(a,b int) int { return a*b }", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err == nil {
		t.Fatal("baseline bypass")
	}
}
func TestWholeFileGuard(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main_test.go"), []byte("test"), 0600)
	g, err := Capture(root, []string{"main_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.CheckWrite("main_test.go", []byte("evil")); err == nil {
		t.Fatal("wanted rejection")
	}
}
func TestRejectSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "main.go"), []byte(initial), 0600)
	if err := os.Symlink(filepath.Join(outside, "main.go"), filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(root, []string{"main.go:Multiply"}); err == nil {
		t.Fatal("symlink allowed")
	}
}
func TestRejectTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := Capture(root, []string{"../main.go:Multiply"}); err == nil {
		t.Fatal("traversal allowed")
	}
}
func TestMissingFunctionDenied(t *testing.T) {
	_, g := makeGate(t)
	changed := `package main
func Add(a,b int) int{return 10}
`
	if err := g.CheckWrite("main.go", []byte(changed)); err == nil {
		t.Fatal("deletion allowed")
	}
}
