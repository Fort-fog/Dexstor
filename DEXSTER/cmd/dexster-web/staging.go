package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxStageBytes int64 = 48 << 20

func stageProject(root string) ([]byte, error) {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	var total int64
	err := filepath.WalkDir(root, func(path string, de os.DirEntry, walkerr error) error {
		if walkerr != nil {
			return walkerr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Avoid leaking .git config/credentials and huge dependencies to worker.
		if de.IsDir() && (de.Name() == "node_modules" || de.Name() == "vendor" || de.Name() == ".git" || de.Name() == ".cache" || de.Name() == ".dexster") {
			return filepath.SkipDir
		}
		info, err := de.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported symlink/special file: %s", rel)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxStageBytes {
				return errors.New("project exceeds 48 MiB staging limit")
			}
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		// Modes never import setuid, setgid, sticky bits.
		if info.IsDir() {
			hdr.Mode = 0700
		} else if info.Mode()&0111 != 0 {
			hdr.Mode = 0700
		} else {
			hdr.Mode = 0600
		}
		if err = tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func workID(root string) string {
	b := sha256.Sum256([]byte(strings.TrimSpace(root)))
	return "project-" + hex.EncodeToString(b[:8])
}
