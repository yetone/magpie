package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Saved accounts of an agent not signed in where magpie looks (a magpie
// serve under another HOME) are said to be left out, not dropped silently.
func TestSavedButSignedOut(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := writeLogins([]savedLogin{
		{Agent: "codex", User: "a@x.com", Auth: []byte(`{}`)},
		{Agent: "codex", User: "b@x.com", Auth: []byte(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	var codex *Exclusion
	for _, x := range Excluded() {
		if x.Agent == "codex" && x.SignedOut {
			codex = &x
		}
		if x.Agent == "claude" {
			t.Errorf("claude has no saved accounts, yet: %+v", x)
		}
	}
	if codex == nil || !strings.Contains(codex.Why, "2 accounts are") || !strings.Contains(codex.Why, filepath.Join(home, ".codex", "auth.json")) {
		t.Fatalf("codex: %+v", codex)
	}
	// signed in: nothing to say
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"x","account_id":"acc-1"}}`), 0o600)
	for _, x := range Excluded() {
		if x.SignedOut {
			t.Errorf("signed in, yet: %+v", x)
		}
	}
}

// A Claude account saved in magpie but not offered says which check found
// no sign-in: no credentials, or credentials Claude Code says are signed out.
func TestSavedButSignedOutClaudeSaysWhy(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := writeLogins([]savedLogin{{Agent: "claude", User: "a@x.com", Auth: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	why := func() string {
		forgetClaudeCredential()
		forgetClaudeStatus()
		for _, x := range Excluded() {
			if x.Agent == "claude" && x.SignedOut {
				return x.Why
			}
		}
		return ""
	}
	if w := why(); !strings.Contains(w, "nothing at "+filepath.Join(home, ".claude", ".credentials.json")) {
		t.Fatalf("no credentials: %q", w)
	}
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	exe := filepath.Join(home, "claude")
	os.WriteFile(exe, []byte("#!/bin/sh\necho '{\"loggedIn\": false}'\n"), 0o755)
	claudeExecutable = func() string { return exe }
	if w := why(); !strings.Contains(w, "claude auth status says no one is signed in") {
		t.Fatalf("signed out: %q", w)
	}
}

// Claude Code's sign-in is read from the keychain as Claude Code reads it,
// under its account: another item for the same service (one an earlier
// sign-in left) that comes first by service alone isn't taken for it.
func TestClaudeKeychainReadsItsAccount(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("USER", "tester")
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o755)
	// the leftover comes first unless the account is asked for
	os.WriteFile(filepath.Join(bin, "security"), []byte(`#!/bin/sh
case "$*" in
*"-a tester"*) echo '{"claudeAiOauth":{"accessToken":"live","subscriptionType":"max"}}' ;;
*) echo '{}' ;;
esac
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeKeychain = true
	forgetClaudeCredential()
	c, loc, ok := claudeCredential()
	if !ok || c.OAuth.AccessToken != "live" || !loc.keychain || loc.account != "tester" {
		t.Fatalf("read %v %+v %+v", ok, c.OAuth, loc)
	}
}
