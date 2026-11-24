package access

import (
	"slices"
	"strings"
	"testing"
)

// providerKeysFixture keeps keys of every kind a provider's editor meets.
func providerKeysFixture(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveGroups([]AccountGroup{{ID: "g1", Name: "team", Accounts: []string{"kimi/a1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := save([]Key{
		{ID: "all", Name: "all", Secret: Prefix + "all-secret-0000000000"},
		{ID: "kept-out", Name: "kept-out", Secret: Prefix + "kept-out-000000000", Accounts: []string{"openai/!x"}},
		{ID: "whole", Name: "whole", Secret: Prefix + "whole-secret-00000000", Models: []string{"openai/*", "anthropic/*"}},
		{ID: "some", Name: "some", Secret: Prefix + "some-secret-000000000", Models: []string{"OpenAI/gpt-5", "anthropic/*"}},
		{ID: "only", Name: "only", Secret: Prefix + "only-secret-000000000", Models: []string{"openai/*", "openai/gpt-5"}},
		{ID: "other", Name: "other", Secret: Prefix + "other-secret-00000000", Models: []string{"anthropic/*"}, Accounts: []string{"anthropic/acct"}},
		{ID: "via", Name: "via", Secret: Prefix + "via-secret-0000000000", Models: []string{"re:^gpt-", "anthropic/*"}},
		{ID: "accts", Name: "accts", Secret: Prefix + "accts-secret-00000000", Accounts: []string{"anthropic/acct", "@g1"}},
		{ID: "ro", Name: "ro", Secret: Prefix + "ro-secret-00000000000", ReadOnly: true},
		{ID: "issued", Name: "issued", Secret: Prefix + "issued-secret-0000000", Issuer: "whole", Models: []string{"anthropic/*"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func reachMap(t *testing.T, pid string) map[string]ProviderKey {
	t.Helper()
	ks, err := ProviderKeys(pid)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ProviderKey{}
	for _, k := range ks {
		out[k.ID] = k
	}
	return out
}

func keyByID(t *testing.T, id string) Key {
	t.Helper()
	keys, err := load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == id })
	if i < 0 {
		t.Fatal("no key", id)
	}
	return keys[i]
}

// A provider's editor tells each key's reach from the key's own models,
// the list the Gateway page's picker writes: every model, every one of
// the provider's, some, or none (justforlxz on X).
func TestProviderKeysReach(t *testing.T) {
	providerKeysFixture(t)
	r := reachMap(t, "openai")
	for id, want := range map[string]string{"all": ReachAll, "kept-out": ReachAll, "whole": ReachWhole, "some": ReachSome, "only": ReachWhole, "other": ReachNone, "via": ReachNone, "accts": ReachNone} {
		if r[id].Reach != want {
			t.Errorf("%s: reach %q, want %q", id, r[id].Reach, want)
		}
	}
	if _, ok := r["ro"]; ok {
		t.Error("a read-only key uses no model, so it isn't listed")
	}
	if _, ok := r["issued"]; ok {
		t.Error("an issued key follows its issuer, so it isn't listed")
	}
	if !r["only"].Only || r["whole"].Only || r["some"].Only {
		t.Errorf("only: %+v %+v %+v", r["only"], r["whole"], r["some"])
	}
	if !slices.Equal(r["via"].Via, []string{"re:^gpt-"}) {
		t.Errorf("via: %v", r["via"].Via)
	}
	// a key held to accounts alone reaches its accounts' providers, a
	// group's among them
	if k := reachMap(t, "kimi")["accts"]; k.Reach != ReachWhole || k.Only {
		t.Errorf("accounts' provider: %+v", k)
	}
}

// Ticking adds "<provider>/*" and keeps what the key had; unticking takes
// the provider's own entries away and nothing else; the gateway then
// holds the key the same way.
func TestSetProviderKeys(t *testing.T) {
	providerKeysFixture(t)
	if err := SetProviderKeys("openai", []string{"other", "accts", "via"}, []string{"some", "whole"}, provs); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][]string{
		"other": {"anthropic/*", "openai/*"},
		"via":   {"re:^gpt-", "anthropic/*", "openai/*"},
		// held to accounts alone: its providers kept, openai beside them
		"accts": {"anthropic/*", "kimi/*", "openai/*"},
		"some":  {"anthropic/*"},
		"whole": {"anthropic/*"},
	} {
		if got := keyByID(t, id).Models; !slices.Equal(got, want) {
			t.Errorf("%s: models %v, want %v", id, got, want)
		}
	}
	if got := keyByID(t, "other").Accounts; !slices.Equal(got, []string{"anthropic/acct"}) {
		t.Errorf("the key's accounts changed: %v", got)
	}
	if got := keyByID(t, "accts").Accounts; !slices.Equal(got, []string{"anthropic/acct", "@g1"}) {
		t.Errorf("the key's accounts changed: %v", got)
	}
	who, ok := Authenticate(Prefix + "whole-secret-00000000")
	if !ok || who.Allows("openai/gpt-5") || !who.Allows("anthropic/claude") {
		t.Errorf("unticked key still reaches openai: %+v", who.Models)
	}
	if who, ok := Authenticate(Prefix + "other-secret-00000000"); !ok || !who.Allows("openai/gpt-5") {
		t.Errorf("ticked key doesn't reach openai: %+v", who.Models)
	}
	if who, ok := Authenticate(Prefix + "accts-secret-00000000"); !ok || !who.Allows("openai/gpt-5") || !who.Allows("kimi/k2") || !who.Allows("anthropic/claude") {
		t.Errorf("accounts key lost a provider: %+v", who.Models)
	}
	// a key that reaches some of the provider's models stays at some
	if err := SetProviderKeys("anthropic", []string{"some"}, nil, provs); err != nil {
		t.Fatal(err)
	}
	if got := keyByID(t, "some").Models; !slices.Equal(got, []string{"anthropic/*"}) {
		t.Errorf("ticked again: %v", got)
	}
}

// A key held to this provider alone is never let off to every model, and
// a key on every model with no other provider to keep it to is refused:
// nothing of the others changes.
func TestSetProviderKeysRefuses(t *testing.T) {
	providerKeysFixture(t)
	for id, ps := range map[string][]string{"only": provs, "all": {"openai"}, "kept-out": nil} {
		err := SetProviderKeys("openai", []string{"other"}, []string{id}, ps)
		if err == nil || !strings.Contains(err.Error(), "Gateway page") {
			t.Errorf("%s: %v", id, err)
		}
		if got := keyByID(t, "other").Models; !slices.Equal(got, []string{"anthropic/*"}) {
			t.Errorf("%s: another key changed though the change was refused: %v", id, got)
		}
	}
	if k := keyByID(t, "all"); len(k.Models) != 0 || len(k.Accounts) != 0 {
		t.Errorf("every-model key changed: %+v", k)
	}
	if err := SetProviderKeys("openai", nil, []string{"ro"}, provs); err == nil {
		t.Error("a read-only key was taken")
	}
	if err := SetProviderKeys("openai", []string{"nope"}, nil, provs); err == nil {
		t.Error("an unknown key was taken")
	}
	for _, pid := range []string{"", "a/b", "*"} {
		if err := SetProviderKeys(pid, []string{"other"}, nil, provs); err == nil {
			t.Errorf("provider %q taken", pid)
		}
	}
}

// A key on every model, as a new key is, can be unticked (justforlxz on
// X: every such key was locked, so a click did nothing). It is held to
// every other provider there is and every routing group, its accounts
// kept, and the gateway then keeps it off this provider alone.
func TestSetProviderKeysNarrowsAnEveryModelKey(t *testing.T) {
	providerKeysFixture(t)
	if err := SetProviderKeys("openai", nil, []string{"all", "kept-out"}, provs); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"all", "kept-out"} {
		if got, want := keyByID(t, id).Models, []string{"anthropic/*", "kimi/*", "group/*"}; !slices.Equal(got, want) {
			t.Errorf("%s: models %v, want %v", id, got, want)
		}
	}
	if got := keyByID(t, "kept-out").Accounts; !slices.Equal(got, []string{"openai/!x"}) {
		t.Errorf("the key's accounts changed: %v", got)
	}
	who, ok := Authenticate(Prefix + "all-secret-0000000000")
	if !ok || who.Allows("openai/gpt-5") || !who.Allows("anthropic/claude") || !who.Allows("kimi/k2") || !who.Allows("group/team") {
		t.Errorf("unticked every-model key: %+v", who.Models)
	}
	if k := reachMap(t, "openai")["all"]; k.Reach != ReachNone {
		t.Errorf("unticked key still reads as reaching openai: %+v", k)
	}
	// ticked again, it reaches openai whole
	if err := SetProviderKeys("openai", []string{"all"}, nil, provs); err != nil {
		t.Fatal(err)
	}
	if who, ok := Authenticate(Prefix + "all-secret-0000000000"); !ok || !who.Allows("openai/gpt-5") {
		t.Errorf("ticked again: %+v", who.Models)
	}
}

// provs are the providers the tests' keys name
var provs = []string{"openai", "anthropic", "kimi", "OpenAI"}
