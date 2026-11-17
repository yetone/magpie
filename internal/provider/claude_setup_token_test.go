package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSetupToken = "sk-ant-oat01-setuptoken-abcdefghijklmnopqrstuvwxyz0123456789"

// sfss5362 on X: "支持 CLAUDE_CODE_OAUTH_TOKEN 吗". A token `claude
// setup-token` printed is added as an account of magpie's own: kept in
// logins.json, named without an email, given to Claude Code for its own
// config directory only, and Claude Code's own sign-in left as it was.
func TestClaudeSetupTokenAccount(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "pro@x.com",
		"organizationUuid": "org-pro", "organizationName": "pro@x.com's Organization"}})
	forgetAccountCaches()
	rememberLogins(true)
	before, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}

	user, err := AddClaudeSetupToken("  CLAUDE_CODE_OAUTH_TOKEN=" + testSetupToken + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if user == "" || strings.Contains(user, "sk-ant") || strings.Contains(user, testSetupToken[len(testSetupToken)-6:]) {
		t.Fatalf("named %q: no name, or a name showing the token", user)
	}
	if after, _ := os.ReadFile(cred); string(after) != string(before) {
		t.Fatal("adding a setup-token wrote Claude Code's own sign-in")
	}
	// kept as a credential is, in logins.json
	if b, _ := os.ReadFile(loginsPath()); !strings.Contains(string(b), testSetupToken) {
		t.Fatal("the token isn't kept in logins.json")
	}
	var got *Login
	for _, l := range Logins("claude") {
		if l.User == user {
			got = &l
		}
	}
	if got == nil {
		t.Fatalf("%q isn't listed: %+v", user, Logins("claude"))
	}
	if got.Active || !got.On || !got.Token || got.Lapsed != "" {
		t.Fatalf("setup-token account listed as %+v, want on beside Claude Code's own, not active", *got)
	}
	// the same token again is the same account
	if again, err := AddClaudeSetupToken(testSetupToken); err != nil || again != user {
		t.Fatalf("the same token again: %q, %v", again, err)
	}
	if n := len(Logins("claude")); n != 2 {
		t.Fatalf("%d Claude accounts, want 2", n)
	}

	// its directory holds no sign-in, and its token is given there alone
	dir, err := claudeSavedDir(user)
	if err != nil {
		t.Fatal(err)
	}
	if dir != claudeAccountDir(user) {
		t.Fatalf("dir %q, want %q", dir, claudeAccountDir(user))
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("a sign-in was put in the setup-token account's directory: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".claude.json")); !strings.Contains(string(b), "hasCompletedOnboarding") {
		t.Fatalf("no onboarding in its .claude.json: %s", b)
	}
	if tok := ClaudeDirToken(dir); tok != testSetupToken {
		t.Fatalf("ClaudeDirToken(own dir) = %q", tok)
	}
	if tok := ClaudeDirToken(claudeAccountDir("pro@x.com · pro@x.com's Organization")); tok != "" {
		t.Fatal("another account's directory was given the token")
	}
	if tok := ClaudeDirToken(""); tok != "" {
		t.Fatal("Claude Code's own run was given the token")
	}

	// Claude Code's own sign-in isn't written with it: a switch to it
	// goes in settings.json (claude_token_env_test.go)
	if err := SwitchLogin("claude", user); err != nil {
		t.Fatalf("switch onto a setup-token account: %v", err)
	}
	if after, _ := os.ReadFile(cred); string(after) != string(before) {
		t.Fatal("a switch wrote the token into Claude Code's own sign-in")
	}
	// its version is the token's hash, never the token
	if v := ClaudeLoginVersion(user); v == "" || strings.Contains(v, testSetupToken) {
		t.Fatalf("version %q", v)
	}
}

// With Claude Code signed out, the first account added is signed in to in
// Claude Code; a setup-token never is.
func TestClaudeSetupTokenSignsClaudeCodeInToNothing(t *testing.T) {
	home := claudeHome(t)
	user, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("Claude Code was signed in to a setup-token: %v", err)
	}
	ls := Logins("claude")
	if len(ls) != 1 || ls[0].User != user || ls[0].Active || !ls[0].On || ls[0].Lapsed != "" {
		t.Fatalf("listed as %+v", ls)
	}
}

// A setup-token has the inference scope only: Claude Code's /usage shows
// the API cost view for it. Its allowance is unknown, never none, and
// /usage isn't run for it.
func TestClaudeSetupTokenAllowanceUnknown(t *testing.T) {
	claudeHome(t)
	var text atomic.Value
	text.Store("Total cost:            $0.0000\nTotal duration (API):  0s")
	runs := fakeClaudeUsage(t, &text, nil)
	user, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ws, err := readSavedClaudeUsage(ctx, user)
	if !errors.Is(err, errClaudeSetupToken) || len(ws) != 0 {
		t.Fatalf("read %+v, %v; want unknown", ws, err)
	}
	if runs.Load() != 0 {
		t.Fatal("/usage was run for a setup-token")
	}
	if claudeUsageDenied.MatchString(err.Error()) {
		t.Fatalf("%q reads as a refusal", err)
	}
	if !passing.MatchString(err.Error()) {
		t.Fatalf("%q isn't kept as saying nothing about the account", err)
	}
	RefreshUsage(ctx, "claude", user)
	q := LoginUsage(ctx, "claude")[user]
	if q.Error != errClaudeSetupToken.Error() || len(q.Windows) != 0 || runs.Load() != 0 {
		t.Fatalf("card %+v after %d runs; want unknown", q, runs.Load())
	}
}

func TestCheckClaudeSetupToken(t *testing.T) {
	for in, ok := range map[string]bool{
		testSetupToken:                        true,
		"'" + testSetupToken + "'":            true,
		"":                                    false,
		"sk-ant-api03-abc":                    false,
		"sk-ant-ort01-refresh":                false,
		"sk-ant-oat01-a b":                    false,
		`{"claudeAiOauth":{"accessToken":1}}`: false,
	} {
		tok, err := checkClaudeSetupToken(in)
		if (err == nil) != ok {
			t.Errorf("%q: %q %v", in, tok, err)
		}
		if err != nil && strings.Contains(err.Error(), in) && in != "" {
			t.Errorf("%q: the error repeats the token: %v", in, err)
		}
	}
}
