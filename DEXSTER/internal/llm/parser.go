package llm

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Call describes a single model-selected action. Tool execution is always
// validated separately by the Go controller.
type Call struct {
	Name string
	Args map[string]string
}

var toolStart = regexp.MustCompile(`(?m)^\s*TOOL_CALL\s*$`)
var toolEnd = regexp.MustCompile(`(?m)^\s*END_TOOL_CALL\s*$`)
var fieldLine = regexp.MustCompile(`^\s*(name|path|file|arg|source|command|expected|timeout|old|new|content)\s*:\s*(.*)$`)

// Parse accepts one complete textual TOOL_CALL or a single JSON tool object.
// Refuse ambiguous/multiple calls instead of silently applying one of them.
func Parse(raw string) (Call, bool) {
	text := strings.TrimSpace(raw)
	starts := toolStart.FindAllStringIndex(text, -1)
	if len(starts) == 0 {
		return parseJSONCall(text)
	}
	if len(starts) != 1 {
		return Call{}, false
	}
	rest := text[starts[0][1]:]
	ends := toolEnd.FindAllStringIndex(rest, -1)
	if len(ends) != 1 {
		return Call{}, false
	}
	block := strings.TrimPrefix(rest[:ends[0][0]], "\n")
	lines := strings.Split(block, "\n")
	call := Call{Args: map[string]string{}}
	for i := 0; i < len(lines); i++ {
		m := fieldLine.FindStringSubmatch(lines[i])
		if m == nil {
			if strings.TrimSpace(lines[i]) != "" {
				return Call{}, false
			}
			continue
		}
		key, value := m[1], strings.TrimSpace(m[2])
		if key == "content" {
			// The final newline before END_TOOL_CALL is the delimiter.
			content := strings.Join(append([]string{value}, lines[i+1:]...), "\n")
			if strings.HasSuffix(content, "\n") {
				content = strings.TrimSuffix(content, "\n")
			}
			if strings.HasPrefix(content, `"`) && strings.HasSuffix(content, `"`) {
				var decoded string
				if json.Unmarshal([]byte(content), &decoded) != nil {
					return Call{}, false
				}
				content = decoded
			}
			call.Args[key] = content
			break
		}
		if key == "name" {
			call.Name = value
		} else {
			call.Args[key] = value
		}
	}
	if call.Name == "" {
		return Call{}, false
	}
	return call, true
}

func parseJSONCall(text string) (Call, bool) {
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```"))
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &obj) != nil || len(obj) == 0 {
		return Call{}, false
	}
	var name string
	if json.Unmarshal(obj["name"], &name) != nil || name == "" {
		return Call{}, false
	}
	call := Call{Name: name, Args: map[string]string{}}
	for _, key := range []string{"path", "file", "arg", "source", "content", "old", "new", "command", "expected", "timeout"} {
		if raw, ok := obj[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return Call{}, false
			}
			call.Args[key] = s
		}
	}
	if raw, ok := obj["arguments"]; ok {
		var args map[string]string
		if json.Unmarshal(raw, &args) != nil {
			return Call{}, false
		}
		for k, v := range args {
			call.Args[k] = v
		}
	}
	return call, true
}

func (c Call) PathArg() string {
	for _, key := range []string{"path", "file", "arg"} {
		if s := c.Args[key]; s != "" {
			return s
		}
	}
	return ""
}
