package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// With magpie wired into Claude Code, `claude auth status` tells of magpie's
// token: signed in, no email. The account is named from ~/.claude.json as
// its saved login is, so it is the one Active and not also served beside
// itself; the env magpie runs in is not handed to the CLI (#177).
func TestClaudeAccountNamedAsItsLogin(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "magpie")
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": "r-" + tok, "expiresAt": far, "subscriptionType": "max"}}
	}
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), oauth("tok-me"))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "me@example.com"}})
	writeFile(t, filepath.Join(filepath.Dir(Path()), "logins.json"), []map[string]any{
		{"agent": "claude", "user": "me@example.com", "plan": "max", "on": true, "auth": oauth("tok-me-old")},
		{"agent": "claude", "user": "spare@example.com", "plan": "max", "on": true, "auth": oauth("tok-spare")},
	})
	dir := t.TempDir()
	exe, env := filepath.Join(dir, "claude"), filepath.Join(dir, "env")
	os.WriteFile(exe, []byte("#!/bin/sh\necho \"token=$ANTHROPIC_AUTH_TOKEN\" > "+env+"\necho '{\"loggedIn\":true,\"authMethod\":\"oauth_token\",\"apiProvider\":\"firstParty\"}'\n"), 0o755)
	claudeExecutable = func() string { return exe }
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	p, ok := claudeAccount()
	if !ok || p.Account.User != "me@example.com" {
		t.Fatalf("account %v %+v", ok, p.Account)
	}
	if b, _ := os.ReadFile(env); string(b) != "token=\n" {
		t.Fatalf("auth status ran with %q", b)
	}
	var also []string
	for _, q := range p.AlsoOn() {
		also = append(also, q.Account.User)
	}
	if len(also) != 1 || also[0] != "spare@example.com" {
		t.Fatalf("also on: %v", also)
	}
}

// a second look at Claude Code's sign-in serves the last answer at once and
// asks the CLI again behind it (#123)
func TestClaudeIdentityServedWhileAsked(t *testing.T) {
	dir := t.TempDir()
	exe, who := filepath.Join(dir, "claude"), filepath.Join(dir, "who")
	os.WriteFile(who, []byte("a@example.com"), 0o600)
	os.WriteFile(exe, []byte("#!/bin/sh\nsleep 1\nprintf '{\"loggedIn\":true,\"email\":\"%s\",\"subscriptionType\":\"max\"}' \"$(cat "+who+")\"\n"), 0o755)
	old := claudeExecutable
	claudeExecutable = func() string { return exe }
	t.Cleanup(func() { claudeExecutable = old; forgetClaudeStatus() })
	forgetClaudeStatus()

	if u, p, out := claudeIdentity(); u != "a@example.com" || p != "max" || out {
		t.Fatalf("first: %q %q %v", u, p, out)
	}
	os.WriteFile(who, []byte("b@example.com"), 0o600)
	claudeStatusMu.Lock()
	claudeStatusAt = time.Now().Add(-time.Minute)
	claudeStatusMu.Unlock()
	start := time.Now()
	if u, _, _ := claudeIdentity(); u != "a@example.com" {
		t.Fatalf("stale look: %q", u)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("a stale look waited %v for the CLI", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if u, _, _ := claudeIdentity(); u == "b@example.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer behind the last one never came")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
