package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// Saved accounts of an agent not signed in where magpie looks (a magpie
// serve under another HOME) are said to be left out, not dropped silently.
func TestSavedButSignedOut(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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

// Claude accounts saved in magpie are offered while Claude Code is signed
// out — no credentials, or credentials Claude Code says are signed out —
// and are not said to be left out; one whose saved sign-in is gone is
// listed as signed out, not dropped.
func TestSavedButSignedOutClaudeServed(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := writeLogins([]savedLogin{
		{Agent: "claude", User: "a@x.com", Auth: []byte(`{}`)},
		{Agent: "claude", User: "b@x.com", Auth: []byte(`{"claudeAiOauth":{"accessToken":"tok-b","refreshToken":"r-b"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	check := func(state string) {
		t.Helper()
		forgetClaudeCredential()
		forgetClaudeStatus()
		for _, x := range Excluded() {
			if x.Agent == "claude" {
				t.Fatalf("%s: excluded %+v", state, x)
			}
		}
		if p, ok := claudeAccount(); !ok || p.Account.User != "b@x.com" {
			t.Fatalf("%s: account %v %+v", state, ok, p.Account)
		}
		ls := Logins("claude")
		if len(ls) != 2 || ls[0].User != "a@x.com" || ls[0].Lapsed == "" || ls[1].Lapsed != "" || !ls[1].On {
			t.Fatalf("%s: logins %+v", state, ls)
		}
	}
	check("no credentials")
	shellFakes(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	exe := filepath.Join(home, "claude")
	testenv.Program(t, exe, "#!/bin/sh\necho '{\"loggedIn\": false}'\n")
	claudeExecutable = func() string { return exe }
	check("auth status signed out")
}

// A saved Claude account whose Claude Code is signed out (a banned account
// logged out, say) is listed, so it can be removed from magpie; removing
// one drops it from magpie's store and leaves Claude Code's own files as
// they are.
func TestSavedButSignedOutCanBeRemoved(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := writeLogins([]savedLogin{
		{Agent: "claude", User: "banned@x.com", Auth: []byte(`{}`)},
		{Agent: "codex", User: "c@x.com", Auth: []byte(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("claude"); len(ls) != 1 || ls[0].User != "banned@x.com" {
		t.Fatalf("claude: %+v", ls)
	}
	if err := ForgetLogin("claude", "banned@x.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := claudeAccount(); ok {
		t.Error("removed, yet served")
	}
	if ls := readLogins(); len(ls) != 1 || ls[0].Agent != "codex" {
		t.Errorf("logins: %+v", ls)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("Claude Code's folder was touched: %v", err)
	}
}

// Claude Code's sign-in is read from the keychain as Claude Code reads it,
// under its account: another item for the same service (one an earlier
// sign-in left) that comes first by service alone isn't taken for it.
func TestClaudeKeychainReadsItsAccount(t *testing.T) {
	shellFakes(t)
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("USER", "tester")
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o755)
	// the leftover comes first unless the account is asked for
	testenv.Program(t, filepath.Join(bin, "security"), `#!/bin/sh
case "$*" in
*"-a tester"*) echo '{"claudeAiOauth":{"accessToken":"live","subscriptionType":"max"}}' ;;
*) echo '{}' ;;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeKeychain = true
	forgetClaudeCredential()
	c, loc, ok := claudeCredential()
	if !ok || c.OAuth.AccessToken != "live" || !loc.keychain || loc.account != "tester" {
		t.Fatalf("read %v %+v %+v", ok, c.OAuth, loc)
	}
}
