// dexster-worker-run is a root-owned fixed-policy launcher invoked under dexster-worker.
package main

import (
	"archive/tar"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type repeatedStrings []string

func (r *repeatedStrings) String() string     { return strings.Join(*r, ",") }
func (r *repeatedStrings) Set(v string) error { *r = append(*r, v); return nil }

func fail(err error) { fmt.Fprintln(os.Stderr, "WORKER:", err); os.Exit(1) }
func unpack(dst string, r io.Reader) error {
	tr := tar.NewReader(io.LimitReader(r, 50<<20))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return errors.New("invalid archive path")
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > 48<<20 {
				return errors.New("file too large")
			}
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			fd, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
			if err != nil {
				return err
			}
			_, err = io.CopyN(fd, tr, h.Size)
			ce := fd.Close()
			if err != nil {
				return err
			}
			if ce != nil {
				return ce
			}
			if h.Mode&0111 != 0 {
				if err = os.Chmod(target, 0700); err != nil {
					return err
				}
			}
		default:
			return errors.New("archive link/device forbidden")
		}
	}
}
func main() {
	var writeScope repeatedStrings
	if os.Geteuid() == 0 {
		fail(errors.New("run as dexster-worker, never root"))
	}
	me, err := user.Current()
	if err != nil || me.Username != "dexster-worker" {
		fail(errors.New("wrong user"))
	}
	id := flag.String("id", "", "project id")
	task := flag.String("task", "", "task")
	model := flag.String("model", "", "model")
	sock := flag.String("socket", "", "Unix model socket")
	ctx := flag.Int("ctx", 16384, "context")
	budget := flag.Int("budget", 9000, "budget")
	rotate := flag.Int("rotate", 3000, "rotation")
	iterations := flag.Int("iterations", 35, "iterations")
	verify := flag.Bool("verify", true, "verify")
	commit := flag.Bool("commit", false, "commit")
	unload := flag.Bool("unload", false, "unload")
	flag.Var(&writeScope, "scope-write", "frozen allowed project write path (repeatable)")
	flag.Parse()
	if len(*task) == 0 || len(*task) > 40000 || len(*model) == 0 || len(*model) > 200 {
		fail(errors.New("invalid task or model"))
	}
	if !strings.HasPrefix(*id, "project-") || len(*id) != 24 {
		fail(errors.New("invalid project id"))
	}
	for _, c := range (*id)[8:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			fail(errors.New("invalid id"))
		}
	}
	if *ctx < 1024 || *ctx > 262144 || *budget < 0 || *budget > *ctx || *iterations < 1 || *iterations > 500 {
		fail(errors.New("invalid options"))
	}
	if !strings.HasPrefix(*sock, "/tmp/dexster-model-") || !strings.HasSuffix(*sock, "/ollama.sock") {
		fail(errors.New("invalid model socket"))
	}
	if f, err := os.Stat(*sock); err != nil || f.Mode()&os.ModeSocket == 0 {
		fail(errors.New("model channel absent"))
	}
	base := "/home/dexster-worker/projects"
	work := filepath.Join(base, *id)
	if err = os.MkdirAll(base, 0700); err != nil {
		fail(err)
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		fail(err)
	}
	// The host sends a bounded tar; project copy never writes into host source.
	// On resume, existing worker project and persisted state are retained.
	if _, err = os.Stat(filepath.Join(work, ".dexster")); os.IsNotExist(err) {
		// Never extract over a previous (possibly malicious) filesystem tree.
		entries, e := os.ReadDir(work)
		if e != nil {
			fail(e)
		}
		if len(entries) != 0 {
			if _, e := os.Lstat(filepath.Join(work, ".dexster-worker-staged")); e != nil {
				fail(errors.New("existing Worker project without state; refusing unsafe overwrite"))
			}
			io.Copy(io.Discard, io.LimitReader(os.Stdin, 50<<20))
		} else {
			if err = unpack(work, os.Stdin); err != nil {
				fail(err)
			}
			if err = os.WriteFile(filepath.Join(work, ".dexster-worker-staged"), []byte("staged\n"), 0600); err != nil {
				fail(err)
			}
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(os.Stdin, 50<<20))
	}
	if err = os.MkdirAll(filepath.Join(work, ".kraken", "events"), 0700); err != nil {
		fail(fmt.Errorf("initialize Worker KRAKEN: %w", err))
	}
	if gitInfo, e := os.Stat(filepath.Join(work, ".git", "info")); e == nil && gitInfo.IsDir() {
		exclude := filepath.Join(work, ".git", "info", "exclude")
		f, e := os.OpenFile(exclude, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			fail(fmt.Errorf("Worker Git excludes: %w", e))
		}
		_, e = f.WriteString("\n# DEXSTER Worker runtime only\n/.dexster-worker-staged\n/.kraken/\n/.dexster/\n/.go-cache/\n")
		ce := f.Close()
		if e != nil {
			fail(e)
		}
		if ce != nil {
			fail(ce)
		}
	}
	if *commit {
		if _, err = os.Stat(filepath.Join(work, ".git")); os.IsNotExist(err) {
			cmd := exec.Command("/usr/bin/git", "init", "-q", work)
			if b, e := cmd.CombinedOutput(); e != nil {
				fail(fmt.Errorf("git init: %v: %s", e, b))
			}
		}
	}
	agent := "/usr/local/libexec/dexster-agent"
	if _, err = os.Stat(agent); err != nil {
		fail(err)
	}
	args := []string{"--die-with-parent", "--unshare-all", "--new-session", "--cap-drop", "ALL",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib", "/lib", "--ro-bind", "/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/run", "--dir", "/work",
		"--bind", work, "/work", "--ro-bind", agent, "/run/dexster-agent", "--ro-bind", *sock, "/run/ollama.sock",
		"--clearenv", "--setenv", "HOME", "/work", "--setenv", "PATH", "/usr/local/go/bin:/usr/bin:/bin",
		"--setenv", "DEXSTER_OLLAMA_SOCKET", "/run/ollama.sock", "--setenv", "GOCACHE", "/work/.go-cache",
		"--chdir", "/work"}
	if _, e := os.Stat("/usr/local/go"); e == nil {
		args = append(args[:len(args)-2], append([]string{"--ro-bind", "/usr/local/go", "/usr/local/go"}, args[len(args)-2:]...)...)
	}
	// Initialize Git history exclusively inside the sandbox; never run user-controlled
	// Git filters or project hooks on the host.
	entry := []string{"--", "/run/dexster-agent"}
	if *commit {
		entry = []string{"--", "/bin/sh", "-c", `
          git config user.name "DEXSTER Worker" &&
          git config user.email "dexster-worker@localhost" &&
          if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
            git add -A && git -c core.hooksPath=/dev/null commit -qm "Worker baseline"
          fi &&
          exec /run/dexster-agent "$@"
        `, "worker-entry"}
	}
	args = append(args, entry...)
	args = append(args, "-root", "/work", "-task", *task, "-model", *model,
		"-ollama-url", "http://localhost", "-kraken-root", "/work/.kraken", "-num-ctx", strconv.Itoa(*ctx),
		"-prompt-budget", strconv.Itoa(*budget), "-rotate-at", strconv.Itoa(*rotate), "-max-iterations", strconv.Itoa(*iterations),
		"-auto-verify="+strconv.FormatBool(*verify), "-auto-commit="+strconv.FormatBool(*commit), "-unload-on-rotate="+strconv.FormatBool(*unload), "-model-trace=true")
	for _, scope := range writeScope {
		args = append(args, "-scope-write", scope)
	}
	cmd := exec.Command("/usr/bin/bwrap", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}
	if err = cmd.Run(); err != nil {
		fail(err)
	}
}
