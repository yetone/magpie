package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// rewindClaude stands in for Claude Code: it logs its arguments and each
// line it is told, answers each user message with its pid and turn, and
// leaves a message saying SLOW mid-reply, for the client to give up on. A
// rewind_conversation request is answered as mode says: "ok" rewinds,
// "refuse" fails, "silent" says nothing. It gives the log's path.
func rewindClaude(t *testing.T, mode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
echo "args $*" >> ` + log + `
n=0
while read -r line; do
  printf '%s\n' "$line" >> ` + log + `
  case "$line" in *rewind_conversation*)
    id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
    echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}'
    case ` + mode + ` in
      ok) echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'","response":{"rewound":true}}}';;
      refuse) echo '{"type":"control_response","response":{"subtype":"error","request_id":"'$id'","error":"no such message"}}';;
    esac
    continue;;
  esac
  case "$line" in *control_request*) continue;; esac
  n=$((n+1))
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$n'","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}}'
  case "$line" in *SLOW*) continue;; esac
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

type rewindHarness struct {
	t   *testing.T
	s   *Server
	p   provider.Provider
	log string
}

func newRewindHarness(t *testing.T, mode string) *rewindHarness {
	log := rewindClaude(t, mode)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	return &rewindHarness{t: t, s: s, log: log,
		p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}}
}

func (h *rewindHarness) body(msgs string) string {
	return `{"model":"claude-sonnet-5","max_tokens":100,"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
}

func (h *rewindHarness) ask(msgs string) string {
	h.t.Helper()
	body := h.body(msgs)
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 {
		h.t.Fatalf("no answer: %s", rec.Body)
	}
	return res.Content[0].Text
}

// giveUp asks msgs, whose last message says SLOW, and goes away once
// Claude Code is mid-reply, as a user stopping it does (a 499).
func (h *rewindHarness) giveUp(msgs string) {
	h.t.Helper()
	body := h.body(msgs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var u Usage
		h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.read(), "SLOW") {
		if time.Now().After(deadline) {
			h.t.Fatalf("Claude Code was never told the turn:\n%s", h.read())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // its reply under way
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		h.t.Fatal("the request given up on never returned")
	}
}

func (h *rewindHarness) read() string {
	b, _ := os.ReadFile(h.log)
	return string(b)
}

// lines are what Claude Code was told, its arguments left out.
func (h *rewindHarness) lines() []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(h.read()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func rmsg(role, text string) string {
	return `{"role":"` + role + `","content":` + strconv.Quote(text) + `}`
}

func pidOf(t *testing.T, answer string) string {
	t.Helper()
	pid, _, ok := strings.Cut(strings.TrimPrefix(answer, "pid "), " ")
	if !ok {
		t.Fatalf("answer %q", answer)
	}
	return pid
}

// A turn the client gave up on mid-reply is taken back in the same Claude
// Code (#780): it is told to rewind to that turn's message, and the
// client's resend — the same conversation, its last message reworded — goes
// on in it, told only the new message, so the prefix Claude Code sends
// Anthropic is the one it cached. Ended, a new Claude Code was told the
// whole conversation in one message, written to the cache again.
func TestClaudeTurnGivenUpOnIsRewound(t *testing.T) {
	h := newRewindHarness(t, "ok")
	first := h.ask(`[` + rmsg("user", "hi") + `]`)
	pid := pidOf(t, first)
	conv := rmsg("user", "hi") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "and?")
	second := h.ask(`[` + conv + `]`)
	if second != "pid "+pid+" turn 2" {
		t.Fatalf("second: %q", second)
	}
	conv += `,` + rmsg("assistant", second)
	h.giveUp(`[` + conv + `,` + rmsg("user", "SLOW, go on") + `]`)
	again := h.ask(`[` + conv + `,` + rmsg("user", "go on instead") + `]`)
	if again != "pid "+pid+" turn 4" {
		t.Fatalf("the resend went to another Claude Code: %q (first %q)\n%s", again, first, h.read())
	}

	var slow, rewind, resend map[string]any
	for _, m := range h.lines() {
		b, _ := json.Marshal(m)
		switch s := string(b); {
		case m["type"] == "user" && strings.Contains(s, "SLOW"):
			slow = m
		case m["type"] == "control_request" && strings.Contains(s, "rewind_conversation"):
			rewind = m
		case m["type"] == "user" && strings.Contains(s, "go on instead"):
			resend = m
		}
	}
	if slow == nil || rewind == nil || resend == nil {
		t.Fatalf("Claude Code was told:\n%s", h.read())
	}
	req, _ := rewind["request"].(map[string]any)
	if slow["uuid"] == nil || req["target_message_uuid"] != slow["uuid"] || req["interrupt_if_running"] != true {
		t.Fatalf("rewound to %v, the turn given up on was %v", req, slow["uuid"])
	}
	if b, _ := json.Marshal(resend); strings.Contains(string(b), "and?") || strings.Contains(string(b), "SLOW") {
		t.Fatalf("the resend was told more than its new message: %s", b)
	}
	// and it goes on as any kept run does
	conv += `,` + rmsg("user", "go on instead") + `,` + rmsg("assistant", again)
	if next := h.ask(`[` + conv + `,` + rmsg("user", "thanks") + `]`); next != "pid "+pid+" turn 5" {
		t.Fatalf("next: %q", next)
	}
}

// A run whose Claude Code refuses the rewind, or doesn't answer it, is let
// go, as every run given up on was: the resend is told the conversation in
// a new one.
func TestClaudeTurnGivenUpOnNotRewound(t *testing.T) {
	for _, mode := range []string{"refuse", "silent"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "silent" {
				old := rewindLongest
				rewindLongest = 300 * time.Millisecond
				t.Cleanup(func() { rewindLongest = old })
			}
			h := newRewindHarness(t, mode)
			first := h.ask(`[` + rmsg("user", "hi") + `]`)
			pid := pidOf(t, first)
			conv := rmsg("user", "hi") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "and?")
			second := h.ask(`[` + conv + `]`)
			conv += `,` + rmsg("assistant", second)
			h.giveUp(`[` + conv + `,` + rmsg("user", "SLOW, go on") + `]`)
			again := h.ask(`[` + conv + `,` + rmsg("user", "go on instead") + `]`)
			if pidOf(t, again) == pid || !strings.HasSuffix(again, " turn 1") {
				t.Fatalf("the resend: %q, first %q", again, first)
			}
			if !strings.Contains(h.read(), "rewind_conversation") {
				t.Fatalf("no rewind asked:\n%s", h.read())
			}
			h.s.subscription.mu.Lock()
			n := len(h.s.subscription.runs)
			h.s.subscription.mu.Unlock()
			if n != 1 {
				t.Fatalf("%d runs, the one given up on kept", n)
			}
		})
	}
}

// A run started for the turn given up on has no conversation before it to
// go back to: it is ended, as before, and nothing is rewound.
func TestClaudeFirstTurnGivenUpOnEnds(t *testing.T) {
	h := newRewindHarness(t, "ok")
	h.giveUp(`[` + rmsg("user", "SLOW hi") + `]`)
	if strings.Contains(h.read(), "rewind_conversation") {
		t.Fatalf("rewound a run started for the turn:\n%s", h.read())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.s.subscription.mu.Lock()
		n := len(h.s.subscription.runs)
		h.s.subscription.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d runs left", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
