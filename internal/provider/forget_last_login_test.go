package provider

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// #874: a subscription signed in to one account of magpie's could never be
// signed out — "magpie uses … first; put another account first", with no
// other account to put first. The only one goes; with another there, the
// first still waits for it to be put first.
func TestForgetOnlyFirstLogin(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	claudeHome(t)
	auth := func(r string) json.RawMessage {
		b, _ := json.Marshal(googleAuth{RefreshToken: r})
		return b
	}
	saveLogins(t, savedLogin{Agent: "antigravity", User: "a@x.com", Auth: auth("ra"), On: true, First: true})
	ForgetAccounts()
	if ls := Logins("antigravity"); len(ls) != 1 || !ls[0].Active {
		t.Fatalf("logins before: %+v", ls)
	}
	if err := ForgetAccount("antigravity"); err != nil {
		t.Fatalf("signing out the only account: %v", err)
	}
	ForgetAccounts()
	if ls := Logins("antigravity"); len(ls) != 0 {
		t.Fatalf("still listed: %+v", ls)
	}
	for _, l := range readLogins() {
		if l.Agent == "antigravity" {
			t.Fatalf("logins.json still keeps %+v", l)
		}
	}

	// two accounts: Remove on the first puts the other first, even one
	// that was off, and signing the subscription out takes both
	saveLogins(t,
		savedLogin{Agent: "antigravity", User: "a@x.com", Auth: auth("ra"), On: true, First: true},
		savedLogin{Agent: "antigravity", User: "b@x.com", Auth: auth("rb")})
	ForgetAccounts()
	if err := ForgetLogin("antigravity", "a@x.com"); err != nil {
		t.Fatalf("removing the first of two: %v", err)
	}
	ForgetAccounts()
	if ls := Logins("antigravity"); len(ls) != 1 || ls[0].User != "b@x.com" || !ls[0].Active || !ls[0].On {
		t.Fatalf("after removing the first: %+v", ls)
	}
	saveLogins(t,
		savedLogin{Agent: "antigravity", User: "a@x.com", Auth: auth("ra"), On: true, First: true},
		savedLogin{Agent: "antigravity", User: "b@x.com", Auth: auth("rb"), On: true})
	ForgetAccounts()
	if err := ForgetAccount("antigravity"); err != nil {
		t.Fatalf("signing out both: %v", err)
	}
	ForgetAccounts()
	if ls := Logins("antigravity"); len(ls) != 0 {
		t.Fatalf("still listed: %+v", ls)
	}
}

// 歧路亡羊 on Discord: Copilot signed in to the editors' own account and
// two of magpie's, one of them first. Removing the first puts the next
// first, and Sign out… takes them all, where it said "magpie uses wwjxhy
// first; put another account first".
func TestForgetFirstCopilotLogin(t *testing.T) {
	signIn(t)
	writeFile(t, filepath.Join(copilotConfigDir(), "github-copilot", "apps.json"), map[string]any{"github.com": map[string]any{"user": "octo", "oauth_token": "gho_own"}})
	tok := func(s string) json.RawMessage { return json.RawMessage(`{"oauth_token":"` + s + `"}`) }
	saved := []savedLogin{
		{Agent: "copilot", User: "octo"},
		{Agent: "copilot", User: "wwjxhy", Auth: tok("gho_w"), On: true, First: true},
		{Agent: "copilot", User: "other", Auth: tok("gho_o"), On: true},
	}
	saveLogins(t, saved...)
	ForgetAccounts()
	if ls := Logins("copilot"); len(ls) != 3 || ls[0].User != "wwjxhy" || !ls[0].Active {
		t.Fatalf("logins before: %+v", ls)
	}
	if err := ForgetLogin("copilot", "wwjxhy"); err != nil {
		t.Fatalf("removing the first: %v", err)
	}
	ForgetAccounts()
	ls := Logins("copilot")
	if len(ls) != 2 || !ls[0].Active || ls[0].User == "wwjxhy" || ls[1].User == "wwjxhy" {
		t.Fatalf("after removing the first: %+v", ls)
	}

	saveLogins(t, saved...)
	ForgetAccounts()
	if err := forgetLogins("copilot"); err != nil {
		t.Fatalf("signing out every account: %v", err)
	}
	ForgetAccounts()
	if ls := Logins("copilot"); len(ls) != 0 {
		t.Fatalf("still listed: %+v", ls)
	}
}
