package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
)

// envTokenOf is CLAUDE_CODE_OAUTH_TOKEN in Claude Code's settings.json.
func envTokenOf(t *testing.T) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(claudeSettingsFile())
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	v := gjson.GetBytes(b, "env."+claudeTokenEnvKey)
	return v.String(), v.Exists()
}

func claudeLoginOf(t *testing.T, user string) Login {
	t.Helper()
	for _, l := range Logins("claude") {
		if l.User == user {
			return l
		}
	}
	t.Fatalf("%q isn't listed: %+v", user, Logins("claude"))
	return Login{}
}

// liukaybee on X: accounts added by `claude setup-token`, and Claude Code
// itself switched among them, never connected to magpie. Use on such an
// account puts its token in Claude Code's settings.json env, which Claude
// Code takes over its stored sign-in; that sign-in is left as it is, and a
// switch to a signed-in account takes the token out again.
func TestClaudeCodeSwitchedToSetupToken(t *testing.T) {
	claudeThree(t) // Claude Code on the Pro, the Team and Max saved
	pro, team := "pro@x.com", "team@x.com · Acme"
	cred := claudeSettingsFile()
	cred = filepath.Join(filepath.Dir(cred), ".credentials.json")
	before, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	// the user's own env stays beside magpie's token
	writeFile(t, claudeSettingsFile(), map[string]any{"env": map[string]any{"FOO": "bar"}, "model": "opus"})
	tok, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}

	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	if v, _ := envTokenOf(t); v != testSetupToken {
		t.Fatalf("settings.json's %s = %q, want the setup-token", claudeTokenEnvKey, v)
	}
	if after, _ := os.ReadFile(cred); string(after) != string(before) {
		t.Fatal("a switch to a setup-token wrote Claude Code's stored sign-in")
	}
	b, _ := os.ReadFile(claudeSettingsFile())
	if gjson.GetBytes(b, "env.FOO").String() != "bar" || gjson.GetBytes(b, "model").String() != "opus" {
		t.Fatalf("the user's settings were lost: %s", b)
	}
	if b, _ := os.ReadFile(claudeTokenRecordPath()); strings.Contains(string(b), testSetupToken) {
		t.Fatal("the record keeps the token, not its hash")
	}
	if l := claudeLoginOf(t, tok); !l.Uses || l.Active {
		t.Fatalf("setup-token listed as %+v, want Uses and not Active", l)
	}
	if l := claudeLoginOf(t, pro); !l.Active || l.Uses {
		t.Fatalf("Claude Code's sign-in listed as %+v, want Active", l)
	}
	if err := SwitchLogin("claude", tok); err != nil { // again: nothing to do
		t.Fatal(err)
	}

	// back to the account it is signed in to: the token goes
	if err := SwitchLogin("claude", pro); err != nil {
		t.Fatal(err)
	}
	if _, has := envTokenOf(t); has {
		t.Fatal("a switch back to the sign-in left the setup-token in settings.json")
	}
	if _, err := os.Stat(claudeTokenRecordPath()); !os.IsNotExist(err) {
		t.Fatalf("the record stayed: %v", err)
	}
	if b, _ := os.ReadFile(claudeSettingsFile()); gjson.GetBytes(b, "env.FOO").String() != "bar" {
		t.Fatalf("the user's env was lost: %s", b)
	}
	if l := claudeLoginOf(t, tok); l.Uses {
		t.Fatal("still listed as used")
	}

	// to another signed-in account: that one's sign-in, and the token goes
	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", team); err != nil {
		t.Fatal(err)
	}
	if _, has := envTokenOf(t); has {
		t.Fatal("a switch to another account left the setup-token in settings.json")
	}
	if l := claudeLoginOf(t, team); !l.Active {
		t.Fatalf("%s listed as %+v, want Active", team, l)
	}
}

// A CLAUDE_CODE_OAUTH_TOKEN the user set in settings.json themselves is
// theirs: magpie writes none over it, and takes it out on no switch.
func TestClaudeOwnEnvTokenLeftAlone(t *testing.T) {
	claudeThree(t)
	team := "team@x.com · Acme"
	writeFile(t, claudeSettingsFile(), map[string]any{"env": map[string]any{claudeTokenEnvKey: "sk-ant-oat01-users-own"}})
	tok, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", tok); err == nil || !strings.Contains(err.Error(), "of your own") {
		t.Fatalf("switch over the user's own token: %v", err)
	}
	if err := SwitchLogin("claude", team); err != nil {
		t.Fatal(err)
	}
	if v, _ := envTokenOf(t); v != "sk-ant-oat01-users-own" {
		t.Fatalf("the user's own token became %q", v)
	}
	if claudeLoginOf(t, tok).Uses {
		t.Fatal("the user's own token was taken for magpie's")
	}

	// magpie's, then changed by the user: theirs from then on
	writeFile(t, claudeSettingsFile(), map[string]any{})
	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	writeFile(t, claudeSettingsFile(), map[string]any{"env": map[string]any{claudeTokenEnvKey: "sk-ant-oat01-users-later"}})
	if err := SwitchLogin("claude", "max@x.com"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envTokenOf(t); v != "sk-ant-oat01-users-later" {
		t.Fatalf("the user's later token became %q", v)
	}
}

// A settings.json that can't be read holds what it holds: no token is
// written into it, and the record of magpie's stays until it can be read.
func TestClaudeEnvTokenUnreadableSettings(t *testing.T) {
	claudeThree(t)
	tok, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	broken := []byte(`{"env": {"` + claudeTokenEnvKey + `": "` + testSetupToken + `",`)
	if err := os.WriteFile(claudeSettingsFile(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", "team@x.com · Acme"); err == nil {
		t.Fatal("a switch past an unreadable settings.json said it took the token out")
	}
	if b, _ := os.ReadFile(claudeSettingsFile()); string(b) != string(broken) {
		t.Fatalf("an unreadable settings.json was written: %s", b)
	}
	if claudeLoginOf(t, "team@x.com · Acme").Active {
		t.Fatal("Claude Code's sign-in was switched under a token that couldn't be taken out")
	}
	if _, err := os.Stat(claudeTokenRecordPath()); err != nil {
		t.Fatalf("the record went with the token still there: %v", err)
	}
	// readable again: the token goes on the next switch
	writeFile(t, claudeSettingsFile(), map[string]any{"env": map[string]any{claudeTokenEnvKey: testSetupToken}})
	if err := SwitchLogin("claude", "team@x.com · Acme"); err != nil {
		t.Fatal(err)
	}
	if _, has := envTokenOf(t); has {
		t.Fatal("the token stayed")
	}
}

// Removing the setup-token Claude Code uses takes it out of settings.json:
// Claude Code is on its own sign-in again.
func TestClaudeSetupTokenRemovedLeavesSettings(t *testing.T) {
	claudeThree(t)
	tok, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	if _, has := envTokenOf(t); has {
		t.Fatal("a removed account's token stayed in settings.json")
	}
	if _, err := os.Stat(claudeTokenRecordPath()); !os.IsNotExist(err) {
		t.Fatalf("the record stayed: %v", err)
	}
}

// Claude Code on a setup-token the user picked is left there by magpie's
// own switching (Switch accounts only), whose allowance it can't read:
// the spent sign-in under it is no reason to move.
func TestClaudeSetupTokenInUseNotMovedOff(t *testing.T) {
	claudeThree(t) // pro@x.com signed in; team and max saved
	var text atomic.Value
	text.Store("Current session: 100% used")
	fakeClaudeUsage(t, &text, nil)
	forget := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}
	forget()
	t.Cleanup(forget)
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	for _, l := range Logins("claude") {
		if !l.Active {
			if err := SetLoginOn("claude", l.User, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir == "" {
			return "Current session: 100% used", nil
		}
		return "Current session: 10% used", nil
	})
	tok, err := AddClaudeSetupToken(testSetupToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("claude", tok); err != nil {
		t.Fatal(err)
	}
	forget()
	if _, to, _, ok := NextLogin(context.Background(), "claude"); ok || to != "" {
		t.Fatalf("NextLogin moved Claude Code off the setup-token to %q", to)
	}
	if v, _ := envTokenOf(t); v != testSetupToken {
		t.Fatalf("settings.json's token became %q", v)
	}
	// back on its sign-in, magpie moves it as before
	if err := SwitchLogin("claude", "pro@x.com"); err != nil {
		t.Fatal(err)
	}
	forget()
	if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to == "" {
		t.Fatalf("NextLogin no longer moves the spent sign-in: %q ok=%v", to, ok)
	}
}
