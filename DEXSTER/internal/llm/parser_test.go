package llm

import (
	"testing"
)

func TestParseSimpleCall(t *testing.T) {
	call, ok := Parse("TOOL_CALL\nname: read_file\npath: math.go\nEND_TOOL_CALL")
	if !ok || call.Name != "read_file" || call.PathArg() != "math.go" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseMultilineContent(t *testing.T) {
	raw := "TOOL_CALL\nname: write_file\npath: math.go\ncontent: package main\nfunc Add(a, b int) int {\n\treturn a + b\n}\nEND_TOOL_CALL"
	call, ok := Parse(raw)
	if !ok || call.Name != "write_file" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
	want := "package main\nfunc Add(a, b int) int {\n\treturn a + b\n}"
	if call.Args["content"] != want {
		t.Fatalf("content mismatch:\ngot  %q\nwant %q", call.Args["content"], want)
	}
}

func TestParseJSONQuotedContent(t *testing.T) {
	raw := `TOOL_CALL
name: write_file
path: x.txt
content: "line1\nline2"
END_TOOL_CALL`
	call, ok := Parse(raw)
	if !ok || call.Args["content"] != "line1\nline2" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseRejectsMultipleCalls(t *testing.T) {
	raw := "TOOL_CALL\nname: read_file\npath: a.go\nEND_TOOL_CALL\nTOOL_CALL\nname: write_file\npath: b.go\ncontent: x\nEND_TOOL_CALL"
	if _, ok := Parse(raw); ok {
		t.Fatal("multiple TOOL_CALL blocks must be rejected")
	}
}

func TestParseRejectsMissingName(t *testing.T) {
	if _, ok := Parse("TOOL_CALL\npath: a.go\nEND_TOOL_CALL"); ok {
		t.Fatal("call without name must be rejected")
	}
}

func TestParseNoCall(t *testing.T) {
	if _, ok := Parse("I think we should look at the files first."); ok {
		t.Fatal("free text without TOOL_CALL must not parse")
	}
}

func TestParseIgnoresSurroundingText(t *testing.T) {
	raw := "Sure, here is my plan.\nTOOL_CALL\nname: list_files\nEND_TOOL_CALL\nDone."
	call, ok := Parse(raw)
	if !ok || call.Name != "list_files" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseContentWithColons(t *testing.T) {
	raw := "TOOL_CALL\nname: write_file\npath: cfg.txt\ncontent: url: http://x/y?z=1\nEND_TOOL_CALL"
	call, ok := Parse(raw)
	if !ok || call.Args["content"] != "url: http://x/y?z=1" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseArgAlias(t *testing.T) {
	call, ok := Parse("TOOL_CALL\nname: read_file\narg: pkg/math.go\nEND_TOOL_CALL")
	if !ok || call.PathArg() != "pkg/math.go" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseEmptyContent(t *testing.T) {
	call, ok := Parse("TOOL_CALL\nname: write_file\npath: empty.txt\ncontent:\nEND_TOOL_CALL")
	if !ok || call.Args["content"] != "" {
		t.Fatalf("got %+v ok=%v", call, ok)
	}
}

func TestParseJSONToolObject(t *testing.T) {
	for _, raw := range []string{
		`{"name":"read_file","arguments":{"path":"main.go"}}`,
		"```json\n{\"name\":\"read_file\",\"path\":\"main.go\"}\n```",
	} {
		c, ok := Parse(raw)
		if !ok || c.Name != "read_file" || c.PathArg() != "main.go" {
			t.Fatalf("json tool call rejected: %+v ok=%t input=%s", c, ok, raw)
		}
	}
}

func TestParseRejectsMissingTerminator(t *testing.T) {
	if _, ok := Parse("TOOL_CALL\nname: read_file\npath: x.go"); ok {
		t.Fatal("unterminated tool call accepted")
	}
}

func TestParseRejectsAmbiguousTerminatorInContent(t *testing.T) {
	raw := "TOOL_CALL\nname: write_file\npath: x.txt\ncontent: first\nEND_TOOL_CALL\nlast\nEND_TOOL_CALL"
	if _, ok := Parse(raw); ok {
		t.Fatal("ambiguous terminator accepted")
	}
}

func TestParseRejectsMultipleJSONActions(t *testing.T) {
	if _, ok := Parse(`[{"name":"read_file","path":"x"},{"name":"write_file","path":"y"}]`); ok {
		t.Fatal("array of actions accepted")
	}
}
