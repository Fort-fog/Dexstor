package main

import (
	"os"
	"path/filepath"
	"testing"

	"kraken/memory"
)

func TestCaptureAnalystPointCreatesIndependentKrakenHistory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	kraken := filepath.Join(base, "kraken")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("before analyst"), 0600); err != nil {
		t.Fatal(err)
	}
	req := analystPlanInput{Root: root, Kraken: kraken, Task: "plan this project", Model: "main-model", AnalystModel: "small-model", AnalystContext: 8192}
	req, err := normalizeAnalystInput(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := captureAnalystPoint(req); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(kraken)
	if err != nil {
		t.Fatal(err)
	}
	points, err := store.ProjectPoints(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Kind != "analyst" || points[0].Source != "analyst" {
		t.Fatalf("points=%+v", points)
	}
}
