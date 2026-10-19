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

// TestClaudeBridgePassesTheRequestsEffort: the effort a request carries is
// the level the spawned Claude Code thinks at (--effort), also when the
// request asks no thinking with it (#2899: output_config.effort low and max
// reached the subscription as the same turn, with no --effort on the spawned
// command line).
func TestClaudeBridgePassesTheRequestsEffort(t *testing.T) {
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
	post := func(body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-opus-5-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		b, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		// the fake writes one argument a line; the command line is them
		// joined, which is what the assertions below read
		return strings.Join(strings.Fields(string(b)), " ")
	}

	// the effort an output_config names, thinking asked or not, is the
	// spawned Claude Code's
	args := post(`{"model":"claude-opus-5-5","max_tokens":100,"output_config":{"effort":"max"},"messages":[{"role":"user","content":"How many positive integers below 100000 have digits that sum to 23?"}]}`)
	if !strings.Contains(args, "--effort max") {
		t.Fatalf("output_config.effort max: no --effort max in %q", args)
	}
	// with the thinking ask, as it always was
	args = post(`{"model":"claude-opus-5-5","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"and now?"}]}`)
	if !strings.Contains(args, "--effort high") {
		t.Fatalf("output_config.effort high with thinking: no --effort high in %q", args)
	}
	// Claude Code's auto mode classifier is asked at low whatever effort
	// it carries: its verdict is due within a minute (#250)
	args = post(`{"model":"claude-opus-5-5","max_tokens":100,"system":"Judge the action. Answer <block>yes</block> or <block>no</block>.","output_config":{"effort":"high"},"messages":[{"role":"user","content":"<transcript>rm -rf build</transcript>"}]}`)
	if !strings.Contains(args, "--effort low") {
		t.Fatalf("the classifier with output_config.effort high: no --effort low in %q", args)
	}
	// no effort asked, no flag: the CLI's own level stands
	args = post(`{"model":"claude-opus-5-5","max_tokens":100,"messages":[{"role":"user","content":"one more"}]}`)
	if strings.Contains(args, "--effort") {
		t.Fatalf("no effort asked: --effort in %q", args)
	}
}
