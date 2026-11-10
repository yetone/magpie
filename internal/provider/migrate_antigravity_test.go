package provider

import (
	"encoding/json"
	"testing"
)

// Antigravity's accounts go to its plugin as "<refresh token>|<project>"
// with the access token, its expiry and the email; one with no project yet
// goes with none for the plugin to ask Code Assist. Back, a renewed sign-in
// is written into the account it came from, its project kept when the
// plugin's has none, and one signed in since is added.
func TestAntigravityMover(t *testing.T) {
	claudeHome(t)
	auth := func(a googleAuth) json.RawMessage {
		b, _ := json.Marshal(a)
		return b
	}
	saveLogins(t,
		savedLogin{Agent: "antigravity", User: "a@x.com", Auth: auth(googleAuth{RefreshToken: "ra", AccessToken: "aa", Expiry: 1900000000000, Project: "proj-a"}), On: true, First: true, Plan: "Google AI Pro"},
		savedLogin{Agent: "antigravity", User: "b@x.com", Auth: auth(googleAuth{RefreshToken: "rb"}), Lapsed: "refused", Refused: revokedPrefix + "rb"},
		// Gemini CLI's accounts are no Antigravity's
		savedLogin{Agent: "gemini", User: "g@x.com", Auth: auth(googleAuth{RefreshToken: "rg"}), On: true})
	ForgetAccounts()

	ms := movingOf(t, "antigravity")
	if len(ms) != 2 {
		t.Fatalf("moving %+v", ms)
	}
	a := ms["a@x.com"]
	if !a.First || !a.On || a.Lapsed || a.Plan != "Google AI Pro" || a.Auth["type"] != "oauth" || a.Auth["refresh"] != "ra|proj-a" ||
		a.Auth["access"] != "aa" || a.Auth["expires"] != int64(1900000000000) || a.Auth["accountId"] != "a@x.com" {
		t.Fatalf("a: %+v", a)
	}
	if b := ms["b@x.com"]; b.First || !b.Lapsed || b.Auth["refresh"] != "rb|" || b.Auth["accountId"] != "b@x.com" {
		t.Fatalf("b: %+v", b)
	}

	// a renewed, with the project the plugin found for it
	ls, user, err := movers["antigravity"].back(readLogins(), "a@x.com", map[string]any{"type": "oauth", "refresh": "ra2|proj-a2", "access": "aa2", "expires": float64(1950000000000), "accountId": "a@x.com"})
	if err != nil || user != "a@x.com" {
		t.Fatalf("back a: %q %v", user, err)
	}
	var got googleAuth
	l := loginOf(t, ls, "antigravity", "a@x.com")
	if json.Unmarshal(l.Auth, &got) != nil || got.RefreshToken != "ra2" || got.AccessToken != "aa2" || got.Expiry != 1950000000000 || got.Project != "proj-a2" || l.Project != "proj-a2" || l.Plan != "Google AI Pro" {
		t.Fatalf("a back: %+v %s", l, l.Auth)
	}
	// b back with no project: the one it had stays, and it is lapsed no more
	ls, _, err = movers["antigravity"].back(ls, "b@x.com", map[string]any{"type": "oauth", "refresh": "rb2|", "access": "ab2", "accountId": "b@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if l := loginOf(t, ls, "antigravity", "b@x.com"); l.Lapsed != "" || l.Refused != "" || func() error { got = googleAuth{}; return json.Unmarshal(l.Auth, &got) }() != nil || got.RefreshToken != "rb2" || got.Project != "" {
		t.Fatalf("b back: %+v %s", l, l.Auth)
	}
	// one signed in through the plugin since, named by its accountId
	n := len(ls)
	ls, user, err = movers["antigravity"].back(ls, "", map[string]any{"type": "oauth", "refresh": "rc|proj-c", "access": "ac", "accountId": "c@x.com"})
	if err != nil || user != "c@x.com" || len(ls) != n+1 {
		t.Fatalf("back c: %q %v %d", user, err, len(ls))
	}
	if l := loginOf(t, ls, "antigravity", "c@x.com"); l.Project != "proj-c" {
		t.Fatalf("c back: %+v", l)
	}
	for _, bad := range []map[string]any{{"type": "oauth", "refresh": "|p", "accountId": "d@x.com"}, {"type": "oauth", "refresh": "rd"}} {
		if _, _, err := movers["antigravity"].back(ls, "", bad); err == nil {
			t.Fatalf("back took %v", bad)
		}
	}
}
