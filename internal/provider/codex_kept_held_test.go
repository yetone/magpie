package provider

import (
	"testing"
	"time"
)

// davezfr on X, after v0.1.4216: account 1, used up and out of credits
// (held), picked as Codex's sign-in showed "magpie" in the Codex app;
// account 2, with room, showed itself. The agents' sync reads both
// CodexUsedUpKnown (held) and CodexSignInKept (the account is the user's
// pick), and leaves Codex beside the kept account's sign-in.
func TestCodexSignInKept(t *testing.T) {
	home := signIn(t) // me@example.com: account 1
	rememberLogins(true)
	codexSignIn(t, home, "two@example.com", "r-two") // account 2, signed in now
	rememberLogins(true)
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	active := func() string {
		for _, l := range Logins("codex") {
			if l.Active {
				return l.User
			}
		}
		return ""
	}
	if CodexSignInKept() {
		t.Fatal("kept with nothing kept")
	}

	// account 1's reading, as the reporter's: not allowed, no credits, its
	// week used up till Thursday
	reset := time.Now().Add(3 * 24 * time.Hour)
	keepCodexReading(t, SubscriptionQuota{Provider: "codex", Held: true, Windows: []QuotaWindow{
		{Name: "7 days", Used: 100, ResetsAt: &reset},
	}}, time.Now())

	// picked in the sign-in (Keep signed in to it)
	if err := SetKeepLoginAs("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if a := active(); a != "me@example.com" {
		t.Fatalf("kept on me, signed in to %s", a)
	}
	if !CodexUsedUpKnown() {
		t.Fatal("account 1 not read as held")
	}
	if !CodexSignInKept() {
		t.Fatal("Codex on the account the user keeps it on: CodexSignInKept false")
	}

	// kept on account 2 while Codex is still on 1: 1 isn't the user's pick
	p, _ := Find("codex")
	p.KeepLoginAs = "two@example.com"
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if CodexSignInKept() {
		t.Fatal("kept on account 2, on account 1: CodexSignInKept true")
	}

	// kept on the first with none named: where it is is the user's
	p.KeepLoginAs = ""
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if !CodexSignInKept() {
		t.Fatal("kept where it is: CodexSignInKept false")
	}

	// only switching the sign-in, nothing is kept; nor with keeping off
	p.SwitchOnly = true
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if CodexSignInKept() {
		t.Fatal("switch-only: CodexSignInKept true")
	}
	p.SwitchOnly, p.KeepLogin = false, false
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if CodexSignInKept() {
		t.Fatal("not kept: CodexSignInKept true")
	}
}
