package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Claude Code whose requests go through magpie's gateway stays signed in to
// the account the user put it on when that one is spent: the gateway moves
// its requests to another account with room by itself, and moving the
// sign-in took away what Claude Code has as the user's main account, its
// claude.ai connectors (MCP) among them. oreoft on Discord set it back
// more than ten times and each time magpie moved it to the quota account
// again. Not routed through magpie, or with magpie told only to switch
// its sign-in, it still moves (#208, TestClaudeSwitchedWhenSpent).
func TestClaudeRoutedThroughMagpieKeepsItsSignIn(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour)) // a@example.com, sk-ant-oat01-old
	writeFile(t, home+"/.claude.json", map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	loginsMu.Lock()
	ls := upsertLogin(readLogins(), savedLogin{Agent: "claude", User: "b@example.com", Plan: "max", On: true, Seen: time.Now().UTC(),
		Auth: mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"}}}),
		Profile: mustJSONRaw(t, map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"})})
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("usage endpoint read: %s", r.URL)
	}))
	defer fake.Close()
	claudeBase = fake.URL // isolate puts it back

	routed := true
	old := ClaudeThroughMagpie
	ClaudeThroughMagpie = func() bool { return routed }
	t.Cleanup(func() { ClaudeThroughMagpie = old })

	// a used up, b with room
	used := map[string]float64{"a@example.com": 100, "b@example.com": 10}
	switched := func() string {
		t.Helper()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		for user, u := range used {
			NoteClaudeLimits(user, []ClaudeLimit{{Kind: "five_hour", Used: u / 100, ResetsAt: time.Now().Add(time.Hour).Unix()}})
		}
		to, err := SwitchWhenSpent(context.Background(), "claude")
		if err != nil {
			t.Fatal(err)
		}
		return to
	}
	signedInAs := func() string {
		t.Helper()
		b, err := os.ReadFile(cred)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "sk-ant-ort01-b") {
			return "b@example.com"
		}
		return "a@example.com"
	}

	// routed: a stays signed in, spent or not, on every look
	for range 3 {
		if to := switched(); to != "" {
			t.Fatalf("Claude Code routed through magpie was signed in to %s, off the account the user put it on", to)
		}
	}
	if who := signedInAs(); who != "a@example.com" {
		t.Fatalf("Claude Code's live credential is %s's", who)
	}
	for _, l := range Logins("claude") {
		if l.User == "a@example.com" && !l.Active || l.User == "b@example.com" && l.Active {
			t.Fatalf("after: %+v", l)
		}
	}
	// told only to switch its sign-in, the gateway serves none of these
	// accounts: moving the sign-in is all magpie does, so it moves
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	if to := switched(); to != "b@example.com" {
		t.Fatalf("switch-only: switched to %q", to)
	}
	if err := SetSwitchOnly("claude", false); err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", "a@example.com"); err != nil {
		t.Fatal(err)
	}

	// not routed: Claude Code asks Anthropic as the account it is signed
	// in to, so it moves to the one with room (#208)
	routed = false
	if to := switched(); to != "b@example.com" {
		t.Fatalf("not routed: switched to %q", to)
	}
	if who := signedInAs(); who != "b@example.com" {
		t.Fatalf("not routed: Claude Code's live credential is %s's", who)
	}
}
