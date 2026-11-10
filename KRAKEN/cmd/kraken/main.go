package main

import (
	"flag"
	"fmt"
	"kraken/memory"
	"os"
	"strings"
	"time"
)

func main() {
	root := flag.String("root", "", "KRAKEN directory")
	project := flag.String("project", "", "project directory")
	cp := flag.String("checkpoint", "", "full path to checkpoint JSON")
	baselineTask := flag.String("pin-git-baseline", "", "pin clean HEAD under refs/kraken/baselines for task ID")
	taskCPs := flag.String("task-checkpoints", "", "comma separated checkpoint JSON paths for one task")
	projectRollback := flag.String("project-rollback", "", "project rollback target: previous or first")
	cleanup := flag.Bool("cleanup", false, "preview pruning old completed-task checkpoints")
	recoverCleanup := flag.Bool("recover-cleanup", false, "resume interrupted checkpoint cleanup; requires -apply")
	retention := flag.Int("retention-days", 30, "minimum age for completed-task checkpoint cleanup")
	apply := flag.Bool("apply", false, "perform restore (default: preview)")
	flag.Parse()
	if *projectRollback != "" {
		if *root == "" || *project == "" || *cp != "" || *taskCPs != "" || *baselineTask != "" || *cleanup || *recoverCleanup {
			flag.Usage()
			os.Exit(2)
		}
		mode := memory.ProjectRollbackMode(*projectRollback)
		if mode != memory.RollbackPrevious && mode != memory.RollbackFirst {
			fmt.Fprintln(os.Stderr, "REFUSED: project-rollback must be previous or first")
			os.Exit(2)
		}
		s, err := memory.New(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		lock, err := s.LockExclusive()
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		defer lock.Close()
		plan, err := s.PreviewProjectRollback(*project, mode)
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		fmt.Printf("PROJECT ROLLBACK: mode=%s strategy=%s target_task=%s\n", mode, plan.Strategy, plan.TargetTaskID)
		if !*apply {
			fmt.Println("PREVIEW ONLY. Pass -apply to restore the project.")
			return
		}
		result, err := s.ApplyProjectRollback(plan)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAILED:", err)
			os.Exit(1)
		}
		fmt.Printf("RESTORED PROJECT: files=%d deleted=%d safety=%s\n", result.RestoredFiles, result.DeletedFiles, result.SafetySnapshot)
		return
	}
	if *recoverCleanup {
		if *root == "" || !*apply || *cleanup || *project != "" || *cp != "" || *taskCPs != "" || *baselineTask != "" {
			flag.Usage()
			os.Exit(2)
		}
		s, err := memory.New(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		lock, err := s.LockExclusive()
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		defer lock.Close()
		n, err := s.RecoverCleanup()
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		fmt.Printf("RECOVERED CHECKPOINTS: %d\n", n)
		return
	}
	if *cleanup {
		if *root == "" || *project != "" || *cp != "" || *taskCPs != "" || *baselineTask != "" || *retention <= 0 {
			flag.Usage()
			os.Exit(2)
		}
		s, err := memory.New(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		lock, err := s.LockExclusive()
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		defer lock.Close()
		now := time.Now().UTC()
		age := time.Duration(*retention) * 24 * time.Hour
		plan, err := s.PreviewCleanup(age, now)
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		for _, c := range plan.Candidates {
			fmt.Printf("CANDIDATE: %s task=%s\n", c.Checkpoint, c.TaskID)
		}
		fmt.Printf("CANDIDATES: %d PROTECTED: %d\n", len(plan.Candidates), plan.Protected)
		if !*apply {
			fmt.Println("PREVIEW ONLY. Pass -apply to prune completed-task checkpoints.")
			return
		}
		n, err := s.ApplyCleanup(age, now, plan)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAILED:", err)
			os.Exit(1)
		}
		fmt.Printf("PRUNED CHECKPOINTS: %d\n", n)
		return
	}
	if *project == "" || (*baselineTask == "" && (*root == "" || (*cp == "" && *taskCPs == "") || (*cp != "" && *taskCPs != ""))) {
		flag.Usage()
		os.Exit(2)
	}
	if *baselineTask != "" {
		if *cp != "" || *taskCPs != "" || *apply {
			flag.Usage()
			os.Exit(2)
		}
		result, err := memory.PinGitBaseline(*project, *baselineTask)
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		fmt.Printf("LOCAL GIT BASELINE: %s → %s\n", result.Ref, result.Commit)
		return
	}
	s, err := memory.New(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lock, err := s.LockExclusive()
	if err != nil {
		fmt.Fprintln(os.Stderr, "REFUSED:", err)
		os.Exit(1)
	}
	defer lock.Close()
	if *taskCPs != "" {
		files := strings.Split(*taskCPs, ",")
		plan, err := s.PreviewTaskRollback(files, *project)
		if err != nil {
			fmt.Fprintln(os.Stderr, "REFUSED:", err)
			os.Exit(1)
		}
		for _, f := range plan.Files {
			fmt.Printf("SAFE TO RESTORE: %s (delete_created=%v)\n", f.Path, f.Created)
		}
		if !*apply {
			fmt.Println("PREVIEW ONLY. Pass -apply to restore.")
			return
		}
		if err := s.ApplyTaskRollback(files, *project); err != nil {
			fmt.Fprintln(os.Stderr, "FAILED:", err)
			os.Exit(1)
		}
		fmt.Println("RESTORED TASK FILES:", len(plan.Files))
		return
	}
	plan, err := s.PreviewRollback(*cp, *project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "REFUSED:", err)
		os.Exit(1)
	}
	fmt.Printf("SAFE TO RESTORE: %s (delete_created=%v)\n", plan.Path, plan.Created)
	if !*apply {
		fmt.Println("PREVIEW ONLY. Pass -apply to restore.")
		return
	}
	if err := s.ApplyRollback(*cp, *project); err != nil {
		fmt.Fprintln(os.Stderr, "REFUSED:", err)
		os.Exit(1)
	}
	fmt.Println("RESTORED:", plan.Path)
}
