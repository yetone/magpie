package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// spentBackend is the ChatGPT backend turning every turn away as refuse
// does, counting what it was asked.
type spentBackend struct {
	refuse func(w http.ResponseWriter)
	n      atomic.Int32
}

func (b *spentBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.n.Add(1)
	// no Content-Type unless refuse says one, as the backend streams
	w.Header()["Content-Type"] = nil
	b.refuse(w)
}

// spentAccount is Codex signed in to ChatGPT with its one account, the
// ChatGPT backend b, and the Codex subscription falling back to fb/m
// when fallback is set.
func spentAccount(t *testing.T, b *spentBackend, fallback bool) *scripted {
	t.Helper()
	codexSignedIn(t)
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	fb := &scripted{replies: []reply{{200, "text/event-stream", sse(
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from the fallback"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"f1","status":"completed","output":[]}}`)}}}
	if fallback {
		responsesOn(t, "fb", fb)
		if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fb/m"}}); err != nil {
			t.Fatal(err)
		}
	}
	if id, routed := codexAccounts(httptest.NewRequest("POST", CodexPath+"/responses", nil), "gpt-5.5"); routed {
		t.Fatalf("one account goes through routing as %s", id)
	}
	return fb
}

// Codex's one ChatGPT account out of its allowance, the Codex subscription
// with a fallback set in magpie (the user's own API key, another
// provider): the turn Codex relays on its sign-in goes on to the fallback,
// in every shape the backend says the account is out, as a turn through
// magpie's accounts does (adorable_piglet on Discord: the Plus account's
// quota ran out and Codex was handed the refusal, never the fallback).
// The account is asked once; the refusal never reaches Codex.
func TestCodexOneAccountSpentFallsBack(t *testing.T) {
	for _, x := range codexSpent {
		t.Run(x.name, func(t *testing.T) {
			b := &spentBackend{refuse: x.refuse}
			fb := spentAccount(t, b, true)
			code, body := codexPost(t, oneAccountTurn)
			if code != 200 || !strings.Contains(body, "from the fallback") || strings.Contains(body, "usage_limit_reached") ||
				strings.Contains(body, "rate_limit_exceeded") || strings.Contains(body, "usage_not_included") {
				t.Fatalf("%d %q", code, body)
			}
			if b.n.Load() != 1 || fb.n != 1 {
				t.Fatalf("ChatGPT asked %d times, the fallback %d", b.n.Load(), fb.n)
			}
		})
	}
}

// With no fallback set, Codex still has the backend's own refusal, as it
// had (TestCodexOwnModelOneAccountRelayed): nowhere else to go.
func TestCodexOneAccountSpentNoFallbackRelayed(t *testing.T) {
	b := &spentBackend{refuse: codexSpent[0].refuse}
	spentAccount(t, b, false)
	code, body := codexPost(t, oneAccountTurn)
	if code != 429 || !strings.Contains(body, "usage_limit_reached") || b.n.Load() != 1 {
		t.Fatalf("%d %q, asked %d", code, body, b.n.Load())
	}
}

// A sign-in the backend refuses (401) goes to Codex as it came, fallback
// or not: Codex renews its own token on it, which a fallback's answer
// would keep it from doing.
func TestCodexOneAccountSignInRefusedNotFallenBack(t *testing.T) {
	b := &spentBackend{refuse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Provided authentication token is expired. Please try signing in again.","code":"token_expired"}}`))
	}}
	fb := spentAccount(t, b, true)
	code, body := codexPost(t, oneAccountTurn)
	if code != 401 || !strings.Contains(body, "token_expired") || fb.n != 0 {
		t.Fatalf("%d %q, fallback asked %d", code, body, fb.n)
	}
}
