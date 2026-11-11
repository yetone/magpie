package mcpauth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

func accountLabels(name string) []string {
	var out []string
	for _, a := range mcpauth.Accounts(name) {
		l := a.Label
		if a.Active {
			l += "*"
		}
		out = append(out, l)
	}
	return out
}

// A second sign-in to a server joins the first and is the one in use; the
// first is kept, and switching back gives its token again, with every
// reader (Get, Token) seeing only the account in use.
func TestAccountsSwitchInPlace(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	first, _ := mcpauth.Get("neon")
	if st := mcpauth.StatusOf("neon", f.URL); len(st.Accounts) != 0 {
		t.Fatalf("one sign-in lists accounts: %+v", st.Accounts)
	}
	mcpauthtest.SignInAccount(t, "neon", f.URL, "work")
	mcpauthtest.SignInAccount(t, "neon", f.URL, "side")
	if got := accountLabels("neon"); !slices.Equal(got, []string{"", "work", "side*"}) {
		t.Fatalf("accounts = %q", got)
	}
	cur, _ := mcpauth.Get("neon")
	if cur.Access == first.Access {
		t.Fatal("the new sign-in is not in use")
	}
	if err := mcpauth.Use("neon", "1"); err != nil {
		t.Fatal(err)
	}
	// the list keeps its places; only the one in use moves
	if got := accountLabels("neon"); !slices.Equal(got, []string{"*", "work", "side"}) {
		t.Fatalf("after Use: %q", got)
	}
	tok, err := mcpauth.Token(context.Background(), "neon")
	if err != nil || tok != first.Access {
		t.Fatalf("Token = %q, %v; want the first account's %q", tok, err, first.Access)
	}
	if err := mcpauth.Use("neon", "nope"); err != mcpauth.ErrNoAccount {
		t.Errorf("Use of no account: %v", err)
	}
}

// Signing in again (a sign-in that ran out) replaces the account in use and
// leaves the others and its place in the list.
func TestSignInAgainKeepsTheOtherAccounts(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	mcpauthtest.SignInAccount(t, "neon", f.URL, "work")
	before, _ := mcpauth.Get("neon")
	f.SignIn(t, "neon")
	after, _ := mcpauth.Get("neon")
	if after.Access == before.Access {
		t.Fatal("not signed in again")
	}
	if got := accountLabels("neon"); !slices.Equal(got, []string{"", "work*"}) {
		t.Fatalf("accounts = %q", got)
	}
}

func TestRemoveAccount(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	mcpauthtest.SignInAccount(t, "neon", f.URL, "work")
	mcpauthtest.SignInAccount(t, "neon", f.URL, "side")
	// the one in use goes: the first of the others is put in use
	if err := mcpauth.RemoveAccount("neon", "3"); err != nil {
		t.Fatal(err)
	}
	if got := accountLabels("neon"); !slices.Equal(got, []string{"*", "work"}) {
		t.Fatalf("accounts = %q", got)
	}
	// another goes: the one in use stays
	if err := mcpauth.RemoveAccount("neon", "2"); err != nil {
		t.Fatal(err)
	}
	if got := accountLabels("neon"); !slices.Equal(got, []string{"*"}) {
		t.Fatalf("accounts = %q", got)
	}
	if err := mcpauth.RemoveAccount("neon", "1"); err != nil || mcpauth.SignedIn("neon", f.URL) {
		t.Fatalf("the last one: %v, signed in %v", err, mcpauth.SignedIn("neon", f.URL))
	}
	if err := mcpauth.RemoveAccount("neon", "1"); err != mcpauth.ErrNoAccount {
		t.Errorf("no account: %v", err)
	}
}

// A name an account has is not given to another, and the sign-ins go with a
// renamed server and out with a signed-out one.
func TestAccountsFollowTheServer(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	mcpauthtest.SignInAccount(t, "neon", f.URL, "work")
	if _, err := mcpauth.StartAccount(context.Background(), "neon", f.URL, nil, " WORK "); err != mcpauth.ErrLabelUsed {
		t.Fatalf("a used label: %v", err)
	}
	mcpauthtest.SignInAccount(t, "neon", f.URL, "side")
	if err := mcpauth.Rename("neon", "lime"); err != nil {
		t.Fatal(err)
	}
	if got := accountLabels("lime"); !slices.Equal(got, []string{"work", "side*"}) {
		t.Fatalf("renamed: %q", got)
	}
	if err := mcpauth.SignOut("lime"); err != nil || len(mcpauth.Accounts("lime")) != 0 {
		t.Fatalf("sign-out leaves %v (%v)", mcpauth.Accounts("lime"), err)
	}
}
