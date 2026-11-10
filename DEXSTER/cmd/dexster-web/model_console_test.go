package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSeparatesRealModelCodeFromControllerLog(t *testing.T) {
	c := newController("/bin/true")
	raw := `{"name":"write_file","path":"main.go","content":"package main\nfunc main() { println(\"ok\") }","source":""}`
	send := func(event, text string) {
		b, err := json.Marshal(modelTraceEvent{Event: event, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		c.addModelEvent(modelTracePrefix + string(b) + "\n")
	}
	send("start", "")
	send("chunk", raw[:55])
	snap := c.snapshot()
	if !strings.Contains(snap.ModelLog, raw[:55]) || snap.Log != "" {
		t.Fatalf("partial console should not be in controller log: %#v", snap)
	}
	send("chunk", raw[55:])
	send("done", "")
	snap = c.snapshot()
	if !strings.Contains(snap.ModelLog, "ФАЙЛ: main.go\npackage main\nfunc main()") || strings.Contains(snap.ModelLog, `\\nfunc`) {
		t.Fatalf("model code not unescaped: %q", snap.ModelLog)
	}
	if snap.Log != "" {
		t.Fatalf("controller log polluted: %q", snap.Log)
	}
	c.addLog("[DEXSTER] #1 write_file main.go ok=true\n")
	w := httptest.NewRecorder()
	c.routes().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/api/state", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"model_log"`) || !strings.Contains(w.Body.String(), `"log"`) {
		t.Fatalf("API state missing fields: %s", w.Body.String())
	}
	if !strings.Contains(page, `id="modelLog"`) || !strings.Contains(page, `terminal-panes`) {
		t.Fatal("UI missing split panes")
	}
}

func TestWebStreamingRetryReplacesPreviousPartial(t *testing.T) {
	c := newController("/bin/true")
	send := func(event, text string) {
		b, _ := json.Marshal(modelTraceEvent{Event: event, Text: text})
		c.addModelEvent(modelTracePrefix + string(b) + "\n")
	}
	send("start", "")
	send("chunk", "broken partial")
	send("retry", "network")
	send("start", "")
	send("chunk", `{"name":"list_files","path":".","content":"","source":""}`)
	send("done", "")
	s := c.snapshot()
	if !strings.Contains(s.ModelLog, "ДЕЙСТВИЕ: list_files .") || !strings.Contains(s.ModelLog, "retry") && !strings.Contains(s.ModelLog, "Временный сбой") {
		t.Fatalf("retry state: %q", s.ModelLog)
	}
}
