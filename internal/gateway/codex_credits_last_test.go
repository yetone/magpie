package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// gokoshiki on Discord: a Codex subscription, then an OpenAI API key, then
// the subscription's credits. An account at 100% that holds credits keeps
// its place by default (In order: asked first, and spends them); set to
// spend them last, the next account not on its credits is asked first,
// the account behind it, and the trace says why; turned on again it is
// back in its place.
func TestCodexCreditsLastAfterTheOthers(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t) // every account answers: the first on its credits
	creditsUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20}, "me@example.com")
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	again := func(want string) *Server {
		t.Helper()
		b.mu.Lock()
		b.tried = nil
		b.mu.Unlock()
		srv := New()
		code, body := resetPost(t, srv)
		if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "pong") || tried != want {
			t.Fatalf("%d %s tried %q, want %q", code, body, tried, want)
		}
		return srv
	}
	again("acct-1") // in its place

	if err := provider.SetCodexCreditsMode("Me@Example.com", provider.CreditsLast); err != nil {
		t.Fatal(err)
	}
	srv := again("acct-2")
	rs := srv.Trace(t.Context(), 0, 0).Routes
	if len(rs) == 0 || len(rs[0].Order) != 2 || rs[0].Order[0].Who != "spare@example.com" || rs[0].Order[0].CreditsLast ||
		rs[0].Order[1].Who != "me@example.com" || !rs[0].Order[1].CreditsLast || !rs[0].Order[1].Credits {
		t.Fatalf("order %+v", rs)
	}

	// below its windows' end it isn't on its credits: in its place still
	creditsUsage(t, map[string]float64{"me@example.com": 60, "spare@example.com": 20}, "me@example.com")
	again("acct-1")

	creditsUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20}, "me@example.com")
	if err := provider.SetCodexCredits("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if provider.CodexCreditsLast("me@example.com") {
		t.Fatal("on is in its place: last kept")
	}
	again("acct-1")
}

// The credits come after a provider's fallback too: both accounts at 100%
// on their credits and set to spend them last, an API key the provider
// falls back to answers first; with the credits in their place, an
// account answers on them as before.
func TestCodexCreditsLastAfterTheFallback(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	creditsUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 100}, "me@example.com", "spare@example.com")
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	fb := &keyed{}
	serveOn(t, "spare", "kf", []string{"m2"}, fb)
	p, _ := provider.Find("codex")
	p.Fallback = []string{"spare/m2"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	post := func() (int, string) {
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
		return postAs(t, New(), "", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	}
	code, body := post()
	if tried, _ := b.seen(); code != 200 || tried != "acct-1" || len(fb.tried) != 0 {
		t.Fatalf("in their place: %d %s, codex tried %q, fallback %v", code, body, tried, fb.tried)
	}
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		if err := provider.SetCodexCreditsMode(u, provider.CreditsLast); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	code, body = post()
	if tried, _ := b.seen(); code != 200 || !strings.Contains(body, "from kf") || tried != "" {
		t.Fatalf("last: %d %s, codex tried %q", code, body, tried)
	}
}

// A conversation an account on its credits answered, set to spend them
// last, isn't kept on it by its cache once one not on its credits leads;
// a turn going round, or a new conversation spread, never lands on it
// past that one either.
func TestCodexCreditsLastNotKept(t *testing.T) {
	acct := func(u string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: u}}, model: "gpt-5.5", rest: "codex/" + u}
	}
	cs := []candidate{acct("spare@example.com"), acct("me@example.com")}
	pl := planned{order: []Weighed{{Who: "spare@example.com"}, {Who: "me@example.com", Credits: true, CreditsLast: true}}}
	h := http.Header{}
	h.Set("x-claude-code-session-id", "credits-last-kept")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	_, _, _, key := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, body, body, cs, pl)
	answered(key, cs[1], 1, Usage{CacheRead: 50_000})
	t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })
	got, _, aff, _ := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, body, body, cs, pl)
	if got[0].rest != "codex/spare@example.com" || aff.Kept || aff.Why != "credits" {
		t.Fatalf("first %s, kept %v, why %s", got[0].rest, aff.Kept, aff.Why)
	}

	// every one on its credits and last: kept where it was
	both := planned{order: []Weighed{{Who: "spare@example.com", CreditsLast: true}, {Who: "me@example.com", CreditsLast: true}}}
	got, _, aff, _ = affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, body, body, cs, both)
	if got[0].rest != "codex/me@example.com" || !aff.Kept {
		t.Fatalf("all last: first %s, kept %v, why %s", got[0].rest, aff.Kept, aff.Why)
	}

	// a turn after spare's goes round to spare again, not to me
	if got, _ := after(append([]candidate(nil), cs...), planned{order: append([]Weighed(nil), pl.order...)}, 0); got[0].rest != "codex/spare@example.com" {
		t.Fatalf("turn went to %s", got[0].rest)
	}
	if readyForNew(pl.order, 1) {
		t.Fatal("a new conversation may go to the one on its credits")
	}
}
