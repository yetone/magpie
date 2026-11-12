package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// TestClaudeBridgeTakesTheEffortOfEveryRoute: a remote client's level reaches
// the spawned Claude Code (--effort) on every route it can use: Messages
// (thinking, output_config), Chat Completions (reasoning_effort) and
// Responses (reasoning.effort) (amhcwushuang on X).
func TestClaudeBridgeTakesTheEffortOfEveryRoute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	testenv.SetHome(t, t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + dir + `/args
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	post := func(proto provider.Protocol, path, body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", path, strings.NewReader(body)), proto, p, "claude-opus-5-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		b, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	for i, c := range []struct {
		name  string
		proto provider.Protocol
		path  string
		body  string
		want  string
	}{
		{"messages budget", provider.Anthropic, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":9000,"thinking":{"type":"enabled","budget_tokens":20000},"messages":[{"role":"user","content":"a1"}]}`, "--effort high"},
		{"messages adaptive", provider.Anthropic, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":9000,"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"a2"}]}`, "--effort xhigh"},
		{"messages turn effort", provider.Anthropic, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":9000,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"a3"},{"role":"system","content":[],"output_config":{"effort":"max"}}]}`, "--effort max"},
		{"chat reasoning object", provider.Chat, "/v1/chat/completions", `{"model":"claude-opus-5-5","reasoning":{"effort":"high"},"messages":[{"role":"user","content":"b3"}]}`, "--effort high"},
		{"chat output_config", provider.Chat, "/v1/chat/completions", `{"model":"claude-opus-5-5","output_config":{"effort":"max"},"messages":[{"role":"user","content":"b4"}]}`, "--effort max"},
		{"chat thinking budget", provider.Chat, "/v1/chat/completions", `{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":8000},"messages":[{"role":"user","content":"b5"}]}`, "--effort medium"},
		{"chat", provider.Chat, "/v1/chat/completions", `{"model":"claude-opus-5-5","reasoning_effort":"high","messages":[{"role":"user","content":"b1"}]}`, "--effort high"},
		{"chat max", provider.Chat, "/v1/chat/completions", `{"model":"claude-opus-5-5","reasoning_effort":"max","messages":[{"role":"user","content":"b2"}]}`, "--effort max"},
		{"responses", provider.Responses, "/v1/responses", `{"model":"claude-opus-5-5","reasoning":{"effort":"xhigh"},"input":"c1"}`, "--effort xhigh"},
	} {
		_ = i
		if args := post(c.proto, c.path, c.body); !strings.Contains(args, c.want) {
			t.Errorf("%s: no %q in %q", c.name, c.want, args)
		}
	}
}
