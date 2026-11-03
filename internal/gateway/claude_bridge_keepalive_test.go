package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// #5043: a Claude Code run that says nothing for minutes before its
// reply (a long conversation replayed whole, prefilled uncached on a
// high effort) left a streaming client with the held stream's comments
// only, and those stop at keepaliveLongest: Claude Code 2.1.316's byte
// watchdog then hung up 300s later, at 599s, and its event watchdog
// counts no comment at all. Past bridgeHeardAfter the reply begins
// (message_start) and the client is sent its protocol's pings while the
// run is quiet, and the run's own start, when it comes, adds its usage.
func TestClaudeBridgeQuietRunKeepsClientAlive(t *testing.T) {
	claudeMadeFirst(t, false)
	keepFast(t, 10*time.Millisecond, 50*time.Millisecond, keepaliveLongest)
	heard, ping := bridgeHeardAfter, pingEvery
	bridgeHeardAfter, pingEvery = 200*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { bridgeHeardAfter, pingEvery = heard, ping })

	// a Claude Code quiet for 1.5s before its reply: ~13 pings' room at
	// 100ms from 200ms, of which 3 are asked for
	bin := t.TempDir()
	testenv.Program(t, filepath.Join(bin, "claude"), `#!/bin/sh
case "$1" in auth) exit 1;; esac
if [ "$2" = "/usage" ]; then
  echo '{"type":"result","is_error":false,"result":"Current session: 10% used"}'
  exit 0
fi
while read -r line; do
  case $line in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  sleep 1.5
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":4242}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"after the quiet"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	t.Cleanup(s.subscription.abortAll)
	body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	var u Usage
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}
	if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	out := rec.Body.String()
	said := strings.Index(out, "after the quiet")
	if said < 0 {
		t.Fatalf("no answer:\n%s", out)
	}
	before := out[:said]
	if n := strings.Count(before, "event: message_start"); n != 1 {
		t.Fatalf("%d message_start before the reply, want 1 (the client heard nothing it counts while the run was quiet):\n%s", n, out)
	}
	if n := strings.Count(before, `data: {"type":"ping"}`); n < 3 {
		t.Fatalf("%d pings while the run was quiet, want 3 or more:\n%s", n, out)
	}
	if n := strings.Count(out, "event: message_start"); n != 1 {
		t.Fatalf("%d message_start in the reply, want 1:\n%s", n, out)
	}
	// the run's own start, after the one sent for it, still counts
	if u.Input != 4242 || !strings.Contains(out, `"input_tokens":4242`) {
		t.Fatalf("the run's start lost its usage: %+v\n%s", u, out)
	}
}
