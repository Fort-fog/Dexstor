package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredJSONAgentCycle(t *testing.T) {
	root := newFixture(t)
	server, requests := mockServer(t, func(prompt string) string {
		if strings.Contains(prompt, "reads=[math.go]") {
			b, _ := json.Marshal(map[string]string{"name": "write_file", "path": "math.go", "content": fixedMath})
			return string(b)
		}
		return `{"name":"read_file","path":"math.go","content":""}`
	})
	a := newAgent(t, root, server.URL, 8)
	a.AutoVerify = true
	if err := a.Run("fix math with structured tool output"); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("model calls=%d, want 2", *requests)
	}
	b, err := os.ReadFile(filepath.Join(root, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "return 10") {
		t.Fatalf("wrong result: %s", b)
	}
}
