package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// An old kiro-cli sign-in, with no kiro-cli to sign it out, is removed in
// magpie: it is hidden, its files untouched, the gateway no longer tries
// it, and it shows again once kiro-cli signs in anew.
func TestKiroHideOwnSignIn(t *testing.T) {
	start, browser := kiroFakeSignIn(t)
	old := map[string]any{"kirocli:social:token": map[string]any{"access_token": "old-at", "refresh_token": "old-rt", "expires_at": "2020-01-01T00:00:00Z"}}
	writeKiroCLI(t, old)
	if ls := Logins("kiro"); len(ls) != 1 || ls[0].User != "Kiro account" || !ls[0].Own {
		t.Fatalf("own %+v", ls)
	}
	if st := kiroSignIn(t, start, browser, "gcode"); st.State != "done" || st.User != "me@example.com" {
		t.Fatalf("sign-in %+v", st)
	}
	if err := SwitchLogin("kiro", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	var home string
	for _, l := range kiroLogins() {
		if l.User == "me@example.com" {
			home = l.Home
		}
	}
	db := kiroCLIDB()
	before, _ := os.ReadFile(db)
	st, _ := os.Stat(db)

	if err := ForgetLogin("kiro", "Kiro account"); err != nil {
		t.Fatal(err)
	}
	for range 2 { // and read again, it stays hidden
		if ls := Logins("kiro"); len(ls) != 1 || ls[0].User != "me@example.com" || !ls[0].Active {
			t.Fatalf("after hide %+v", ls)
		}
	}
	p, ok := kiroAccount()
	if !ok || p.Account.User != "me@example.com" || p.Account.Home != home {
		t.Fatalf("first %+v", p.Account)
	}
	if also := p.AlsoOn(); len(also) != 0 {
		t.Fatalf("still tried %+v", also)
	}
	after, _ := os.ReadFile(db)
	st2, _ := os.Stat(db)
	if !bytes.Equal(before, after) || !st.ModTime().Equal(st2.ModTime()) {
		t.Fatal("kiro-cli's database was touched")
	}
	if row := readKiroRow(t, "kirocli:social:token"); row["refresh_token"] != "old-rt" {
		t.Fatalf("kiro-cli's sign-in changed: %v", row)
	}
	if b, _ := os.ReadFile(Path()); bytes.Contains(b, []byte("old-rt")) {
		t.Fatal("the refresh token was saved")
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(Path()), "logins.json")); bytes.Contains(b, []byte("old-rt")) {
		t.Fatal("the refresh token was saved in logins.json")
	}

	// kiro-cli signed in anew: it shows again, behind the one put first
	writeKiroCLI(t, map[string]any{"kirocli:social:token": map[string]any{"access_token": "new-at", "refresh_token": "new-rt", "expires_at": "2099-01-01T00:00:00Z"}})
	ls := Logins("kiro")
	if len(ls) != 2 || ls[0].User != "me@example.com" || !ls[0].Active || ls[1].User != "Kiro account" || !ls[1].Own {
		t.Fatalf("after a new sign-in %+v", ls)
	}

	// hidden while first, the next is put first; hidden alone, none is left
	if err := SwitchLogin("kiro", "Kiro account"); err != nil {
		t.Fatal(err)
	}
	if p, _ := kiroAccount(); p.Account.User != "Kiro account" {
		t.Fatalf("own first %+v", p.Account)
	}
	if err := ForgetLogin("kiro", "Kiro account"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("kiro"); len(ls) != 1 || ls[0].User != "me@example.com" || !ls[0].Active {
		t.Fatalf("next first %+v", ls)
	}

	// one of magpie's, removed, goes with its home as before
	if st := kiroSignIn(t, start, browser, "gcode2"); st.State != "done" || st.User != "two@example.com" {
		t.Fatalf("second %+v", st)
	}
	var two string
	for _, l := range kiroLogins() {
		if l.User == "two@example.com" {
			two = l.Home
		}
	}
	if err := ForgetLogin("kiro", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(two); !os.IsNotExist(err) || len(Logins("kiro")) != 1 {
		t.Fatalf("home %s left: %v %+v", two, err, Logins("kiro"))
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the other home went: %v", err)
	}
}

// The only account, kiro-cli's own, removed: Kiro has none left.
func TestKiroHideOnlySignIn(t *testing.T) {
	kiroSandbox(t)
	writeKiroCLI(t, map[string]any{"kirocli:social:token": map[string]any{"access_token": "old-at", "refresh_token": "old-rt", "expires_at": "2020-01-01T00:00:00Z"}})
	if _, ok := kiroAccount(); !ok {
		t.Fatal("no account")
	}
	if err := ForgetLogin("kiro", "Kiro account"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("kiro"); len(ls) != 0 {
		t.Fatalf("left %+v", ls)
	}
	if _, ok := kiroAccount(); ok {
		t.Fatal("still tried")
	}
	if row := readKiroRow(t, "kirocli:social:token"); row["refresh_token"] != "old-rt" {
		t.Fatalf("kiro-cli's sign-in changed: %v", row)
	}
}

// An agent with no mark to tell a fresh sign-in by (Grok's CLI) hides its
// own the same way, and shows it again once it is another account.
func TestGrokHideOwnSignIn(t *testing.T) {
	home := signIn(t)
	own := filepath.Join(home, ".grok")
	grokSignedIn(t, own, "me@x.ai")
	extra, _ := newGrokHome()
	grokSignedIn(t, extra, "two@x.ai")
	if _, err := addGrokLogin(extra); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(own, "auth.json"))
	if err := ForgetLogin("grok", "me@x.ai"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("grok"); len(ls) != 1 || ls[0].User != "two@x.ai" || !ls[0].Active {
		t.Fatalf("after hide %+v", ls)
	}
	if after, _ := os.ReadFile(filepath.Join(own, "auth.json")); !bytes.Equal(before, after) {
		t.Fatal("the CLI's own sign-in was touched")
	}
	grokSignedIn(t, own, "new@x.ai")
	if ls := Logins("grok"); len(ls) != 2 || ls[0].User != "two@x.ai" || ls[1].User != "new@x.ai" || !ls[1].Own {
		t.Fatalf("another account %+v", ls)
	}
}
