package provider

import (
	"os"
	"slices"
	"testing"
	"time"
)

// ikunbots on X: Claude Code removed from magpie sat under "Removed from
// magpie, still signed in", and its Sign out… changed nothing. Claude Code
// is never signed out by magpie (its sign-in may be in the keychain), so
// ForgetAccount was refused by ForgetLogin and the row stayed for good.
// Signing out a removed Claude Code now leaves Claude Code as it is and
// takes the row off the list: it no longer shows among the removed.
func TestForgetRemovedClaudeCodeLeavesTheList(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	shellFakes(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, home+"/.claude.json", map[string]any{"oauthAccount": map[string]any{"emailAddress": "cc@example.com"}})
	rememberLogins(true)
	if _, ok := liveLogin("claude"); !ok {
		t.Fatal("no Claude Code sign-in")
	}
	id := ""
	for _, a := range Accounts() {
		if a.Account != nil && a.Account.Agent == "claude" {
			id = a.ID
		}
	}
	if id == "" {
		t.Fatal("Claude Code is no account")
	}
	if err := Delete(id); err != nil {
		t.Fatal(err)
	}
	if err := QuietAccount(id); err != nil {
		t.Fatal(err)
	}
	row := func() (Exclusion, bool) {
		i := slices.IndexFunc(Excluded(), func(x Exclusion) bool { return x.Provider == id })
		if i < 0 {
			return Exclusion{}, false
		}
		return Excluded()[i], true
	}
	if x, ok := row(); !ok || !x.Quiet {
		t.Fatalf("before: %+v %v", x, ok)
	}
	if err := ForgetAccount(id); err != nil {
		t.Fatalf("Sign out of the removed Claude Code: %v", err)
	}
	ForgetAccounts()
	if x, ok := row(); ok {
		t.Fatalf("still listed among the removed after Sign out: %+v", x)
	}
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("Claude Code's own sign-in went: %v", err)
	}
}
