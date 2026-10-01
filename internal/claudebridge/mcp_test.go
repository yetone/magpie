package claudebridge

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A call's answer that brings new tools (the client loaded one with Claude
// Code's ToolSearch) has the helper tell Claude Code its tools changed, and
// is given only once Claude Code listed them again: its next request then
// offers the tool, as Claude Code 2.1 does with a list_changed mid-turn.
func TestNewToolsListedBeforeTheAnswer(t *testing.T) {
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "Tool WebFetch is loaded and can be called now."}},
			"tools": []map[string]any{
				{"name": "ToolSearch", "inputSchema": map[string]any{"type": "object"}},
				{"name": "WebFetch", "inputSchema": map[string]any{"type": "object"}},
			},
		})
	}))
	defer cb.Close()
	dir := t.TempDir()
	toolsPath := filepath.Join(dir, "tools.json")
	os.WriteFile(toolsPath, []byte(`[{"name":"ToolSearch","inputSchema":{"type":"object"}}]`), 0o600)

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	go func() { _ = RunMCP([]string{cb.URL, toolsPath}); outW.Close() }()
	lines := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var m map[string]any
			_ = json.Unmarshal(sc.Bytes(), &m)
			lines <- m
		}
		close(lines)
	}()
	send := func(s string) { inW.Write([]byte(s + "\n")) }
	next := func() map[string]any {
		t.Helper()
		select {
		case m := <-lines:
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no message from the helper")
			return nil
		}
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	caps, _ := next()["result"].(map[string]any)["capabilities"].(map[string]any)["tools"].(map[string]any)
	if caps["listChanged"] != true {
		t.Errorf("tools capability = %v, no listChanged", caps)
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ToolSearch","arguments":{"query":"select:WebFetch"},"_meta":{"claudecode/toolUseId":"toolu_1"}}}`)
	if m := next(); m["method"] != "notifications/tools/list_changed" {
		t.Fatalf("first after the call = %v, not list_changed", m)
	}
	select {
	case m := <-lines:
		t.Fatalf("answered before the tools were listed again: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	list := next()
	if list["id"] != float64(3) {
		t.Fatalf("expected the tools/list answer first, got %v", list)
	}
	if tools := list["result"].(map[string]any)["tools"].([]any); len(tools) != 2 {
		t.Fatalf("tools listed = %v", tools)
	}
	if m := next(); m["id"] != float64(2) || m["result"] == nil {
		t.Fatalf("call answer = %v", m)
	}
	inW.Close()
}
