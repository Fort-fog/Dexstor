package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"dexster/internal/llm"
)

// prepareExactReplacement converts a bounded edit into an ordinary write_file.
// The existing controller remains the sole authority for CAS, constraints,
// checkpoint creation, journal reconciliation, and the evidence chain.
// It never reads untrusted new bytes to build an edit: the entire replacement
// is calculated from the last previously acknowledged read snapshot.
func (a *Agent) prepareExactReplacement(call llm.Call) (llm.Call, error) {
	p := call.PathArg()
	if p == "" || hiddenWorkerMetadata(p) {
		return llm.Call{}, fmt.Errorf("EXACT EDIT: a valid project source path is required")
	}
	if a.State == nil || a.State.Evidence.ReadFiles == nil {
		return llm.Call{}, fmt.Errorf("EXACT EDIT: trusted read snapshot required; call read_file first")
	}
	// Keep aliases normalized to the same path to prevent bypassing CAS.
	p = filepath.Clean(p)
	before, ok := a.State.Evidence.ReadFiles[p]
	if !ok {
		return llm.Call{}, fmt.Errorf("EXACT EDIT: trusted read snapshot for %s required; call read_file first", p)
	}
	old, hasOld := call.Args["old"]
	newer, hasNew := call.Args["new"]
	if !hasOld || !hasNew || old == "" || old == newer {
		return llm.Call{}, fmt.Errorf("EXACT EDIT: nonempty distinct old and new strings are required")
	}
	// No fuzzy edits: they can silently patch an unrelated function, class
	// or image card. One exact occurrence is the only acceptable target.
	count := strings.Count(before, old)
	if count != 1 {
		return llm.Call{}, fmt.Errorf("EXACT EDIT: old snippet appears %d times in trusted snapshot of %s; choose a UNIQUE exact excerpt or read the file again", count, p)
	}
	content := strings.Replace(before, old, newer, 1)
	return llm.Call{Name: "write_file", Args: map[string]string{"path": p, "content": content}}, nil
}
