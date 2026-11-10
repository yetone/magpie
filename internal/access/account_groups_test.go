package access

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// LeiWang_2000 on X: an account group is a named set of accounts a key's
// list names, "@<id>" to use them or "!@<id>" to keep them out, read as
// it is on every request: a change to the group reaches every key naming
// it, one gone holds the key to nothing, and one in use can't be removed.
func TestAccountGroupHoldsAKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1", Name: "First"}, {Key: "sk-2"}, {Key: "sk-3"}}}); err != nil {
		t.Fatal(err)
	}
	k1, k2, k3 := provider.KeyID("sk-1"), provider.KeyID("sk-2"), provider.KeyID("sk-3")

	// a group keeps each account as a key's list does: by its id
	g, err := SaveAccountGroup(AccountGroup{Name: " Work ", Accounts: []string{"relay/First", "relay/" + k2, "RELAY/" + strings.ToUpper(k2)}})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || g.Name != "Work" || !slices.Equal(g.Accounts, []string{"relay/" + k1, "relay/" + k2}) {
		t.Fatalf("saved %+v", g)
	}
	for _, c := range []struct {
		name string
		in   AccountGroup
		bad  string
	}{
		{"a name taken", AccountGroup{Name: "work"}, "named Work already"},
		{"no name", AccountGroup{Name: " "}, "between 1 and 80"},
		{"a name like an entry", AccountGroup{Name: "@x"}, "can't start with"},
		{"a group in a group", AccountGroup{Name: "Nest", Accounts: []string{"@" + g.ID}}, "not another group"},
		{"an account kept out", AccountGroup{Name: "Out", Accounts: []string{"relay/!" + k1}}, "not another group"},
		{"an account no provider has", AccountGroup{Name: "Typo", Accounts: []string{"relay/nobody"}}, "has no key"},
		{"a group no one made", AccountGroup{ID: "nope", Name: "Nope"}, "not found"},
	} {
		if _, err := SaveAccountGroup(c.in); err == nil || !strings.Contains(err.Error(), c.bad) {
			t.Errorf("%s: said %v, want %q in it", c.name, err, c.bad)
		}
	}

	held, heldSecret := addKey(t, "Held")
	out, outSecret := addKey(t, "Out")
	// named by the group's name, kept by its id
	if _, err := Update("accounts-key", Change{Key: held, Accounts: []string{"@work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("accounts-key", Change{Key: out, Accounts: []string{"!@Work"}}); err != nil {
		t.Fatal(err)
	}
	keys, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == held }); !slices.Equal(keys[i].Accounts, []string{"@" + g.ID}) {
		t.Fatalf("kept %v", keys[i].Accounts)
	}
	if _, err := CleanAccounts([]string{"@Work", "!@" + g.ID}); err == nil || !strings.Contains(err.Error(), "either used or kept out") {
		t.Fatalf("a group used and kept out: %v", err)
	}
	if _, err := CleanAccounts([]string{"@Nope"}); err == nil || !strings.Contains(err.Error(), "no account group") {
		t.Fatalf("a group no one made: %v", err)
	}

	check := func(secret string) Identity {
		t.Helper()
		who, err := Check(secret)
		if err != nil {
			t.Fatal(err)
		}
		return who
	}
	allows := func(who Identity) (got []bool) {
		for _, k := range []string{k1, k2, k3} {
			got = append(got, who.AllowsAccount("relay", k))
		}
		return got
	}
	who := check(heldSecret)
	if got := allows(who); !slices.Equal(got, []bool{true, true, false}) {
		t.Fatalf("held to the group: %v", got)
	}
	if !slices.Equal(who.Models, []string{"relay/*"}) || !who.AccountModels {
		t.Fatalf("held to the group's providers' models: %v %v", who.Models, who.AccountModels)
	}
	if got := allows(check(outSecret)); !slices.Equal(got, []bool{false, false, true}) {
		t.Fatalf("keeping the group out: %v", got)
	}
	if ms := check(outSecret).Models; ms != nil {
		t.Fatalf("keeping a group out held the models: %v", ms)
	}

	// a change to the group reaches both keys at once
	g.Accounts = []string{"relay/" + k3}
	if _, err := SaveAccountGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := allows(check(heldSecret)); !slices.Equal(got, []bool{false, false, true}) {
		t.Fatalf("held, after the change: %v", got)
	}
	if got := allows(check(outSecret)); !slices.Equal(got, []bool{true, true, false}) {
		t.Fatalf("kept out, after the change: %v", got)
	}

	// a provider key rotated in place moves in the group too
	if n, err := ReplaceAccount("relay", k3, provider.KeyID("sk-3b")); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	if gs, _ := AccountGroups(); !slices.Equal(gs[0].Accounts, []string{"relay/" + provider.KeyID("sk-3b")}) {
		t.Fatalf("rotated: %v", gs[0].Accounts)
	}

	// a group a key names stays
	if err := RemoveAccountGroup(g.ID); err == nil || !strings.Contains(err.Error(), "Held") || !strings.Contains(err.Error(), "Out") {
		t.Fatalf("removed a group in use: %v", err)
	}

	// a group with no account, held to, holds the key to nothing; kept
	// out, it keeps nothing out
	g.Accounts = nil
	if _, err := SaveAccountGroup(g); err != nil {
		t.Fatal(err)
	}
	who = check(heldSecret)
	if got := allows(who); slices.Contains(got, true) || who.AllowsAccount("other", "x") {
		t.Fatalf("held to an empty group: %v", got)
	}
	if len(who.Models) == 0 || covered(who.Models, "relay/m") || covered(who.Models, "other/m") {
		t.Fatalf("held to an empty group, models %v", who.Models)
	}
	if got := allows(check(outSecret)); !slices.Equal(got, []bool{true, true, true}) {
		t.Fatalf("keeping an empty group out: %v", got)
	}

	// a group gone (a store written over, a sync) holds a key naming it
	// to nothing, held to or kept out
	if err := RestoreGroups(nil); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{heldSecret, outSecret} {
		who := check(s)
		if slices.Contains(allows(who), true) || who.AllowsAccount("other", "x") {
			t.Fatalf("a group gone let the key on: %v", who.Accounts)
		}
	}

	// groups that can't be read refuse the key naming one, and leave
	// every other key as it was
	if err := os.WriteFile(GroupsPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(heldSecret); err == nil {
		t.Fatal("unreadable groups let a key naming one through")
	}
	_, free := addKey(t, "Free")
	if who := check(free); !who.AllowsAccount("relay", k1) {
		t.Fatal("unreadable groups held a key naming none")
	}
}

// A key issued under one naming a group may use only what the group
// holds now, and one with no accounts of its own takes the issuer's.
func TestAccountGroupHoldsAnIssuedKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1"}, {Key: "sk-2"}}}); err != nil {
		t.Fatal(err)
	}
	k1, k2 := provider.KeyID("sk-1"), provider.KeyID("sk-2")
	g, err := SaveAccountGroup(AccountGroup{Name: "Work", Accounts: []string{"relay/" + k1}})
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := addKey(t, "Orchestrator")
	if _, err := Update("accounts-key", Change{Key: pid, Accounts: []string{"@Work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("issuer-key", Change{Key: pid, Issues: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := IssueKey(Issue{TTL: time.Hour, Issuer: pid, Accounts: []string{"relay/" + k2}}); err == nil {
		t.Fatal("issued an account the issuer's group doesn't hold")
	}
	one, err := IssueKey(Issue{TTL: time.Hour, Issuer: pid, Accounts: []string{"relay/" + k1}})
	if err != nil {
		t.Fatal(err)
	}
	all, err := IssueKey(Issue{TTL: time.Hour, Issuer: pid})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{one.Key, all.Key} {
		who, err := Check(s)
		if err != nil {
			t.Fatal(err)
		}
		if !who.AllowsAccount("relay", k1) || who.AllowsAccount("relay", k2) {
			t.Fatalf("issued: %v", who.Accounts)
		}
	}
	// the issuer's group changed: the issued keys follow it
	g.Accounts = []string{"relay/" + k2}
	if _, err := SaveAccountGroup(g); err != nil {
		t.Fatal(err)
	}
	if who, err := Check(all.Key); err != nil || who.AllowsAccount("relay", k1) || !who.AllowsAccount("relay", k2) {
		t.Fatalf("issued with none of its own, after the change: %v %v", who.Accounts, err)
	}
	// one issued k1 is held to the issuer's accounts now, as when an
	// issuer's own list changes (foldAccounts)
	if who, err := Check(one.Key); err != nil || who.AllowsAccount("relay", k1) {
		t.Fatalf("issued k1, its issuer's group now k2 alone: %v %v", who.Accounts, err)
	}
}
