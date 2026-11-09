package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1797 (miniers): a conversation set to stay strictly with the account
// that answered it never moves to another. "strict" is spelled out here,
// not provider.AffinityStrict, for the test to say what the code before it
// did: it took "strict" for auto.

// strictSetup signs Codex in to two accounts, routed in order, its
// conversations staying strictly, with no conversation remembered.
func strictSetup(t *testing.T) *resetBackend {
	t.Helper()
	codexSignedIn(t, "spare@example.com")
	sticks.Lock()
	sticks.m, sticks.opening, sticks.from = map[string]stick{}, map[string]stick{}, ""
	sticks.Unlock()
	t.Cleanup(func() {
		sticks.Lock()
		sticks.m, sticks.opening, sticks.from = map[string]stick{}, map[string]stick{}, ""
		sticks.Unlock()
	})
	b := newResetBackend(t)
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("codex", "strict"); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("codex"); err != nil || p.Affinity != "strict" {
		t.Fatalf("saved as %+v, %v", p, err)
	}
	return b
}

// strictPost posts a turn of the Codex conversation session to srv, and
// says which accounts the backend was asked it by.
func strictPost(t *testing.T, srv *Server, b *resetBackend, session string) (int, string, string) {
	t.Helper()
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping `+session+`"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("session_id", session)
	srv.Handler().ServeHTTP(rec, req)
	tried, _ := b.seen()
	return rec.Code, rec.Body.String(), tried
}

// lastWhy is why the routing trace says the last request stayed or not.
func lastWhy(t *testing.T, srv *Server) string {
	rs := srv.Trace(t.Context(), 0, 0).Routes
	if len(rs) == 0 || rs[len(rs)-1].Affinity == nil {
		return ""
	}
	return rs[len(rs)-1].Affinity.Why
}

// The account a strict conversation is on runs out (429): its error goes
// to the agent, and nobody else is asked it — where "session" moves it to
// the next account, as it did. A new conversation goes to the next.
func TestStrictAffinityPassesTheErrorOn(t *testing.T) {
	b := strictSetup(t)
	srv := New()
	if code, body, tried := strictPost(t, srv, b, "s1"); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" {
		t.Fatalf("first turn: %d %s tried %s", code, body, tried)
	}
	b.mu.Lock()
	b.out["acct-1"] = true
	b.mu.Unlock()
	code, body, tried := strictPost(t, srv, b, "s1")
	if code != 429 || !strings.Contains(body, "usage limit") || tried != "acct-1" {
		t.Fatalf("strict, its account out: %d %s tried %s", code, body, tried)
	}
	if why := lastWhy(t, srv); why != "strict" {
		t.Fatalf("traced as %q", why)
	}
	// resting now till the time ChatGPT named, it is still its account's
	// error the agent gets, not another's answer, but given from the rest:
	// asked again on every retry, an account is flagged (timedHeld,
	// hsjzg58958 on X)
	if code, body, tried := strictPost(t, srv, b, "s1"); code != 429 || !strings.Contains(body, "usage limit") || tried != "" {
		t.Fatalf("strict, its account resting: %d %s tried %s", code, body, tried)
	}
	if code, body, tried := strictPost(t, srv, b, "s2"); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("a new conversation: %d %s tried %s", code, body, tried)
	}

	if err := provider.SetAffinity("codex", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	if code, body, tried := strictPost(t, srv, b, "s1"); code != 200 || !strings.Contains(body, "pong") || tried != "acct-2" {
		t.Fatalf("session moves it on: %d %s tried %s", code, body, tried)
	}
}

// The usage cap set on an account keeps new conversations off it, while a
// strict conversation already on it goes on there past the cap.
func TestStrictAffinityGoesPastTheCap(t *testing.T) {
	b := strictSetup(t)
	capUsage(t, map[string]float64{"me@example.com": 60, "spare@example.com": 20})
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	srv := New()
	if code, _, tried := strictPost(t, srv, b, "s1"); code != 200 || tried != "acct-1" {
		t.Fatalf("below the cap: %d tried %s", code, tried)
	}
	capUsage(t, map[string]float64{"me@example.com": 75, "spare@example.com": 20})
	if code, body, tried := strictPost(t, srv, b, "s1"); code != 200 || !strings.Contains(body, "pong") || tried != "acct-1" {
		t.Fatalf("strict, past the cap: %d %s tried %s", code, body, tried)
	}
	if why := lastWhy(t, srv); why != "strict" {
		t.Fatalf("traced as %q", why)
	}
	if code, _, tried := strictPost(t, srv, b, "s2"); code != 200 || tried != "acct-2" {
		t.Fatalf("a new conversation past the cap: %d tried %s", code, tried)
	}
}

// An account set not to spend its credits, at 100%: a strict conversation
// on it is refused, as held, rather than moved; nothing reaches the
// vendor. A new conversation goes to the next account.
func TestStrictAffinityHeldForCreditsIsRefused(t *testing.T) {
	b := strictSetup(t)
	capUsage(t, map[string]float64{"me@example.com": 50, "spare@example.com": 20})
	srv := New()
	if code, _, tried := strictPost(t, srv, b, "s1"); code != 200 || tried != "acct-1" {
		t.Fatalf("first turn: %d tried %s", code, tried)
	}
	capUsage(t, map[string]float64{"me@example.com": 100, "spare@example.com": 20})
	if err := provider.SetCodexCredits("me@example.com", false); err != nil {
		t.Fatal(err)
	}
	code, body, tried := strictPost(t, srv, b, "s1")
	if code != 429 || tried != "" || !strings.Contains(body, "me@example.com") || !strings.Contains(body, "Stays: Strict") {
		t.Fatalf("strict, held for its credits: %d %s tried %q", code, body, tried)
	}
	if code, _, tried := strictPost(t, srv, b, "s2"); code != 200 || tried != "acct-2" {
		t.Fatalf("a new conversation: %d tried %s", code, tried)
	}
}

// A strict group's member whose stream breaks off mid-reply, its 200
// already sent (imwhatiam123 on Discord: a group of Qoder CN and WorkBuddy
// AI set to Stays: Strict moved a session to WorkBuddy AI mid-way): the
// conversation isn't forgotten as it is under the other modes (#733), so
// the agent's retry goes back to the member it is on, failing there as
// strict says, and goes on there once it answers again. A new conversation
// goes to the other member while the broken one rests.
func TestStrictGroupKeepsItsMemberPastABrokenReply(t *testing.T) {
	fresh(t)
	const broke = "Unable to reach the model provider"
	var mu sync.Mutex
	var tried []string
	breaking := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		tried = append(tried, who)
		br := breaking && who == "first"
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if br {
			io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look"}}]}`,
				`data: {"error":{"message":"`+broke+`"}}`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"}}]}`,
			`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "qoder-like", Name: "Qoder-like", Key: "first", Chat: up.URL + "/v1", Models: []string{"m"}},
		{ID: "workbuddy-like", Name: "WorkBuddy-like", Key: "second", Chat: up.URL + "/v1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"qoder-like/m", "workbuddy-like/m"}, Routing: provider.Ordered, Affinity: "strict"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	post := func(session string) (string, string) {
		mu.Lock()
		tried = nil
		mu.Unlock()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi `+session+`"}]}`))
		req.Header.Set("x-session-id", session)
		s.Handler().ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		return rec.Body.String(), strings.Join(tried, ",")
	}
	if out, got := post("s1"); !strings.Contains(out, "from first") || got != "first" {
		t.Fatalf("first turn: %s tried %s", out, got)
	}
	mu.Lock()
	breaking = true
	mu.Unlock()
	if out, got := post("s1"); !strings.Contains(out, broke) || got != "first" {
		t.Fatalf("the broken reply: %s tried %s", out, got)
	}
	// the agent's retry stays on the member, resting or not
	if out, got := post("s1"); !strings.Contains(out, broke) || got != "first" {
		t.Fatalf("strict, the retry after a broken reply moved: %s tried %s", out, got)
	}
	if why := lastWhy(t, s); why != "strict" {
		t.Fatalf("the retry traced as %q, want strict", why)
	}
	// a new conversation goes elsewhere meanwhile
	if out, got := post("s2"); !strings.Contains(out, "from second") || got != "second" {
		t.Fatalf("a new conversation: %s tried %s", out, got)
	}
	mu.Lock()
	breaking = false
	mu.Unlock()
	if out, got := post("s1"); !strings.Contains(out, "from first") || got != "first" {
		t.Fatalf("strict, the member back: %s tried %s", out, got)
	}
}
