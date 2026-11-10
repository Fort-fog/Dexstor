package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newHistoryTestStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	kraken := filepath.Join(base, "kraken")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"events", "tasks", "checkpoints", "lessons", "decisions", "sessions"} {
		if err := os.MkdirAll(filepath.Join(kraken, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(kraken)
	if err != nil {
		t.Fatal(err)
	}
	return store, project, kraken
}

func TestProjectHistorySurvivesClearedProjectAndFreshSameTask(t *testing.T) {
	store, project, kraken := newHistoryTestStore(t)
	task := "build 3d site"
	want := []byte("WORKING-3D-V1")
	if err := os.WriteFile(filepath.Join(project, "index.html"), want, 0600); err != nil {
		t.Fatal(err)
	}

	first, err := store.BeginTaskRun(project, task, true)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := store.PrepareVerifiedSnapshot(project, task, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteVerifiedTask(project, task, 1, verified); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterVerifiedProjectPoint(project, task, first.RunID, 1, verified); err != nil {
		t.Fatal(err)
	}

	// Simulate the user's real workflow: throw the project away and begin a
	// new generation in exactly the same directory with exactly the same task.
	if err := os.Remove(filepath.Join(project, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "new.txt"), []byte("NEW-GENERATION"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := store.BeginTaskRun(project, task, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID == first.RunID {
		t.Fatal("fresh run reused RunID")
	}

	archived, err := os.ReadDir(filepath.Join(kraken, "runs", ProjectID(project)))
	if err != nil || len(archived) == 0 {
		t.Fatalf("previous run not archived: %v entries=%d", err, len(archived))
	}

	plan, err := store.PreviewProjectRollback(project, RollbackPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "project_snapshot" {
		t.Fatalf("strategy=%s", plan.Strategy)
	}
	result, err := store.ApplyProjectRollback(plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.RestoredFiles == 0 {
		t.Fatalf("no restored files: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(project, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("restored %q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(project, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new generation file survived rollback: %v", err)
	}
}

func TestExperienceMemoryPairsFailureRecoveryAndVerification(t *testing.T) {
	store, project, _ := newHistoryTestStore(t)
	task := "fix hover"
	fail := Event{Type: "action_recorded", Task: task, Project: project, Iteration: 2, Tool: "replace_in_file", Path: "index.html", Summary: "old text not found", OK: false, Exit: 1}
	if err := store.Record(fail); err != nil {
		t.Fatal(err)
	}
	repair := Event{Type: "change_applied", Task: task, Project: project, Iteration: 3, Tool: "write_file", Path: "index.html", Summary: "wrote index.html", OK: true}
	if err := store.Record(repair); err != nil {
		t.Fatal(err)
	}
	verify := Event{Type: "action_recorded", Task: task, Project: project, Iteration: 4, Tool: "validate_html", Summary: "PASS", OK: true}
	if err := store.Record(verify); err != nil {
		t.Fatal(err)
	}

	hints, err := store.ExperienceHints(project, "replace_in_file", "index.html", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) == 0 {
		t.Fatal("no recovered experience returned")
	}
	joined := strings.Join(hints, "\n")
	for _, want := range []string{"replace_in_file", "write_file", "validate_html"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hint missing %q: %s", want, joined)
		}
	}
}

func TestRollbackPreviousCrossesVerifiedGenerationsInSameDirectory(t *testing.T) {
	store, project, _ := newHistoryTestStore(t)
	task1 := "first project"
	if err := os.WriteFile(filepath.Join(project, "index.html"), []byte("FIRST-3D-WORKING"), 0600); err != nil {
		t.Fatal(err)
	}
	run1, err := store.BeginTaskRun(project, task1, true)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := store.PrepareVerifiedSnapshot(project, task1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteVerifiedTask(project, task1, 1, v1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterVerifiedProjectPoint(project, task1, run1.RunID, 1, v1); err != nil {
		t.Fatal(err)
	}

	// Completely replace the project with another successful generation.
	if err := os.Remove(filepath.Join(project, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "app.txt"), []byte("SECOND-WORKING"), 0600); err != nil {
		t.Fatal(err)
	}
	task2 := "second project"
	run2, err := store.BeginTaskRun(project, task2, true)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := store.PrepareVerifiedSnapshot(project, task2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteVerifiedTask(project, task2, 1, v2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterVerifiedProjectPoint(project, task2, run2.RunID, 1, v2); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PreviewProjectRollback(project, RollbackPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "project_snapshot" || plan.Verified == nil || plan.Verified.TreeSHA256 != v1.TreeSHA256 {
		t.Fatalf("did not select previous generation: %+v", plan)
	}
	if _, err := store.ApplyProjectRollback(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(project, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FIRST-3D-WORKING" {
		t.Fatalf("restored %q", got)
	}
	if _, err := os.Stat(filepath.Join(project, "app.txt")); !os.IsNotExist(err) {
		t.Fatalf("second generation survived: %v", err)
	}
}
