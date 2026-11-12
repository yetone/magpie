package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// nearCapBackend is a ChatGPT backend for the near-cap tests: each
// account's five hours read at used[acct] by /wham/usage, every turn held
// until open is closed. A turn's reply says the account is at heard[acct]
// (its codex.rate_limits event), or nothing of it when heard has none; once
// a turn has ended, /wham/usage says after[acct] when it has one.
type nearCapBackend struct {
	mu          sync.Mutex
	used, after map[string]float64
	heard       map[string]float64
	tried       []string
	ended       int
	open        chan struct{}
	five, week  int64
}

func newNearCapBackend(t *testing.T, used, heard, after map[string]float64) *nearCapBackend {
	t.Helper()
	now := time.Now()
	b := &nearCapBackend{used: used, heard: heard, after: after, open: make(chan struct{}),
		five: now.Add(2 * time.Hour).Unix(), week: now.Add(4 * 24 * time.Hour).Unix()}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			io.ReadAll(r.Body)
			b.mu.Lock()
			b.tried = append(b.tried, acct)
			b.mu.Unlock()
			select {
			case <-b.open:
			case <-r.Context().Done():
				return
			}
			var lines []string
			if n, ok := b.heard[acct]; ok {
				lines = append(lines, `event: codex.rate_limits`+"\n"+`data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":`+jsonFloat(n)+`,"window_minutes":300,"reset_at":`+jsonInt(b.five)+`},"secondary":{"used_percent":10,"window_minutes":10080,"reset_at":`+jsonInt(b.week)+`}},"credits":null,"metered_limit_name":null,"limit_name":null}`)
			}
			lines = append(lines,
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`)
			io.WriteString(w, sse(lines...))
			b.mu.Lock()
			b.ended++
			b.mu.Unlock()
		case "/backend-api/wham/usage":
			b.mu.Lock()
			used := b.used[acct]
			if n, ok := b.after[acct]; ok && b.ended > 0 {
				used = n
			}
			b.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{"allowed": true,
					"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 18000, "reset_at": b.five},
					"secondary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 604800, "reset_at": b.week}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() {
		select {
		case <-b.open:
		default:
			close(b.open)
		}
	})
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return b
}

func jsonFloat(n float64) string {
	bs, _ := json.Marshal(n)
	return string(bs)
}

func (b *nearCapBackend) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.tried...)
}

// nearCapAccounts, Codex's accounts signed in (me@example.com, and the
// spares) and the fake backend up, routes them in order, caps me at capAt
// and reads every one from the backend, waiting for me's reading to be
// want.
func nearCapAccounts(t *testing.T, capAt int, want float64, spares ...string) {
	t.Helper()
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountCap("codex", "me@example.com", capAt); err != nil {
		t.Fatal(err)
	}
	users := append([]string{"me@example.com"}, spares...)
	// a reading begun now, not one an earlier test left that already
	// says want: the reading out would land after this test's reply was
	// heard and put its account back under its cap
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !provider.ReadUsageNow(ctx, "codex", users...) {
		t.Fatal("never read")
	}
	t.Cleanup(func() {
		for _, u := range users {
			provider.StaleAllowance("codex", u)
		}
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n, _ := provider.Allowances("codex")["me@example.com"].For("gpt-5.5", time.Now())
		if n == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never read: %v%%", n)
		}
	}
}

type nearReply struct {
	code int
	body string
}

// postAtOnce sends n turns at once, each in its own goroutine.
func postAtOnce(t *testing.T, srv *Server, n int) chan nearReply {
	out := make(chan nearReply, n)
	for range n {
		go func() {
			code, body := resetPost(t, srv)
			out <- nearReply{code, body}
		}()
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

// dyzdyz010 on Discord: an account capped at 90% was used to 100%. Every
// turn sent at once to an account a point under its cap was planned on the
// same reading, so each went to it, and the account went as far past its
// cap as they took between them. Within nearCapPoints of a cap an
// account takes one turn at a time: the others go to the next account.
func TestNearCapTakesOneTurnAtATime(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newNearCapBackend(t, map[string]float64{"acct-1": 89, "acct-2": 20},
		map[string]float64{"acct-1": 92, "acct-2": 21}, nil)
	nearCapAccounts(t, 90, 89, "spare@example.com")
	srv := New()
	const n = 4
	replies := postAtOnce(t, srv, n)
	// every turn is at the vendor, before any is answered: each was
	// routed on the reading of 89%
	waitFor(t, "every turn sent", func() bool { return len(b.sent()) == n })
	close(b.open)
	for range n {
		if r := <-replies; r.code != 200 || !strings.Contains(r.body, "pong") {
			t.Fatalf("%d %s", r.code, r.body)
		}
	}
	got := map[string]int{}
	for _, a := range b.sent() {
		got[a]++
	}
	if got["acct-1"] != 1 || got["acct-2"] != n-1 {
		t.Fatalf("sent %v; want one turn to the account near its cap, the rest to the other", got)
	}
}

// The account near its cap the only one: a turn waits for the one before
// it, and is then held by what that one's reply said (92%), not sent past
// the cap on the old reading.
func TestNearCapWaitsThenHolds(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 89}, map[string]float64{"acct-1": 92}, nil)
	nearCapAccounts(t, 90, 89)
	srv := New()
	replies := postAtOnce(t, srv, 2)
	waitFor(t, "one turn sent, one waiting", func() bool { return len(b.sent()) == 1 && srv.nearCap.waiting() == 1 })
	close(b.open)
	codes := map[int]int{}
	var held string
	for range 2 {
		r := <-replies
		codes[r.code]++
		if r.code == 429 {
			held = r.body
		}
	}
	if codes[200] != 1 || codes[429] != 1 || len(b.sent()) != 1 {
		t.Fatalf("codes %v, sent %v; want one answered, one held at the cap", codes, b.sent())
	}
	if !strings.Contains(held, "usage cap") {
		t.Fatalf("held as %s", held)
	}
}

// A reply that tells nothing of the account's usage: the turn that waited
// for it reads the usage again before it goes, and is held at the cap the
// reading says it has reached.
func TestNearCapReadsAgainAfterASilentReply(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 89}, nil, map[string]float64{"acct-1": 93})
	nearCapAccounts(t, 90, 89)
	srv := New()
	replies := postAtOnce(t, srv, 2)
	waitFor(t, "one turn sent, one waiting", func() bool { return len(b.sent()) == 1 && srv.nearCap.waiting() == 1 })
	close(b.open)
	codes := map[int]int{}
	for range 2 {
		codes[(<-replies).code]++
	}
	if codes[200] != 1 || codes[429] != 1 || len(b.sent()) != 1 {
		t.Fatalf("codes %v, sent %v; want the second read again and held", codes, b.sent())
	}
}

// Under its cap by more than nearCapPoints, turns go at once, together.
func TestFarFromCapTurnsGoTogether(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 50}, map[string]float64{"acct-1": 51}, nil)
	nearCapAccounts(t, 90, 50)
	srv := New()
	replies := postAtOnce(t, srv, 3)
	waitFor(t, "every turn sent", func() bool { return len(b.sent()) == 3 })
	close(b.open)
	for range 3 {
		if r := <-replies; r.code != 200 {
			t.Fatalf("%d %s", r.code, r.body)
		}
	}
}

// dyzdyz010's own case: Claude Code accounts, each capped at 90% of its
// five hours, Stop at the caps on. Turns sent together while the first
// account is a point under its cap: one goes to it, the other to the
// second account, not both past the first's cap on one reading.
func TestNearCapClaudeAccountsTakeOneTurnEach(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	log := claudeSubscriptionFirstSays(t, `    while [ ! -f `+gate+` ]; do sleep 0.05; done
    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
`)
	t.Cleanup(func() { os.WriteFile(gate, nil, 0o600) })
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	if err := provider.SetRouting("claude", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		if err := provider.SetAccountCap("claude", u, 90); err != nil {
			t.Fatal(err)
		}
	}
	five, week := time.Now().Add(2*time.Hour).Unix(), time.Now().Add(4*24*time.Hour).Unix()
	provider.NoteClaudeLimits("me@example.com", []provider.ClaudeLimit{{Kind: "five_hour", Used: 0.89, ResetsAt: five}, {Kind: "seven_day", Used: 0.1, ResetsAt: week}})
	provider.NoteClaudeLimits("spare@example.com", []provider.ClaudeLimit{{Kind: "five_hour", Used: 0.2, ResetsAt: five}, {Kind: "seven_day", Used: 0.1, ResetsAt: week}})
	t.Cleanup(func() {
		provider.StaleAllowance("claude", "me@example.com")
		provider.StaleAllowance("claude", "spare@example.com")
	})
	if n, _ := provider.Allowances("claude")["me@example.com"].For("claude-sonnet-5", time.Now()); n != 89 {
		t.Fatalf("read at %v%%", n)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	out := make(chan nearReply, 2)
	for i := range 2 {
		go func() {
			code, body := sendTo(s, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"ping `+strconv.Itoa(i)+`"}]}`)
			out <- nearReply{code, body}
		}()
	}
	// a Claude Code run on each account the turns went to: the account
	// near its cap may have a run started ahead for its next turn too
	waitFor(t, "a run on each account", func() bool {
		b, _ := os.ReadFile(log)
		return strings.Contains(string(b), "own") && strings.Contains(string(b), "tok-spare")
	})
	os.WriteFile(gate, nil, 0o600)
	for range 2 {
		if r := <-out; r.code != 200 || !strings.Contains(r.body, "pong") {
			t.Fatalf("%d %s", r.code, r.body)
		}
	}
	var got []string
	s.trace.mu.Lock()
	for _, r := range s.trace.routes {
		for _, try := range r.Tries {
			got = append(got, try.ID)
		}
	}
	s.trace.mu.Unlock()
	slices.Sort(got)
	if strings.Join(got, ",") != "claude,claude@spare@example.com" {
		t.Fatalf("tried %v; want one turn on the account near its cap, one on the spare", got)
	}
}

// A turn that waits for the one before it on an account near its cap has
// no try yet, and its row said "routing" till that one ended (tmr11235 on
// X, v0.1.3852): its route says queued, and the wait is one magpie knows
// of, so the watch on a request with no route doesn't end it as unknown.
func TestNearCapWaitSaysQueued(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 89}, map[string]float64{"acct-1": 89}, nil)
	nearCapAccounts(t, 90, 89)
	srv := New()
	replies := postAtOnce(t, srv, 2)
	waitFor(t, "one turn sent, one waiting", func() bool { return len(b.sent()) == 1 && srv.nearCap.waiting() == 1 })
	queued := func() (n int) {
		for _, r := range srv.Trace(context.Background(), 0, 0).Routes {
			if r.Phase == phaseQueue && len(r.Tries) == 0 {
				n++
			}
		}
		return
	}
	waitFor(t, "the waiting turn's route says queued", func() bool { return queued() == 1 })
	if !slices.ContainsFunc(srv.Active(), func(a Active) bool { return a.Phase == phaseQueue }) {
		t.Fatalf("active %v; want one queued", srv.Active())
	}
	close(b.open)
	for range 2 {
		<-replies
	}
	waitFor(t, "no route says queued once it ended", func() bool { return queued() == 0 })
	for _, r := range srv.Trace(context.Background(), 0, 0).Routes {
		if r.Phase != "" {
			t.Fatalf("route %d still says %q", r.ID, r.Phase)
		}
	}
}

// tmr11235 on X: one Codex account at 95% of its window, no cap and no
// concurrency set, and its requests queued. Only credits turned off held it
// (at 100%), a limit the user never set on usage, and with no other account
// to go to the turns wait for nothing but each other: they go out together.
func TestNearCapOnlyCreditsOffOneAccountSendsAtOnce(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 95}, map[string]float64{"acct-1": 95}, nil)
	nearCapAccounts(t, 0, 95)
	if err := provider.SetCodexCredits("me@example.com", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.SetCodexCredits("me@example.com", true) })
	srv := New()
	replies := postAtOnce(t, srv, 2)
	waitFor(t, "both turns out at the vendor at once", func() bool { return len(b.sent()) == 2 })
	if n := srv.nearCap.waiting(); n != 0 {
		t.Fatalf("%d turns waiting for a turn; want none", n)
	}
	close(b.open)
	for range 2 {
		if r := <-replies; r.code != 200 {
			t.Fatalf("status %d: %s", r.code, r.body)
		}
	}
}
