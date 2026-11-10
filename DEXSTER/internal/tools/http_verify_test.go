package tools

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func httpFixture(t *testing.T, src string) *Router {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module httptestfixture\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	r.VerifyMode = "http_server"
	return r
}
func TestHTTPVerificationPass(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := httpFixture(t, `package main
import "net/http"
func main(){ http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Write([]byte("ok"))}); http.ListenAndServe("`+addr+`",nil) }
`)
	r.VerifyHTTPURL = "http://" + addr + "/"
	result := r.RunTests()
	if !result.OK || !strings.Contains(result.Output, "PASS HTTP 200") {
		t.Fatalf("%+v", result)
	}
}
func TestHTTPVerificationBuildError(t *testing.T) {
	r := httpFixture(t, "package main\nfunc main(){ _ = ctx }\n")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.VerifyHTTPURL = "http://" + l.Addr().String() + "/"
	l.Close()
	result := r.RunTests()
	if result.OK || !strings.Contains(result.Output, "undefined: ctx") {
		t.Fatalf("%+v", result)
	}
}
func TestHTTPVerificationOccupiedPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := httpFixture(t, "package main\nfunc main(){}\n")
	r.VerifyHTTPURL = "http://" + l.Addr().String() + "/"
	result := r.RunTests()
	if result.OK || !strings.Contains(result.Error, "already occupied") {
		t.Fatalf("%+v", result)
	}
}

func TestHTTPVerificationRequiredContent(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := httpFixture(t, `package main
import "net/http"
func main(){ http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Write([]byte("HELLO WORLD"))}); http.ListenAndServe("`+addr+`",nil) }
`)
	r.VerifyHTTPURL = "http://" + addr + "/"
	r.VerifyHTTPContains = "NEON FORGE"
	result := r.RunTests()
	if result.OK || !strings.Contains(result.Error, "missing required text") {
		t.Fatalf("expected content mismatch: %+v", result)
	}
	r.VerifyHTTPContains = "HELLO WORLD"
	result = r.RunTests()
	if !result.OK {
		t.Fatalf("expected content match: %+v", result)
	}
}
