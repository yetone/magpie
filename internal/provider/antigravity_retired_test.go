package provider

import (
	"context"
	"testing"
	"time"
)

// Antigravity's built-in is retired (11-15): Google bans accounts it sees
// on Code Assist through Antigravity's client. A saved account is still
// read by its mover, but nothing lists, reads usage for or asks Google
// with it, while Gemini CLI's accounts, on the same code, are untouched.
func TestRetiredAntigravityAsksGoogleNothing(t *testing.T) {
	f := &fakeGoogle{quota: `{"buckets":[]}`, models: `{"models":{}}`}
	googleSandbox(t, f)
	exp := time.Now().Add(time.Hour).UnixMilli()
	if err := addGoogleLogin("antigravity", "ag@example.com", "", googleAuth{AccessToken: "at-ag", RefreshToken: "rt-ag", Project: "proj-ag", Expiry: exp}); err != nil {
		t.Fatal(err)
	}
	ForgetAccounts()

	if !PluginServes("antigravity") {
		t.Fatal("antigravity's built-in is not retired")
	}
	if ls := googleLogins("antigravity"); len(ls) != 0 {
		t.Fatalf("googleLogins(antigravity) = %d accounts, want none: the built-in serves nothing", len(ls))
	}
	if _, ok := googleAccountOf("antigravity"); ok {
		t.Fatal("googleAccountOf(antigravity) gave an account")
	}
	if ls := googleSavedLogins("antigravity"); len(ls) != 1 || ls[0].User != "ag@example.com" {
		t.Fatalf("googleSavedLogins(antigravity) = %+v, want the saved account for its mover", ls)
	}
	if ms := movingOf(t, "antigravity"); len(ms) != 1 {
		t.Fatalf("the mover carries %d accounts, want 1: %+v", len(ms), ms)
	}
	for _, q := range fetchSubscriptionUsage(context.Background()) {
		if q.Provider == "antigravity" {
			t.Fatalf("usage has an Antigravity card: %+v", q)
		}
	}
	f.mu.Lock()
	asked := len(f.heads)
	f.mu.Unlock()
	if asked != 0 {
		t.Fatalf("Google was asked %d times with only an Antigravity account saved", asked)
	}

	// Gemini CLI's accounts, on the same code, are served as before
	if err := addGoogleLogin("gemini", "g@example.com", "", googleAuth{AccessToken: "at-g", RefreshToken: "rt-g", Project: "proj-g", Expiry: exp}); err != nil {
		t.Fatal(err)
	}
	ForgetAccounts()
	if ls := googleLogins("gemini"); len(ls) != 1 {
		t.Fatalf("googleLogins(gemini) = %d accounts, want 1", len(ls))
	}
}
