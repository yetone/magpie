package gateway

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// heardUsage is creditsUsage for Codex accounts whose credits are known
// only from what their replies say (noteCodexCredits) or set: their
// /wham/usage reading tells none. It returns the set, by lower-cased user.
func heardUsage(t *testing.T, used map[string]float64) *sync.Map {
	t.Helper()
	var heard sync.Map
	oldA, oldN := allowances, noteCodexCredits
	t.Cleanup(func() { allowances, noteCodexCredits = oldA, oldN })
	noteCodexCredits = func(agent, user string, on bool) { heard.Store(strings.ToLower(user), on) }
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, n := range used {
			c, _ := heard.Load(strings.ToLower(u))
			on, _ := c.(bool)
			out[u] = provider.Allowance{
				{Used: n, Resets: time.Now().Add(2 * time.Hour), Span: 5 * time.Hour, Credits: on},
				{Used: n, Resets: time.Now().Add(3 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Credits: on},
			}
		}
		return out
	}
	return &heard
}

// @123xtv03k on X: Codex by itself goes on on the credits an account
// bought once its plan's allowance runs out; through magpie the account
// was benched until its window renewed. ChatGPT says the credits on the
// 429 itself (x-codex-credits-has-credits, as Codex reads them), and the
// refused turn is asked again of the same account at once, as one known
// from /wham/usage to hold credits is (TestCodexCreditsAskedAgainAtTheEdge).
func TestCodexCreditsHeardOnTheRefusal(t *testing.T) {
	t.Run("routed", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		b := newResetBackend(t, "acct-1")
		b.credits["acct-1"], b.edge["acct-1"], b.sayCredits = true, 1, true
		heardUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20})
		if err := provider.SetRouting("codex", provider.Ordered); err != nil {
			t.Fatal(err)
		}
		srv := New()
		code, body := resetPost(t, srv)
		if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1,acct-1" {
			t.Fatalf("%d %s tried %s", code, body, tried)
		}
		if ts := triesOf(srv); len(ts) != 2 || !ts[0].Credits || ts[0].Rest != nil {
			t.Fatalf("traced %+v", ts)
		}
	})
	t.Run("relayed", func(t *testing.T) {
		codexSignedIn(t)
		b := newResetBackend(t, "acct-1")
		b.credits["acct-1"], b.edge["acct-1"], b.sayCredits = true, 1, true
		heardUsage(t, map[string]float64{"me@example.com": 100})
		code, body := resetPost(t, New())
		if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1,acct-1" {
			t.Fatalf("%d %s tried %s", code, body, tried)
		}
	})
	t.Run("relayed, refused twice", func(t *testing.T) {
		// asked again on its credits and refused again: held a moment, as
		// the accounts loop rests one (fallbackCooldown), not till the
		// window renews two hours on
		codexSignedIn(t)
		b := newResetBackend(t, "acct-1")
		b.credits["acct-1"], b.edge["acct-1"], b.sayCredits = true, 2, true
		heardUsage(t, map[string]float64{"me@example.com": 100})
		if code, _ := resetPost(t, New()); code != 429 {
			t.Fatalf("refused twice: %d", code)
		}
		h, ok := codexHeld("me@example.com", time.Now())
		if ok && time.Until(h.until) > fallbackCooldown {
			t.Fatalf("held %v", time.Until(h.until).Round(time.Second))
		}
	})
	t.Run("says none", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		b := newResetBackend(t, "acct-1")
		b.sayCredits = true
		heardUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20})
		if err := provider.SetRouting("codex", provider.Ordered); err != nil {
			t.Fatal(err)
		}
		srv := New()
		if code, _ := resetPost(t, srv); code != 200 {
			t.Fatalf("%d", code)
		}
		if tried, _ := b.seen(); tried != "acct-1,acct-2" {
			t.Fatalf("tried %s", tried)
		}
		if ts := triesOf(srv); len(ts) == 0 || ts[0].Credits || ts[0].Rest == nil || time.Until(ts[0].Rest.Until) < time.Hour {
			t.Fatalf("traced %+v", ts)
		}
	})
}

// An account turned away as out of its allowance before magpie knew it
// held credits — bought after the plan ran out, or its reading failed —
// rested until its window renewed. Once a reply or a reading says it holds
// them (provider.OnCredits), the rest is lifted and it is asked again at
// once; so is the hold of Codex's own sign-in, relayed.
func TestCodexCreditsKnownLiftsTheRest(t *testing.T) {
	t.Run("routed", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		b := newResetBackend(t, "acct-1")
		heard := heardUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20})
		if err := provider.SetRouting("codex", provider.Ordered); err != nil {
			t.Fatal(err)
		}
		srv := New()
		if code, _ := resetPost(t, srv); code != 200 {
			t.Fatalf("%d", code)
		}
		if tried, _ := b.seen(); tried != "acct-1,acct-2" {
			t.Fatalf("tried %s", tried)
		}
		if ts := triesOf(srv); len(ts) == 0 || ts[0].Rest == nil || time.Until(ts[0].Rest.Until) < time.Hour {
			t.Fatalf("not rested till its window renews: %+v", ts)
		}
		// credits bought: the next reply or reading says so
		b.mu.Lock()
		b.tried, b.credits["acct-1"] = nil, true
		b.mu.Unlock()
		heard.Store("me@example.com", true)
		provider.NoteCodexCredits("codex", "me@example.com", false) // what another test left
		provider.NoteCodexCredits("codex", "me@example.com", true)
		if code, body := resetPost(t, srv); code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("%d %s", code, body)
		}
		if tried, _ := b.seen(); tried != "acct-1" {
			t.Fatalf("with its credits known, tried %s", tried)
		}
	})
	t.Run("relayed", func(t *testing.T) {
		codexSignedIn(t)
		b := newResetBackend(t, "acct-1")
		heard := heardUsage(t, map[string]float64{"me@example.com": 100})
		srv := New()
		if code, _ := resetPost(t, srv); code != http.StatusTooManyRequests {
			t.Fatalf("%d", code)
		}
		if _, ok := codexHeld("me@example.com", time.Now()); !ok {
			t.Fatal("not held")
		}
		b.mu.Lock()
		b.tried, b.credits["acct-1"] = nil, true
		b.mu.Unlock()
		heard.Store("me@example.com", true)
		provider.NoteCodexCredits("codex", "me@example.com", false) // what another test left
		provider.NoteCodexCredits("codex", "me@example.com", true)
		if code, body := resetPost(t, srv); code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("%d %s", code, body)
		}
	})
}

// The x-codex-credits-* headers and a codex.rate_limits event's credits
// are read as Codex reads them: both bools said, a spent balance none,
// the image allowance's headers not the account's.
func TestCodexCreditsRead(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	for _, c := range []struct {
		h      http.Header
		on, ok bool
	}{
		{h("x-codex-credits-has-credits", "true", "x-codex-credits-unlimited", "false", "x-codex-credits-balance", "28263.2358130000"), true, true},
		{h("x-codex-credits-has-credits", "True", "x-codex-credits-unlimited", "0"), true, true},
		{h("x-codex-credits-has-credits", "false", "x-codex-credits-unlimited", "1"), true, true},
		{h("x-codex-credits-has-credits", "false", "x-codex-credits-unlimited", "false"), false, true},
		{h("x-codex-credits-has-credits", "true", "x-codex-credits-unlimited", "false", "x-codex-credits-balance", "0"), false, true},
		{h("x-codex-credits-has-credits", "true"), false, false},
		{h("x-codex-credits-has-credits", "yes", "x-codex-credits-unlimited", "false"), false, false},
		{h("x-codex-active-limit", "imagegen_premium", "x-codex-credits-has-credits", "true", "x-codex-credits-unlimited", "false"), false, false},
	} {
		if on, ok := codexHeaderCredits(c.h); on != c.on || ok != c.ok {
			t.Errorf("%v: %v %v, want %v %v", c.h, on, ok, c.on, c.ok)
		}
	}
	for _, c := range []struct {
		data   string
		on, ok bool
	}{
		{`{"type":"codex.rate_limits","rate_limits":{},"credits":{"has_credits":true,"unlimited":false,"balance":"12.5"}}`, true, true},
		{`{"type":"codex.rate_limits","credits":{"has_credits":true,"unlimited":false,"balance":null}}`, true, true},
		{`{"type":"codex.rate_limits","credits":{"has_credits":false,"unlimited":false}}`, false, true},
		{`{"type":"codex.rate_limits","rate_limits":{}}`, false, false},
		{`{"type":"codex.rate_limits","metered_limit_name":"gpt-5.5-pro","credits":{"has_credits":true,"unlimited":false}}`, false, false},
		{`{"type":"response.created","credits":{"has_credits":true,"unlimited":false}}`, false, false},
	} {
		if on, ok := codexEventCredits([]byte(c.data)); on != c.on || ok != c.ok {
			t.Errorf("%s: %v %v, want %v %v", c.data, on, ok, c.on, c.ok)
		}
	}
}
