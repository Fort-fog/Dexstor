package memory

import "strings"

type ProjectRollbackMode string

const (
	RollbackPrevious ProjectRollbackMode = "previous"
	RollbackFirst    ProjectRollbackMode = "first"
)

type RollbackRequest struct {
	Mode ProjectRollbackMode
}

// ParseRollbackIntent handles explicit project rollback requests, not ordinary
// requests to restore source files or continue work from persisted agent state.
// File repairs must reach the DEXSTER agent, never the KRAKEN rollback path.
func ParseRollbackIntent(task string) (RollbackRequest, bool) {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(task), "ё", "е"))
	if t == "" {
		return RollbackRequest{}, false
	}
	for _, neg := range []string{
		"не откатывай", "не откат", "без отката", "не восстанавливай",
		"do not rollback", "don't rollback", "no rollback",
	} {
		if strings.Contains(t, neg) {
			return RollbackRequest{}, false
		}
	}

	// Explicit rollback commands retain the existing user-facing behavior.
	rollbackWord := strings.Contains(t, "откат") || strings.Contains(t, "rollback")

	// A restore/return verb alone isn't a project rollback. In particular,
	// "восстанови task.go; используй сохраненное состояние" is normal repair.
	// Require a clear version or checkpoint target, not just "состояние".
	versionTarget := false
	for _, target := range []string{
		"рабочую верси", "рабочей верси", "рабочую точк", "рабочей точк",
		"предыдущую верси", "предыдущей верси", "первую верси",
		"первой верси", "первую рабоч", "первой рабоч",
		"исходную верси", "исходной верси", "последний checkpoint",
		"предыдущий checkpoint", "verified snapshot", "working version",
		"first version", "first verified", "stable version",
	} {
		if strings.Contains(t, target) {
			versionTarget = true
			break
		}
	}
	restoreWord := strings.Contains(t, "восстанов") || strings.Contains(t, "restore")
	returnWord := strings.Contains(t, "верни") || strings.Contains(t, "вернуть") || strings.Contains(t, "return")
	if !rollbackWord && !(versionTarget && (restoreWord || returnWord)) {
		return RollbackRequest{}, false
	}

	mode := RollbackPrevious
	for _, w := range []string{"первую рабоч", "первой рабоч", "самую первую", "первонач", "first working", "first verified", "first version"} {
		if strings.Contains(t, w) {
			mode = RollbackFirst
			break
		}
	}
	return RollbackRequest{Mode: mode}, true
}
