package access

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key may keep some accounts out instead of naming the ones it uses
// (LeiWang_2000 on X): "<provider>/!<account>" lets it use every other
// account of the provider, one signed in after the key was saved too,
// and never the one kept out.
func TestKeptOutAccounts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1", Name: "First"}, {Key: "sk-2"}}}); err != nil {
		t.Fatal(err)
	}
	k1, k2, k3 := provider.KeyID("sk-1"), provider.KeyID("sk-2"), provider.KeyID("sk-3")

	// kept by its id, the mark kept, a name resolving to it too
	for _, c := range []struct {
		name string
		in   []string
		want []string
		bad  string
	}{
		{"kept out by its fingerprint", []string{"relay/!" + k2}, []string{"relay/!" + k2}, ""},
		{"kept out by the name it is shown by", []string{"relay/!First"}, []string{"relay/!" + k1}, ""},
		{"once, told apart in lower case", []string{"relay/!" + k2, " RELAY/!" + strings.ToUpper(k2) + " "}, []string{"relay/!" + k2}, ""},
		{"used and kept out is refused", []string{"relay/" + k1, "relay/!First"}, nil, "either used or kept out"},
		{"a mark with no account", []string{"relay/!"}, nil, "<provider>/<account>"},
		{"one the provider has not", []string{"relay/!" + k3}, nil, "has no key"},
	} {
		out, err := CleanAccounts(c.in)
		if c.bad != "" {
			if err == nil || !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%s: said %v, want %q in it", c.name, err, c.bad)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(out, c.want) {
			t.Errorf("%s: kept %v, want %v", c.name, out, c.want)
		}
	}

	who := Identity{Accounts: []string{"relay/!" + strings.ToUpper(k2)}}
	if !who.AllowsAccount("relay", k1) || who.AllowsAccount("relay", k2) {
		t.Fatal("kept out: the other used, it never")
	}
	// a key signed in after the key was saved is one it may use
	if !who.AllowsAccount("relay", k3) {
		t.Fatal("an account added later was held out")
	}
	if !who.AllowsAccount("other", k2) {
		t.Fatal("another provider's was held")
	}
	// a provider with no account left: the list names none it uses, so
	// it serves as it always did
	if !who.AllowsAccount("relay", "") {
		t.Fatal("no account named to use, yet the provider was held")
	}
	// used and kept out of different providers
	mixed := Identity{Accounts: []string{"relay/" + k1, "codex/!acct-2"}}
	if !mixed.AllowsAccount("relay", k1) || mixed.AllowsAccount("relay", k3) {
		t.Fatal("relay is held to k1")
	}
	if !mixed.AllowsAccount("codex", "acct-1") || !mixed.AllowsAccount("codex", "acct-9") || mixed.AllowsAccount("codex", "acct-2") {
		t.Fatal("codex: every account but acct-2")
	}

	// kept out holds the key to no model
	if ms := HeldModels(nil, []string{"relay/!" + k2}); ms != nil {
		t.Fatalf("kept out only: models %v, want every one", ms)
	}
	if ms := HeldModels(nil, []string{"relay/" + k1, "codex/!acct-2"}); !slices.Equal(ms, []string{"relay/*"}) {
		t.Fatalf("models %v", ms)
	}

	// shown
	names := map[string]string{"relay/" + k1: "relay/First", "relay/" + k2: "relay/sk-…2"}
	if got := AccountsShown(names, []string{"relay/!" + k2}); got != "every account but relay/sk-…2" {
		t.Fatalf("shown %q", got)
	}
	if got := AccountsShown(names, []string{"relay/" + k1, "codex/!acct-2"}); got != "relay/First; every account but codex/acct-2" {
		t.Fatalf("shown %q", got)
	}
}

// An issued key folds in its issuer's kept-out accounts, and may not
// name one of them.
func TestKeptOutFolded(t *testing.T) {
	for _, c := range []struct {
		child, issuer, want []string
	}{
		{nil, []string{"codex/!a2"}, nil},
		{[]string{"codex/a1"}, []string{"codex/!a2"}, []string{"codex/a1", "codex/!a2"}},
		{[]string{"codex/a2", "codex/a1"}, []string{"codex/!a2"}, []string{"codex/a1", "codex/!a2"}},
		{[]string{"codex/!a3"}, []string{"codex/!a2"}, []string{"codex/!a3", "codex/!a2"}},
		{[]string{"codex/!a3"}, []string{"codex/a1"}, []string{"codex/a1", "codex/!a3"}},
	} {
		got := foldAccounts(c.child, c.issuer)
		if c.want == nil {
			c.want = c.issuer
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("foldAccounts(%v, %v) = %v, want %v", c.child, c.issuer, got, c.want)
		}
		who := Identity{Accounts: got}
		if who.AllowsAccount("codex", "a2") {
			if slices.Contains(c.issuer, "codex/!a2") {
				t.Errorf("%v: the issuer's kept-out account was let in", got)
			}
		}
	}
	issuer := Identity{Accounts: []string{"codex/!a2"}}
	if issuer.allowsEntry("codex/a2") || !issuer.allowsEntry("codex/a9") || !issuer.allowsEntry("codex/!a3") {
		t.Fatal("allowsEntry")
	}
}

// A key value replaced in place (#1869) moves the keys keeping it out too.
func TestKeptOutReplaced(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1"}, {Key: "sk-2"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("add-key", Change{Name: "Desk"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[len(keys)-1].ID
	if _, err := Update("accounts-key", Change{Key: id, Accounts: []string{"relay/!" + provider.KeyID("sk-2")}}); err != nil {
		t.Fatal(err)
	}
	n, err := ReplaceAccount("relay", provider.KeyID("sk-2"), provider.KeyID("sk-2b"))
	if err != nil || n != 1 {
		t.Fatalf("moved %d, %v", n, err)
	}
	keys, _ = List()
	if got := keys[len(keys)-1].Accounts; !slices.Equal(got, []string{"relay/!" + provider.KeyID("sk-2b")}) {
		t.Fatalf("accounts %v", got)
	}
}
