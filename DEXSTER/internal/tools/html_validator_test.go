package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateHTMLPureGo(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "one.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEXSTER_VNU_JAR", "/missing-vnu.jar")
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `<!doctype html><html lang="ru"><head><title>ok</title><style>.x:after{content:"<div>"}</style></head><body><div><img src="assets/one.png"></div></body></html>`, true},
		{"missing image", `<html><body><img src="assets/missing.png"></body></html>`, false},
		{"escape image", `<html><body><img src="../Pictures/one.png"></body></html>`, false},
		{"unclosed div", `<html><body><div></body></html>`, false},
		{"bad close", `<html><body><div></span></div></body></html>`, false},
		{"comment", `<html><body><!-- <div> --><p>Hello</p></body></html>`, true},
		{"missing html", `<div>something</div>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			res := r.ValidateHTML("index.html")
			if res.OK != tc.valid {
				t.Fatalf("want valid %v got %+v", tc.valid, res)
			}
		})
	}
}
func TestValidateHTMLRejectsOutsidePaths(t *testing.T) {
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.ValidateHTML("../outside.html").OK {
		t.Fatal("escaped project root")
	}
}
func TestHTMLAssetsMustRemainInsideProjectAndExist(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "one.png"), []byte("fake-image"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "index.html")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		html    string
		allowed bool
	}{
		{`<img src="assets/one.png">`, true},
		{`<img src="assets/missing.png">`, false},
		{`<img src="../Pictures/missing.png">`, false},
		{`<img src="https://example.org/cdn.png">`, true},
	} {
		if err := os.WriteFile(path, []byte(tc.html), 0600); err != nil {
			t.Fatal(err)
		}
		err := r.validateLocalImages("index.html", path)
		if (err == nil) != tc.allowed {
			t.Fatalf("%q: %v", tc.html, err)
		}
	}
	if err := r.validateLocalImages("index.html", path); err != nil && !strings.Contains(err.Error(), "missing") {
		t.Fatal(err)
	}
}
