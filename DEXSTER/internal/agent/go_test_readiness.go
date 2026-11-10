package agent

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Explicit Go-test creation requests need the test file before running
// verification on an unfinished multi-file scaffold. Do not infer this from
// the mere mention of tests: repair tasks can legitimately have no tests.
var goTestCreation = regexp.MustCompile(`(?i)(создай|создать|создайте|напиши|написать|добавь|добавить|покрой|create|write|add|implement)[^\n.!?]{0,90}(go[- ]?тест[а-я]*|go[- ]?tests?|тест[а-я]*[^\n.!?]{0,25}\bgo\b|unit[- ]?tests?|tests?|test cases)`)

func (a *Agent) waitForRequestedGoTests() bool {
	if a == nil || a.State == nil || a.Router == nil || !goTestCreation.MatchString(a.State.Task) {
		return false
	}
	files, err := a.Router.ListFiles()
	if err != nil {
		return false
	} // never bypass verification on IO errors
	for _, name := range files {
		if strings.HasSuffix(filepath.Base(name), "_test.go") {
			return false
		}
	}
	return true
}
