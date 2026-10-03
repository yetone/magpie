package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Every run works in the same folder: Claude Code puts its working
// directory in the system prompt, ahead of the conversation, so a folder of
// each run's own left nothing past Claude Code's own part of the prompt to
// be read from the cache.
func TestClaudeRunsShareAWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
pwd -P >> ` + dir + `/dirs
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, said := range []string{"one", "two"} {
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","messages":[{"role":"user","content":"` + said + `"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "dirs"))
	dirs := strings.Fields(string(b))
	if len(dirs) != 2 || dirs[0] != dirs[1] {
		t.Fatalf("working directories: %q", dirs)
	}
}
