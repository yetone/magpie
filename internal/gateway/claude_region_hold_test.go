package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/egress"
	"github.com/yetone/magpie/internal/settings"
)

// claudePong is a turn of Claude Code's that answers "pong".
const claudePong = `    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
`

// regionLookedUp has egress's lookups answered by a server of the test's,
// which says the way out is in loc, and has one made for the Claude
// accounts' proxy choice ("" for magpie's own proxy setting).
func regionLookedUp(t *testing.T, loc string) {
	t.Helper()
	var at atomic.Value
	at.Store(loc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "fl=1\nip=203.0.113.9\nloc=%s\n", at.Load())
	}))
	t.Cleanup(srv.Close)
	url, client := egress.TraceURL, egress.Client
	egress.TraceURL, egress.Client = srv.URL, srv.Client()
	t.Cleanup(func() { egress.TraceURL, egress.Client = url, client; egress.Forget() })
	if r := egress.Check(context.Background(), ""); r.Loc != loc {
		t.Fatalf("looked up %+v", r)
	}
}

// @alterxyz4 on X: with "Hold requests outside supported regions" on, a
// Claude subscription's request whose way out is in a region Anthropic
// doesn't list goes nowhere: no Claude Code starts for any account, the
// client is told why, and as a request that never left nobody rests for
// it. Off, as by default, the same request goes as before.
func TestClaudeRegionHoldKeepsTheRequest(t *testing.T) {
	log := claudeSubscriptionFirstSays(t, claudePong)
	regionLookedUp(t, "CN")
	if err := settings.Save(settings.Settings{ClaudeRegionHold: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
	s := New()
	t.Cleanup(s.subscription.abortAll)
	ask := `{"model":"claude/claude-sonnet-5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`
	code, body := sendTo(s, "/v1/messages", ask)
	if code != http.StatusBadGateway || !strings.Contains(body, egress.HeldWords) || !strings.Contains(body, "CN") {
		t.Fatalf("status %d: %.400s", code, body)
	}
	if b, err := os.ReadFile(log); err == nil {
		t.Fatalf("Claude Code started for a held request: %q", b)
	}
	r := lastRoute(s)
	if len(r.Tries) < 2 {
		t.Fatalf("the spare wasn't asked: %+v", r.Tries)
	}
	for _, try := range r.Tries {
		if try.Fail != failProxy || try.Rest != nil {
			t.Fatalf("a held try read as the account's failure: %+v", try)
		}
	}
	// a warm-up or a usage read starts no Claude Code either
	if err := warmClaude(context.Background(), ""); err == nil || !strings.Contains(err.Error(), egress.HeldWords) {
		t.Fatalf("warm-up while held: %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("Claude Code started for a held warm-up")
	}

	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	if code, body := sendTo(s, "/v1/messages", ask); code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("with the hold off: status %d: %.400s", code, body)
	}
}

// A way out in a supported region, or one the lookup couldn't tell,
// holds nothing even with the hold on.
func TestClaudeRegionHoldLetsSupportedGo(t *testing.T) {
	for _, loc := range []string{"US", "XX"} {
		t.Run(loc, func(t *testing.T) {
			claudeSubscriptionFirstSays(t, claudePong)
			regionLookedUp(t, loc)
			if err := settings.Save(settings.Settings{ClaudeRegionHold: true}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
			s := New()
			t.Cleanup(s.subscription.abortAll)
			if code, body := sendTo(s, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`); code != 200 || !strings.Contains(body, "pong") {
				t.Fatalf("status %d: %.400s", code, body)
			}
		})
	}
}
