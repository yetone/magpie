package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A Claude model of the 5 generation on that Claude Code doesn't know
// (Fable 5.2 on 2.1.316) is asked for by its "[1m]" name, which Claude
// Code runs to 1M: by its own it held the model to 200K and turned a
// longer conversation away before Anthropic was asked, so Codex, told the
// model's 1M window and compacting at 272K, never compacted (magicalaciw18
// on X). One it knows, and every 4 generation one, whose 1M a subscription
// without usage credits is refused a short turn on, keep their names.
func TestClaudeRunName(t *testing.T) {
	for model, want := range map[string]string{
		"claude-fable-5-2":     "claude-fable-5-2[1m]",
		"claude-opus-6":        "claude-opus-6[1m]",
		"claude-sonnet-5-7":    "claude-sonnet-5-7[1m]",
		"claude-fable-5-2[1m]": "claude-fable-5-2[1m]",
		"claude-fable-5-1":     "claude-fable-5-1",
		"claude-fable-5":       "claude-fable-5",
		"claude-opus-5-5":      "claude-opus-5-5",
		"claude-sonnet-5-5":    "claude-sonnet-5-5",
		"claude-haiku-5-5":     "claude-haiku-5-5",
		"claude-mythos-5-1":    "claude-mythos-5-1",
		"claude-sonnet-4-6":    "claude-sonnet-4-6",
		"claude-opus-4-8":      "claude-opus-4-8",
		"claude-haiku-4-5":     "claude-haiku-4-5",
		"claude-3-7-sonnet":    "claude-3-7-sonnet",
		"haiku":                "haiku",
		"m":                    "m",
	} {
		if got := claudeRunName(model); got != want {
			t.Errorf("claudeRunName(%q) = %q, want %q", model, got, want)
		}
	}
}

// The run of a Codex turn on claude/claude-fable-5-2 asks Claude Code for
// claude-fable-5-2[1m], and the reply names the model as asked.
func TestClaudeRunAsksUnknownModelLong(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" >> '` + argsFile + `'
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for model, want := range map[string]string{"claude-fable-5-2": "claude-fable-5-2[1m]", "claude-sonnet-4-6": "claude-sonnet-4-6"} {
		_ = os.Remove(argsFile)
		body := `{"model":"` + model + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, model, []byte(body), &u); code != 200 {
			t.Fatalf("%s: %d %s", model, code, msg)
		}
		b, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(string(b), "\n")
		got := ""
		for i, a := range args {
			if a == "--model" && i+1 < len(args) {
				got = args[i+1]
			}
		}
		if got != want {
			t.Errorf("%s: Claude Code was asked for %q, want %q", model, got, want)
		}
		var res struct{ Model string }
		json.Unmarshal(rec.Body.Bytes(), &res)
		if strings.Contains(res.Model, "[1m]") {
			t.Errorf("%s: the reply names %q", model, res.Model)
		}
	}
}
