package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Claude Code on Windows rewrites ~/.claude/.credentials.json and
// ~/.claude.json in place when magpie has the file open (its rename gets
// EPERM, and it truncates and writes the file instead), so a read can find
// it empty or cut short. That is no sign-out: the account Claude Code holds
// stays its own and in use, never marked "Sign-in required", and its
// sign-in is never put in a config directory of magpie's, where a second
// Claude Code would refresh the same refresh token and sign the first out
// (xbpb6 on X: signed out again and again on Windows). One unreadable for
// longer than a write takes is gone after all.
func TestClaudeHalfWrittenSignInIsNoLogout(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	noAnthropic(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	profile := filepath.Join(home, ".claude.json")
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "own@example.com"}})
	good, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	goodProfile, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	ForgetAccounts()
	own := func(when string) Login {
		t.Helper()
		ForgetAccounts()
		forgetClaudeCredential()
		for _, l := range Logins("claude") {
			if l.User == "own@example.com" {
				return l
			}
		}
		t.Fatalf("%s: no own account in %+v", when, Logins("claude"))
		return Login{}
	}
	if l := own("signed in"); !l.Active || l.Lapsed != "" {
		t.Fatalf("signed in: %+v", l)
	}
	// whatever reads the account as not in use for a moment (warm-up, a
	// card's reading, a request served beside it) is given no folder of
	// its own for it while Claude Code holds it
	if dir, err := claudeSavedDir("own@example.com"); err == nil || ClaudeSignInRequired(err.Error()) {
		t.Fatalf("Claude Code's own sign-in put in %q: %v", dir, err)
	}
	if _, err := os.Stat(claudeAccountDir("own@example.com")); !os.IsNotExist(err) {
		t.Fatalf("a second holder of Claude Code's refresh token: %v", err)
	}

	for _, c := range []struct {
		name          string
		path          string
		half, restore []byte
	}{
		{"credentials empty", cred, nil, good},
		{"credentials cut short", cred, good[:len(good)/2], good},
		{"profile cut short", profile, goodProfile[:len(goodProfile)/2], goodProfile},
	} {
		if err := os.WriteFile(c.path, c.half, 0o600); err != nil {
			t.Fatal(err)
		}
		if l := own(c.name); !l.Active || l.Lapsed != "" {
			t.Fatalf("%s: taken for a sign-out: %+v", c.name, l)
		}
		if dir, err := claudeSavedDir("own@example.com"); err == nil {
			t.Fatalf("%s: Claude Code's own sign-in put in %s", c.name, dir)
		}
		if _, err := os.Stat(claudeAccountDir("own@example.com")); !os.IsNotExist(err) {
			t.Fatalf("%s: a second holder of Claude Code's refresh token: %v", c.name, err)
		}
		if err := os.WriteFile(c.path, c.restore, 0o600); err != nil {
			t.Fatal(err)
		}
		if l := own(c.name + ", written"); !l.Active || l.Lapsed != "" {
			t.Fatalf("%s, written: %+v", c.name, l)
		}
	}

	// unreadable past what a write takes: gone after all
	old := claudeUnsureGrace
	claudeUnsureGrace = 0
	t.Cleanup(func() { claudeUnsureGrace = old })
	if err := os.WriteFile(cred, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if l := own("left empty"); l.Active || l.Lapsed != claudeLogoutLapse {
		t.Fatalf("left empty: %+v", l)
	}
}
