package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// takebackClaude stands in for Claude Code as rewindClaude does, and calls
// the client's read tool through its MCP server for a turn that says CALL:
// it says the call, makes it, logs what it got back, and says that in the
// reply after, which it leaves unfinished when the result says SLOW, for
// the client to give up on. A turn told with a tool result in it in words
// (a run told the turn again) is answered with its pid and turn only.
func takebackClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("the script makes its MCP calls with curl")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
echo "args $*" >> ` + log + `
cb=$(printf '%s\n' "$@" | grep -o 'http://[^"]*/_magpie/claude-mcp/[A-Za-z0-9_-]*' | head -1)
n=0
me=$$
start() { echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$n'","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'; }
text() { echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'"$1"'"}}}'; }
finish() {
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
}
while read -r line; do
  printf '%s\n' "$line" >> ` + log + `
  case "$line" in *rewind_conversation*)
    id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
    echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}'
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'","response":{"rewound":true}}}'
    continue;;
  esac
  case "$line" in *control_request*) continue;; esac
  n=$((n+1))
  case "$line" in
    *"tool result"*) start; text "pid $$ turn $n"; finish; continue;;
    *CALL*) ;;
    *) start; text "pid $$ turn $n"; finish; continue;;
  esac
  start
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_T'$$'_'$n'","name":"mcp__magpie__read"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_stop","index":0}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  got=$(curl -sS -X POST -H 'Content-Type: application/json' --data '{"tool_call_id":"toolu_T'$me'_'$n'","name":"read","arguments":{}}' "$cb" 2>> ` + log + `)
  echo "got $got" >> ` + log + `
  start
  text "pid $$ turn $n read"
  case "$got" in *SLOW*) continue;; esac
  finish
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// #1365: a client that goes away while the run answers a turn's tool
// results — a cut connection, or the user interrupting to send a message
// they queued — sends the turn again, its results with it. The run had
// ended its turn's rewind with the reply that called the tool, so it was
// let go, the results found "no run waiting", and a new Claude Code was
// told the whole conversation in one message, every image in it with it,
// which the model took for new. The turn is now taken back whole, to its
// user message, and the resend goes on in the same Claude Code, told only
// the turn since that message.
func TestClaudeToolResultsGivenUpOnAreTakenBack(t *testing.T) {
	s := New()
	h := &rewindHarness{t: t, s: s, log: takebackClaude(t),
		p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(s.subscription.abortAll) // before srv.Close: a run's MCP call holds a request open
	t.Setenv("MAGPIE_ADDR", srv.Listener.Addr().String())

	first := h.ask(`[` + rmsg("user", "first-words") + `]`)
	pid := pidOf(t, first)
	conv := rmsg("user", "first-words") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "CALL the read tool")

	// the turn's reply calls read
	body := h.body(`[` + conv + `]`)
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Type, ID string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 || res.Content[len(res.Content)-1].Type != "tool_use" {
		t.Fatalf("no call: %s\n%s", rec.Body, h.read())
	}
	id := res.Content[len(res.Content)-1].ID
	for deadline := time.Now().Add(5 * time.Second); ; {
		h.s.subscription.mu.Lock()
		waits := h.s.subscription.calls[id] != nil
		h.s.subscription.mu.Unlock()
		if waits {
			break
		}
		if time.Now().After(deadline) {
			h.s.subscription.mu.Lock()
			var keys []string
			for k := range h.s.subscription.calls {
				keys = append(keys, k)
			}
			h.s.subscription.mu.Unlock()
			t.Fatalf("the run never made its call %s (%v):\n%s", id, keys, h.read())
		}
		time.Sleep(5 * time.Millisecond)
	}
	conv += `,{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`
	results := func(said string) string {
		return `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + said + `"}]}`
	}

	// the client goes away while the run answers the results
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		body := h.body(`[` + conv + results("SLOW file body") + `]`)
		var u Usage
		h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	}()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(h.read(), "got ") || !strings.Contains(h.read(), "SLOW"); {
		if time.Now().After(deadline) {
			t.Fatalf("the run never got the results:\n%s", h.read())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // its reply under way
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the request given up on never returned")
	}
	h.rewindAsked()

	// the client sends the turn again, its results with it
	again := h.ask(`[` + conv + results("file body") + `]`)
	if again != "pid "+pid+" turn 3" {
		t.Fatalf("the turn sent again went to another Claude Code: %q (first %q)\n%s", again, first, h.read())
	}
	var turn, rewind, told map[string]any
	for _, m := range h.lines() {
		b, _ := json.Marshal(m)
		switch s := string(b); {
		case m["type"] == "user" && strings.Contains(s, "CALL") && !strings.Contains(s, "tool result"):
			turn = m
		case m["type"] == "control_request" && strings.Contains(s, "rewind_conversation"):
			rewind = m
		case m["type"] == "user" && strings.Contains(s, "tool result"):
			told = m
		}
	}
	if turn == nil || rewind == nil || told == nil {
		t.Fatalf("Claude Code was told:\n%s", h.read())
	}
	req, _ := rewind["request"].(map[string]any)
	if turn["uuid"] == nil || req["target_message_uuid"] != turn["uuid"] {
		t.Fatalf("rewound to %v, the turn's message was %v", req, turn["uuid"])
	}
	b, _ := json.Marshal(told)
	if strings.Contains(string(b), "first-words") || !strings.Contains(string(b), "CALL the read tool") || !strings.Contains(string(b), "file body") {
		t.Fatalf("the turn sent again was told other than the turn since its message: %s", b)
	}
}
