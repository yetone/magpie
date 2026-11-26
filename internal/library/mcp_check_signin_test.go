package library

import (
	"context"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// A server magpie is signed in to is checked as the gateway relays it: a
// token the server ended early is renewed and asked again with, so the row
// doesn't say "needs sign-in" beside "Signed in" (新v学员 on Discord). A
// token refused just after it was renewed, or one that can't be renewed,
// is a sign-in that has run out: the check says so, and the record does,
// for the tag to read "Sign-in ran out". The check isn't run again for it.
func TestCheckRenewsMagpiesSignIn(t *testing.T) {
	sandbox(t)
	checked.Lock()
	checked.m = map[string]checkedServer{}
	checked.Unlock()
	f := mcpauthtest.New(t)
	ok(t)(SaveServer("", Server{Name: "neon", Transport: "http", URL: f.URL, Agents: []string{}}))
	f.SignIn(t, "neon")

	f.Revoke()
	h := CheckServer(context.Background(), &Server{Name: "neon", Transport: "http", URL: f.URL})
	if h.State != "ok" || f.Refreshed != 1 || h.SignIn == nil || !h.SignIn.SignedIn || h.SignIn.Dead {
		t.Fatalf("a token the server ended early: %+v, renewed %d", h, f.Refreshed)
	}

	f.RefuseTokens()
	m, err := CheckServers(context.Background(), []string{"neon"}, true)
	if err != nil {
		t.Fatal(err)
	}
	h = m["neon"]
	if h.State != "auth" || !h.OAuth || f.Refreshed != 2 || h.SignIn == nil || !h.SignIn.Dead {
		t.Fatalf("a renewed token refused: %+v, renewed %d", h, f.Refreshed)
	}
	if st := mcpauth.StatusOf("neon", f.URL); !st.Dead {
		t.Fatalf("the record after a renewed token was refused: %+v", st)
	}
	// as it is now, it was checked
	calls := len(f.Calls)
	m, _ = CheckServers(context.Background(), []string{"neon"}, false)
	if m["neon"].State != "auth" || f.Refreshed != 2 || len(f.Calls) != calls {
		t.Fatalf("checked again: %+v, renewed %d", m["neon"], f.Refreshed)
	}
}

// A sign-in the server won't renew has run out, as the check finds it.
func TestCheckSaysSignInRanOut(t *testing.T) {
	sandbox(t)
	f := mcpauthtest.New(t)
	ok(t)(SaveServer("", Server{Name: "neon", Transport: "http", URL: f.URL, Agents: []string{}}))
	f.SignIn(t, "neon")
	f.Revoke()
	f.ForgetRefresh()
	h := CheckServer(context.Background(), &Server{Name: "neon", Transport: "http", URL: f.URL})
	if h.State != "auth" || !h.OAuth || h.SignIn == nil || !h.SignIn.Dead {
		t.Fatalf("a sign-in that won't renew: %+v", h)
	}
}

// #7451 (Asher-S-Wu): a renewal the token endpoint turns away with 429 is
// a sign-in that waits, not one that needs signing in again: the check
// says the renewal is rate-limited, beside a sign-in still standing, and
// a second check in the wait doesn't ask the server again.
func TestCheckSaysRenewalRateLimited(t *testing.T) {
	sandbox(t)
	f := mcpauthtest.New(t)
	ok(t)(SaveServer("", Server{Name: "canva", Transport: "http", URL: f.URL, Agents: []string{}}))
	f.SignIn(t, "canva")
	f.Revoke()
	f.Throttle("60")
	for i := range 2 {
		h := CheckServer(context.Background(), &Server{Name: "canva", Transport: "http", URL: f.URL})
		if h.State == "auth" || h.Why != "ratelimited" || !strings.Contains(h.Detail, "Too many refresh_token requests") && i == 0 {
			t.Fatalf("check %d of a renewal answered 429: %+v", i, h)
		}
		if h.SignIn == nil || !h.SignIn.SignedIn || h.SignIn.Dead || h.SignIn.RetryAt == 0 {
			t.Fatalf("check %d: sign-in %+v", i, h.SignIn)
		}
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("asked the server to renew %d times, want 1", n)
	}
}
