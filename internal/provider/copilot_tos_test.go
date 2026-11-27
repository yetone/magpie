package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// copilotToSBody is GitHub's 403 on the trade for a session token, word
// for word as yidasanqian's log has it (on X).
const copilotToSBody = `{"message":"Resource not accessible by integration. For more on scraping GitHub and how it may affect your rights, please review our Terms of Service (https://docs.github.com/en/site-policy/github-terms/github-terms-of-service)","documentation_url":"https://docs.github.com/rest","status":"403"}`

// tosCopilot is lapsedCopilot's machine, hubot's trade answered
// copilotToSBody with header h while refuse is set; it counts hubot's
// trades.
func tosCopilot(t *testing.T, h http.Header) (refuse *atomic.Bool, trades *atomic.Int32) {
	t.Helper()
	lapsedCopilot(t)
	refuse, trades = &atomic.Bool{}, &atomic.Int32{}
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token gho_kept" {
			trades.Add(1)
			if refuse.Load() {
				for k, v := range h {
					w.Header()[k] = v
				}
				w.WriteHeader(403)
				w.Write([]byte(copilotToSBody))
				return
			}
		}
		w.Write([]byte(`{"token":"tid","expires_at":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}`))
	}))
	t.Cleanup(exchange.Close)
	old := CopilotTokenURL
	CopilotTokenURL = exchange.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	return refuse, trades
}

// GitHub's refusal of a Copilot account under its terms is not its
// sign-in refused: the account isn't marked signed out (and a mark an
// earlier such refusal left goes), the error says what GitHub said and
// that signing in again doesn't help, and GitHub isn't asked again with
// the token while the refusal holds.
func TestCopilotToSRefusalIsNoSignOut(t *testing.T) {
	refuse, trades := tosCopilot(t, nil)
	refuse.Store(true)
	// left by the refusal read as a sign-out before
	copilotRefuse("gho_kept", http.StatusForbidden)
	for _, c := range copilotLogins(copilotConfigDir()) {
		if c.User == "hubot" {
			copilotLapse(c.app)
		}
	}
	resetCopilotState()
	if l := copilotLapsedOf(t, "hubot"); l != copilotLapsedText {
		t.Fatalf("not marked to begin with: %q", l)
	}
	for i := range 5 {
		err := copilotTrade(t, "hubot")
		if err == nil || !strings.Contains(err.Error(), "For more on scraping GitHub") || !strings.HasSuffix(err.Error(), CopilotToSHint) {
			t.Fatalf("try %d: %v", i, err)
		}
		if strings.Contains(err.Error(), "sign in to Copilot again") || strings.Contains(err.Error(), "signed out") {
			t.Fatalf("try %d told to sign in again: %v", i, err)
		}
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("five requests traded the token %d times", n)
	}
	if l := copilotLapsedOf(t, "hubot"); l != "" {
		t.Fatalf("the account reads %q", l)
	}
	// its allowance isn't asked for either while it holds
	var users atomic.Int32
	user := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { users.Add(1) }))
	defer user.Close()
	oldUser := CopilotUserURL
	CopilotUserURL = user.URL
	defer func() { CopilotUserURL = oldUser }()
	if q := loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"}); !strings.HasSuffix(q.Error, CopilotToSHint) {
		t.Fatalf("allowance: %+v", q)
	}
	if n := users.Load(); n != 0 {
		t.Fatalf("GitHub asked %d times for the allowance", n)
	}
}

// The hold lasts as long as GitHub's Retry-After says, and ends: the
// account is asked again once it is over.
func TestCopilotToSHoldEnds(t *testing.T) {
	refuse, trades := tosCopilot(t, http.Header{"Retry-After": {"120"}})
	refuse.Store(true)
	if err := copilotTrade(t, "hubot"); err == nil {
		t.Fatal("no error")
	}
	copilotToSHolds.Lock()
	h := copilotToSHolds.m["gho_kept"]
	if d := time.Until(h.until); d < 110*time.Second || d > 130*time.Second {
		copilotToSHolds.Unlock()
		t.Fatalf("held %s", d)
	}
	h.until = time.Now().Add(-time.Second)
	copilotToSHolds.m["gho_kept"] = h
	copilotToSHolds.Unlock()
	refuse.Store(false)
	if err := copilotTrade(t, "hubot"); err != nil {
		t.Fatalf("after the hold: %v", err)
	}
	if n := trades.Load(); n != 2 {
		t.Fatalf("traded %d times", n)
	}
}

// A token GitHub refused (401 Bad credentials) is traded once a while,
// not at every try: each try of a request, and the agent's retries,
// traded it again.
func TestCopilotRefusedTokenNotTradedAtEveryTry(t *testing.T) {
	lapsedCopilot(t)
	var n atomic.Int32
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`))
	}))
	defer exchange.Close()
	old := CopilotTokenURL
	CopilotTokenURL = exchange.URL
	defer func() { CopilotTokenURL = old }()
	for range 5 {
		if err := copilotTrade(t, "hubot"); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
			t.Fatalf("refused: %v", err)
		}
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("traded %d times", got)
	}
	if l := copilotLapsedOf(t, "hubot"); l != copilotLapsedText {
		t.Fatalf("the account reads %q", l)
	}
}

func TestCopilotToSWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{403, copilotToSBody, true},
		{403, `{"message":"API rate limit exceeded for user ID 1. If you reach out to GitHub Support for help, please include the request ID. For more on scraping GitHub and how it may affect your rights, please review our Terms of Service"}`, true},
		{429, `{"message":"You have exceeded a secondary rate limit."}`, true},
		{403, `{"message":"Bad credentials"}`, false},
		{403, `{"message":"Resource not accessible by integration","documentation_url":"https://docs.github.com/rest"}`, false},
		{401, copilotToSBody, false},
		{500, copilotToSBody, false},
	} {
		if got := copilotToSRefused(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %v", c.status, c.body, got)
		}
	}
}
