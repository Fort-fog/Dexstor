package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// protectedSnapshotEntry records the pre-execution state of one protected
// file. A missing file is a legitimate snapshot state: it means the path was
// not protected yet, so creating it afterwards must be detected as a
// violation too (otherwise `echo x > solution_test.go` would bypass the
// guard simply because the file did not exist when the snapshot was taken).
type protectedSnapshotEntry struct {
	missing bool
	hash    string
}

// protectedIntegrityPaths returns the set of relative paths whose integrity
// must be verified after shell execution. It always includes the static
// ProtectedPath registry and additionally every *_test.go file that exists
// under the project root at snapshot time.
//
// The walk is depth-limited to two directory levels below the root, which is
// enough for the current single-package benchmark layout while keeping the
// per-Exec cost bounded. Only files that already exist are snapshotted, so a
// brand-new *_test.go created later in the run does NOT retroactively join
// the protected set — the normal workflow (agent writing its first test
// file) keeps working.
func (r *Router) protectedIntegrityPaths() []string {
	set := make(map[string]bool)

	for protected := range r.ProtectedPath {
		clean := filepath.Clean(protected)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue
		}
		set[clean] = true
	}

	rootDepth := strings.Count(filepath.Clean(r.Root), string(os.PathSeparator))
	maxDepth := rootDepth + 3 // root itself + two nested package levels

	_ = filepath.WalkDir(r.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable directories must never crash the guard; skipping
			// them is conservative because any actual write into them still
			// lands on disk and is caught by the post-run comparison of
			// known paths.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if path == r.Root {
				return nil
			}
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator))
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !strings.HasSuffix(name, "_test.go") && name != "test.pl" {
			return nil
		}

		rel, relErr := filepath.Rel(r.Root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "..") {
			return nil
		}
		set[rel] = true
		return nil
	})

	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	return paths
}

// snapshotProtectedFiles captures content hashes of all currently protected
// files before a shell command runs. Missing files are recorded as
// missing-but-tracked so creation attempts are also detected.
func (r *Router) snapshotProtectedFiles() map[string]protectedSnapshotEntry {
	snapshot := make(map[string]protectedSnapshotEntry)

	for _, rel := range r.protectedIntegrityPaths() {
		abs := filepath.Join(r.Root, rel)

		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot[rel] = protectedSnapshotEntry{missing: true}
				continue
			}
			// Unreadable entries (permissions, dangling symlink targets):
			// treat as missing so a later successful read/write attempt is
			// still compared conservatively.
			snapshot[rel] = protectedSnapshotEntry{missing: true}
			continue
		}

		sum := sha256.Sum256(data)
		snapshot[rel] = protectedSnapshotEntry{hash: hex.EncodeToString(sum[:])}
	}

	return snapshot
}

// verifyProtectedIntegrity compares the post-execution state against the
// snapshot and returns the first protected file that changed, appeared or
// disappeared. It performs no repair and deletes nothing — reporting only.
func (r *Router) verifyProtectedIntegrity(snapshot map[string]protectedSnapshotEntry) (string, bool) {
	for rel, before := range snapshot {
		abs := filepath.Join(r.Root, rel)

		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				if !before.missing {
					return rel, true // deleted
				}
				continue
			}
			// Became unreadable — count as tampering, conservatively.
			return rel, true
		}

		if before.missing {
			return rel, true // file was created where none existed
		}

		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != before.hash {
			return rel, true
		}
	}

	return "", false
}

// protectedIntegrityFailure converts a detected violation into the shared
// FAIL Result contract used by Exec/AssertOutput. The original command
// output is preserved in Output for diagnostics; Status/OK/Exit are forced
// to failure so the ledger can never record the command as successful.
func protectedIntegrityFailure(tool, command, modified, output string) Result {
	return Result{
		Tool:   tool,
		Output: output,
		Error: fmt.Sprintf(
			"protected file integrity violation: command %q modified protected file %s",
			command, modified,
		),
		Status: "FAIL",
		Exit:   1,
	}
}
