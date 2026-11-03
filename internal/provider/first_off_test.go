package provider

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Rime_Nan on X: the first account of a subscription magpie's gateway
// serves could be ticked off only after dragging it down the list ("put
// another account first"). First is only the gateway's order there, so
// ticked off it hands first to the next ticked account; with no other
// ticked, it stays, as one account is always used.
func TestFirstTickedOffHandsFirstOn(t *testing.T) {
	claudeHome(t)
	auth := func(r string) json.RawMessage {
		b, _ := json.Marshal(googleAuth{RefreshToken: r})
		return b
	}
	saveLogins(t,
		savedLogin{Agent: "antigravity", User: "a@x.com", Auth: auth("ra"), On: true, First: true},
		savedLogin{Agent: "antigravity", User: "b@x.com", Auth: auth("rb")},
		savedLogin{Agent: "antigravity", User: "c@x.com", Auth: auth("rc"), On: true})
	ForgetAccounts()

	// b is off: the next ticked one, c, takes first, and b stays off
	if err := SetLoginOn("antigravity", "a@x.com", false); err != nil {
		t.Fatalf("ticking the first off with another ticked: %v", err)
	}
	ForgetAccounts()
	ls := Logins("antigravity")
	if len(ls) != 3 || ls[0].User != "c@x.com" || !ls[0].Active || !ls[0].On {
		t.Fatalf("c is not first: %+v", ls)
	}
	for _, l := range ls[1:] {
		if l.On || l.Active {
			t.Fatalf("%s is still in use: %+v", l.User, ls)
		}
	}

	// c is the only one ticked now: it stays, said why
	if err := SetLoginOn("antigravity", "c@x.com", false); err == nil {
		t.Fatalf("the only account in use was ticked off: %+v", Logins("antigravity"))
	}
	ForgetAccounts()
	if ls := Logins("antigravity"); ls[0].User != "c@x.com" || !ls[0].Active || !ls[0].On {
		t.Fatalf("after a refused tick off: %+v", ls)
	}
}

// The editors' own Copilot account is first with no account marked so;
// ticked off, the next ticked one of magpie's is first, and the own one
// is listed off, not first again.
func TestOwnFirstCopilotTickedOff(t *testing.T) {
	signIn(t)
	writeFile(t, filepath.Join(copilotConfigDir(), "github-copilot", "apps.json"), map[string]any{"github.com": map[string]any{"user": "octo", "oauth_token": "gho_own"}})
	tok := func(s string) json.RawMessage { return json.RawMessage(`{"oauth_token":"` + s + `"}`) }
	saveLogins(t,
		savedLogin{Agent: "copilot", User: "octo", On: true},
		savedLogin{Agent: "copilot", User: "other", Auth: tok("gho_o"), On: true})
	ForgetAccounts()
	if ls := Logins("copilot"); len(ls) != 2 || ls[0].User != "octo" || !ls[0].Active {
		t.Fatalf("logins before: %+v", ls)
	}
	if err := SetLoginOn("copilot", "octo", false); err != nil {
		t.Fatalf("ticking the own first account off: %v", err)
	}
	ForgetAccounts()
	ls := Logins("copilot")
	if len(ls) != 2 || ls[0].User != "other" || !ls[0].Active {
		t.Fatalf("other is not first: %+v", ls)
	}
	if ls[1].User != "octo" || ls[1].On || ls[1].Active {
		t.Fatalf("octo is still in use: %+v", ls)
	}
}
