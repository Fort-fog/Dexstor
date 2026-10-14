package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// must be verified before and after shell execution. It always includes the
// full static ProtectedPath registry (even when the file does not exist yet:
// creation at such a path is a violation) plus EVERY existing *_test.go and
// test.pl found anywhere under the project root.
//
// There is deliberately no depth limit: a *_test.go hidden in deeply nested
// packages must be protected exactly like one at the root. Any WalkDir or
// filepath.Rel failure is returned as an error — callers must treat it as
// fail-closed and must NOT proceed as if the tree were clean.
//
// Existing files discovered by the walk join the snapshot lazily, so a
// brand-new *_test.go created during a command is NOT retroactively treated
// as a violation of THAT command (the agent's normal first-test-file write
// keeps working). From the next command onwards the file is fully protected.
func (r *Router) protectedIntegrityPaths() ([]string, error) {
	set := make(map[string]bool)

	for protected := range r.ProtectedPath {
		clean := filepath.Clean(protected)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			// A registry entry that points outside the project root cannot be
			// guarded meaningfully; skip it but keep the rest of the scan strict.
			continue
		}
		set[clean] = true
	}

	walkErr := filepath.WalkDir(r.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Fail-closed: an unreadable subtree could hide protected files;
			// we cannot certify integrity without seeing it.
			return fmt.Errorf("protected scan failed at %s: %w", path, err)
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if !strings.HasSuffix(name, "_test.go") && name != "test.pl" {
			return nil
		}

		rel, relErr := filepath.Rel(r.Root, path)
		if relErr != nil {
			return fmt.Errorf("cannot resolve relative path for %s: %w", path, relErr)
		}
		rel = filepath.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "..") {
			// Path escaped the root during the walk — refuse rather than
			// silently ignoring it.
			return fmt.Errorf("protected path escapes project root: %s", path)
		}
		set[rel] = true
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// snapshotProtectedFiles captures content hashes of all currently protected
// files before a shell command runs. Missing files are recorded as
// missing-but-tracked so creation attempts are also detected. Only a
// confirmed os.IsNotExist is treated as "missing": permission errors, I/O
// failures and dangling symlink targets are returned as errors so callers
// fail closed instead of silently accepting an unverifiable state.
func (r *Router) snapshotProtectedFiles() (map[string]protectedSnapshotEntry, error) {
	snapshot := make(map[string]protectedSnapshotEntry)

	paths, err := r.protectedIntegrityPaths()
	if err != nil {
		return nil, err
	}

	for _, rel := range paths {
		abs := filepath.Join(r.Root, rel)

		// Lstat first so a symlink can be classified explicitly: a dangling
		// symlink or a link pointing outside the root cannot be hashed safely
		// and must not be disguised as "missing".
		if lfi, lerr := os.Lstat(abs); lerr == nil && lfi.Mode()&os.ModeSymlink != 0 {
			target, rerr := filepath.EvalSymlinks(abs)
			if rerr != nil {
				return nil, fmt.Errorf("cannot resolve protected symlink %s: %w", rel, rerr)
			}
			if !withinRoot(r.Root, target) {
				return nil, fmt.Errorf("protected symlink %s resolves outside project root: %s", rel, target)
			}
		}

		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot[rel] = protectedSnapshotEntry{missing: true}
				continue
			}
			return nil, fmt.Errorf("cannot read protected file %s: %w", rel, err)
		}

		sum := sha256.Sum256(data)
		snapshot[rel] = protectedSnapshotEntry{hash: hex.EncodeToString(sum[:])}
	}

	return snapshot, nil
}

// withinRoot reports whether absPath is inside root after cleaning.
func withinRoot(root, absPath string) bool {
	root = filepath.Clean(root)
	p := filepath.Clean(absPath)
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// verifyProtectedIntegrity re-scans the protected set AFTER the command
// finished and compares both the path SET and the contents against the
// pre-execution snapshot. It detects:
//   - modification of an existing protected file;
//   - deletion of an existing protected file;
//   - creation at a previously-missing statically registered ProtectedPath
//     (test.pl is always registered, so `echo x > test.pl` is caught);
//   - modification/deletion of any *_test.go that existed before the command,
//     regardless of directory depth (the fresh scan has no depth limit).
//
// A brand-new *_test.go discovered only in the post-scan is adopted lazily:
// it was not tracked by the pre-scan, so its initial creation by the agent's
// own write stays allowed (documented contract with
// TestProtectedIntegrityNewTestFileDoesNotBreakCurrentLoop), but ANY later
// tampering with it is detected because from the next command onwards it is
// part of the tracked snapshot at every depth.
//
// Any scan/read/resolve failure is returned as err — callers MUST NOT report
// success when err != nil, even if the command exited 0 and printed PASS.
// The function performs no repair and deletes nothing — reporting only.
func (r *Router) verifyProtectedIntegrity(snapshot map[string]protectedSnapshotEntry) (string, bool, error) {
	post, err := r.snapshotProtectedFiles()
	if err != nil {
		return "", false, fmt.Errorf("post-execution protected scan failed: %w", err)
	}

	// Compare every path tracked BEFORE the command.
	for rel, before := range snapshot {
		after, exists := post[rel]
		if !exists || after.missing {
			if !before.missing {
				return rel, true, nil // deleted
			}
			continue // still missing: fine
		}
		if before.missing {
			return rel, true, nil // created at a previously-absent registered path
		}
		if before.hash != after.hash {
			return rel, true, nil // modified
		}
	}

	return "", false, nil
}

// protectedScanFailure converts an integrity CHECK failure (scan/read error)
// into the same unambiguous FAIL contract as a detected violation: Exit=-1,
// Status=FAIL, and the raw command output (which may contain success markers
// like "PASS") is moved into Error so no downstream consumer can read the
// outcome as success. The check being unavailable is never a pass.
func protectedScanFailure(tool, command string, scanErr error, output string) Result {
	return Result{
		Tool: tool,
		Output: fmt.Sprintf(
			"SEC-2 INTEGRITY CHECK UNAVAILABLE: %v; command result cannot be trusted, raw command output moved to Error field",
			scanErr,
		),
		Error: fmt.Sprintf(
			"protected file integrity check failed for command %q: %v. original output follows:\n%s",
			command, scanErr, output,
		),
		Status: "FAIL",
		Exit:   -1,
	}
}

// protectedIntegrityFailure converts a detected violation into the shared
// FAIL Result contract used by Exec/AssertOutput. The result is
// unambiguous: Exit is forced to -1 (never 0), and the raw command output
// (which may contain success markers like "PASS") is moved into the Error field so
// downstream code reading Output/Error cannot mistake the outcome for
// success. Status/OK are forced to failure for ledger compatibility.
func protectedIntegrityFailure(tool, command, modified, output string) Result {
	return Result{
		Tool: tool,
		Output: fmt.Sprintf(
			"SEC-2 INTEGRITY VIOLATION: protected file %s was modified/deleted/created by command; raw command output moved to Error field to avoid ambiguity",
			modified,
		),
		Error: fmt.Sprintf(
			"protected file integrity violation: command %q modified protected file %s. original output follows:\n%s",
			command, modified, output,
		),
		Status: "FAIL",
		Exit:   -1,
	}
}
