package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const limit int64 = 48 << 20

func export(root string, out io.Writer) error {
	tw := tar.NewWriter(out)
	var size int64
	err := filepath.WalkDir(root, func(path string, de os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		name := de.Name()
		if de.IsDir() && (name == ".git" || name == ".kraken" || name == ".dexster" || name == ".go-cache" || name == "node_modules" || name == "vendor" || name == ".cache") {
			return filepath.SkipDir
		}
		if rel == ".dexster-worker-staged" {
			return nil
		}
		info, e := de.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsafe file: %s", rel)
		}
		if info.Mode().IsRegular() {
			size += info.Size()
			if size > limit {
				return errors.New("export exceeds 48 MiB")
			}
		}
		hdr, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Mode = 0700
		} else if info.Mode()&0111 != 0 {
			hdr.Mode = 0700
		} else {
			hdr.Mode = 0600
		}
		if e = tw.WriteHeader(hdr); e != nil {
			return e
		}
		if info.Mode().IsRegular() {
			// No symlink following for the final component.
			f, e := os.OpenFile(path, os.O_RDONLY|0x20000, 0)
			if e != nil {
				return e
			} // O_NOFOLLOW on Linux
			n, ce := io.CopyN(tw, f, info.Size())
			closeErr := f.Close()
			if ce != nil {
				return ce
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() {
				return errors.New("short file")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
func main() {
	u, e := user.Current()
	if e != nil || u.Username != "dexster-worker" || os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "worker-only")
		os.Exit(1)
	}
	id := flag.String("id", "", "worker id")
	flag.Parse()
	if !strings.HasPrefix(*id, "project-") || len(*id) != 24 {
		os.Exit(2)
	}
	b, e := hex.DecodeString((*id)[8:])
	if e != nil || len(b) != 8 {
		os.Exit(2)
	}
	_ = sha256.Size
	root := filepath.Join("/home/dexster-worker/projects", *id)
	if e = export(root, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
