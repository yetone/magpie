package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Claude Code wired to magpie, its accounts served by magpie: the user logs
// out of Claude Code (it says to, over magpie's ANTHROPIC_AUTH_TOKEN), and
// the accounts saved in magpie are still served — the one it was signed in
// to first, in a config directory of its own, the others on behind it — and
// none of them is dropped from the list (StringKe on Discord: all six went).
func TestClaudeAccountsOutliveLogout(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	noAnthropic(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "own@example.com"}})
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": "r-" + tok, "expiresAt": far, "subscriptionType": "max"}}
	}
	seen := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	writeFile(t, loginsPath(), []map[string]any{
		{"agent": "claude", "user": "on@example.com", "plan": "max", "on": true, "seen": seen, "auth": oauth("tok-on")},
		{"agent": "claude", "user": "off@example.com", "plan": "pro", "seen": seen, "auth": oauth("tok-off")},
	})
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()
	if p, ok := find(All(), "claude"); !ok || p.Account.User != "own@example.com" {
		t.Fatalf("signed in: %v %+v", ok, p.Account)
	}

	// /logout: Claude Code's sign-in is gone
	os.Remove(cred)
	os.Remove(filepath.Join(home, ".claude.json"))
	forgetClaudeCredential()
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "own@example.com" {
		t.Fatalf("logged out: %v %+v", ok, p.Account)
	}
	dir, own, err := p.Account.Token(context.Background())
	if err != nil || !own || dir != claudeAccountDir("own@example.com") {
		t.Fatalf("dir %q %v %v", dir, own, err)
	}
	if c, ok := readClaudeDir(dir); !ok || c.OAuth.RefreshToken != "sk-ant-ort01-old" {
		t.Fatalf("dir credentials %+v", c.OAuth)
	}
	var also []string
	for _, q := range p.AlsoOn() {
		also = append(also, q.Account.User)
	}
	if len(also) != 1 || also[0] != "on@example.com" {
		t.Fatalf("also on: %v", also)
	}
	ls := Logins("claude")
	if len(ls) != 3 {
		t.Fatalf("logins: %+v", ls)
	}
	for _, l := range ls {
		if l.Active || l.On != (l.User != "off@example.com") {
			t.Fatalf("login %+v", l)
		}
	}
	if u := InUseLogin("claude"); u != "own@example.com" {
		t.Fatalf("in use %q", u)
	}
	for _, x := range savedButSignedOut() {
		if x.Agent == "claude" {
			t.Fatalf("excluded: %+v", x)
		}
	}

	// it can be paused behind the one on, as when Claude Code was signed in to it
	if err := SetLoginOn("claude", "own@example.com", false); err != nil {
		t.Fatal(err)
	}
	if p, _ = find(All(), "claude"); !p.OwnPaused() {
		t.Fatal("not paused")
	}
	if u := InUseLogin("claude"); u != "on@example.com" {
		t.Fatalf("in use %q", u)
	}
}

// Logged out of Claude Code with accounts in magpie, an account signed in
// in magpie stays magpie's: Claude Code isn't signed in to it again, which
// would only bring back its warning over magpie's token.
func TestClaudeSignInWhileLoggedOut(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	noAnthropic(t)
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, loginsPath(), []map[string]any{
		{"agent": "claude", "user": "old@example.com", "plan": "max", "seen": time.Now().Add(-time.Hour).UTC(),
			"auth": map[string]any{"claudeAiOauth": map[string]any{"accessToken": "tok-old", "refreshToken": "r-old", "expiresAt": far}}},
	})
	fakeClaudeLogin(t, fakeClaudeAccount{email: "new@example.com", plan: "pro", refresh: "sk-ant-ort01-new"}, "", true)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	if st = waitDone(t, st.ID); st.State != "done" || st.Using {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("Claude Code signed in: %v", err)
	}
	forgetClaudeCredential()
	var users []string
	for _, l := range Logins("claude") {
		if l.On {
			users = append(users, l.User)
		}
	}
	if len(users) != 2 {
		t.Fatalf("on: %v", users)
	}
}
