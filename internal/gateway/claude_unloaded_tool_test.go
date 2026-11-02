package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// Claude Code with tool search on tells its model a deferred MCP tool by
// name only ("The following deferred tools are now available via
// ToolSearch… mcp__magpie__magpie-web-search__web_search") and leaves it out
// of tools until a ToolSearch loads it. The run's model calls it as told,
// under the full name, and the client is handed that name, which it runs:
// not the bare magpie-web-search__web_search it has no tool of, which it
// answered "Error: No such tool available: magpie-web-search__web_search"
// before the model retried (packinggoods, #feedback 11-02). A tool it
// declared still comes back under its declared name.
func TestClaudeBridgeHandsOverAnUnloadedMagpieTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
while read -r line; do
  case "$line" in
    *call-search*) name=mcp__magpie__magpie-web-search__web_search ;;
    *call-browser*) name=mcp__magpie__magpie-browser__browser_click ;;
    *) name=mcp__magpie__Read ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_'$$'","name":"'$name'"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"magpie\"}"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := New()
	t.Cleanup(s.subscription.abortAll)

	// the client's tools: ToolSearch, Read and the one MCP tool it loaded;
	// web_search is announced in the conversation only
	tools := `[{"name":"ToolSearch","input_schema":{"type":"object"}},{"name":"Read","input_schema":{"type":"object"}},{"name":"mcp__magpie__magpie-browser__browser_click","input_schema":{"type":"object"}}]`
	announced := "<system-reminder>\nThe following deferred tools are now available via ToolSearch. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use ToolSearch with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\nmcp__magpie__magpie-web-search__web_search\n</system-reminder>\n"
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	call := func(what string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":` + tools + `,"messages":[{"role":"user","content":` + strconv.Quote(announced+what) + `}]}`
		rec := httptest.NewRecorder()
		var u Usage
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%s: %d %s", what, code, msg)
		}
		var res struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		for _, c := range res.Content {
			if c.Type == "tool_use" {
				return c.Name
			}
		}
		t.Fatalf("%s: no tool call in %s", what, rec.Body)
		return ""
	}
	for what, want := range map[string]string{
		"call-search: search the web for magpie": "mcp__magpie__magpie-web-search__web_search",
		"call-browser: click the button":         "mcp__magpie__magpie-browser__browser_click",
		"read a file":                            "Read",
	} {
		if got := call(what); got != want {
			t.Errorf("%s: the client is handed a call of %q, want %q", what, got, want)
		}
	}
}
