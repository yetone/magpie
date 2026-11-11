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

// What the Providers page says of Claude Code's sign-in (SignInMovesOf) is
// what the switch does: "moves" exactly when a spent account is moved off,
// at the share it names. The owner's Claude Code went unswitched for days
// under a "keep signed in" tick left on, with nothing on the page saying
// automatic switching was off (11-11).
func TestSignInMovesSaysWhatSwitchDoes(t *testing.T) {
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

	cases := []struct {
		name               string
		routed, only, keep bool
		routing            string
		mode               string
		share              float64
		back, kRouted      bool
	}{
		{name: "not routed", mode: "moves", share: SpentShare},
		{name: "not routed, in order", routing: Ordered, mode: "moves", share: 100, back: true},
		{name: "routed", routed: true, mode: "gateway"},
		{name: "routed, switch only", routed: true, only: true, mode: "moves", share: SpentShare},
		{name: "kept on the first", keep: true, mode: "kept"},
		{name: "kept, routed", keep: true, routed: true, mode: "kept", kRouted: true},
	}
	for _, c := range cases {
		routed = c.routed
		if err := SetRouting("claude", c.routing); err != nil {
			t.Fatal(err)
		}
		if err := SetSwitchOnly("claude", c.only); err != nil {
			t.Fatal(err)
		}
		if err := SetKeepLogin("claude", c.keep); err != nil {
			t.Fatal(err)
		}
		if err := SwitchLogin("claude", "a@example.com"); err != nil {
			t.Fatal(err)
		}
		setLoginReturn("claude", loginReturn{})
		m := SignInMovesOf("claude")
		if m == nil || m.Mode != c.mode || m.Share != c.share || m.Back != c.back || m.Routed != c.kRouted {
			t.Fatalf("%s: says %+v, want mode %s share %g back %v", c.name, m, c.mode, c.share, c.back)
		}
		to := switched()
		if moved := to != ""; moved != (c.mode == "moves") {
			t.Fatalf("%s: the page says %q but the switch moved to %q", c.name, c.mode, to)
		}
		if to != "" && signedInAs() != to {
			t.Fatalf("%s: switched to %s, signed in as %s", c.name, to, signedInAs())
		}
	}
	if SignInMovesOf("gemini") != nil {
		t.Fatal("an agent whose sign-in magpie never moves has a line")
	}
}
