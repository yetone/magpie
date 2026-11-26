package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
)

// standInAgy has MoveToAgy's installs and import answer here: the plugin
// list holds agy-cli (taking accounts from a file when importable), and
// what was installed and imported is kept.
type agyStandIn struct {
	installed []string
	texts     []string
	// the proxy agy was installed and the accounts imported through, as
	// their ctx chose it
	cliVia, importVia string
	cliInstalled      bool
}

func standInAgy(t *testing.T, importable bool) *agyStandIn {
	t.Helper()
	s := &agyStandIn{}
	oldInstall, oldProviders, oldCLI, oldImport, oldMissing := installPlugin, pluginProviders, installAgy, importPlugin, agyCLIMissing
	installPlugin = func(_ context.Context, pkg, min string) error {
		s.installed = append(s.installed, pkg+"@"+min)
		return nil
	}
	pluginProviders = func(context.Context) ([]plugin.Provider, error) {
		return []plugin.Provider{{ID: agyCLIID, Spec: agyCLIPackage, Importable: importable}}, nil
	}
	agyCLIMissing = func(string) string { return "" }
	installAgy = func(ctx context.Context, _ string) error {
		s.cliInstalled, s.cliVia = true, netproxy.Choice(ctx)
		return nil
	}
	importPlugin = func(ctx context.Context, id string, texts []string) ([]ImportedAccount, error) {
		if id != agyCLIID {
			return nil, errors.New("imported into " + id)
		}
		s.importVia = netproxy.Choice(ctx)
		s.texts = append(s.texts, texts...)
		return []ImportedAccount{{User: "a@x.com", Status: "added"}}, nil
	}
	t.Cleanup(func() {
		installPlugin, pluginProviders, installAgy, importPlugin, agyCLIMissing = oldInstall, oldProviders, oldCLI, oldImport, oldMissing
	})
	return s
}

// The retired Antigravity plugin's sign-ins are offered to agy-cli, which
// signs in with the same Google client, in the plugin's order: its first
// account first. One agy-cli has already (its email, in any case) and a
// second copy of one sign-in are left out, as is another provider's
// (@tongzhenbao on X).
func TestAgyMoveUsersAreTheOldPluginsNotInAgyCLI(t *testing.T) {
	signIn(t)
	writePluginAuth(t, map[string]any{
		"antigravity#b":  map[string]any{"type": "oauth", "refresh": "1//b|proj-b", "access": "ya29.b", "expires": 1, "accountId": "b@x.com"},
		"antigravity":    map[string]any{"type": "oauth", "refresh": "1//a|proj-a", "access": "ya29.a", "expires": 1, "accountId": "a@x.com"},
		"antigravity#c":  map[string]any{"type": "oauth", "refresh": "1//c|proj-c", "accountId": "C@x.com"},
		"antigravity#a2": map[string]any{"type": "oauth", "refresh": "1//a|proj-a", "accountId": "a2@x.com"},
		"agy-cli":        map[string]any{"type": "success", "key": "agy:0123", "metadata": map[string]any{"email": "c@X.com", "agyHome": "0123"}},
		"gemini":         map[string]any{"type": "oauth", "refresh": "1//g", "accountId": "g@x.com"},
	})
	if got := AgyMoveUsers(); !slices.Equal(got, []string{"a@x.com", "b@x.com"}) {
		t.Fatalf("offered %v", got)
	}
}

// agy-cli has every account of the old plugin once each one's email is
// among its own (the owner's screenshot, 11-21: one Google account in
// both, still told to use agy-cli); one it lacks, or none signed in, is
// not every.
func TestAgyHasEveryOldAccount(t *testing.T) {
	signIn(t)
	if AgyHasEvery() {
		t.Fatal("nothing signed in has every account")
	}
	old := map[string]any{"type": "oauth", "refresh": "1//a|proj-a", "accountId": "A@x.com"}
	writePluginAuth(t, map[string]any{"antigravity": old})
	if AgyHasEvery() {
		t.Fatal("agy-cli signed in to nothing has every account")
	}
	cli := map[string]any{"type": "success", "key": "agy:0123", "metadata": map[string]any{"email": "a@x.com"}}
	writePluginAuth(t, map[string]any{"antigravity": old, "agy-cli": cli})
	if !AgyHasEvery() {
		t.Fatal("agy-cli with the one old account doesn't have every one")
	}
	writePluginAuth(t, map[string]any{"antigravity": old, "antigravity#b": map[string]any{"type": "oauth", "refresh": "1//b|p", "accountId": "b@x.com"}, "agy-cli": cli})
	if AgyHasEvery() {
		t.Fatal("agy-cli without b@x.com has every account")
	}
	writePluginAuth(t, map[string]any{"agy-cli": cli})
	if !AgyHasEvery() {
		t.Fatal("agy-cli signed in, no old account left: not every")
	}
}

// Move installs agy-cli at the version that takes the old plugin's
// sign-ins, and hands it those sign-ins as the plugin keeps them, every one
// at once; the old sign-ins stay where they are.
func TestMoveToAgyImportsTheOldSignIns(t *testing.T) {
	signIn(t)
	old := map[string]any{"type": "oauth", "refresh": "1//a|proj-a", "access": "ya29.a", "expires": 1700000000000.0, "accountId": "a@x.com"}
	writePluginAuth(t, map[string]any{"antigravity": old})
	s := standInAgy(t, true)
	got, err := MoveToAgy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != "added" {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(s.installed, []string{agyCLIPackage + "@" + agyCLIMin}) || agyCLIMin != "0.1.139" {
		t.Fatalf("installed %v", s.installed)
	}
	if len(s.texts) != 1 {
		t.Fatalf("texts %v", s.texts)
	}
	var sent []map[string]any
	if err := json.Unmarshal([]byte(s.texts[0]), &sent); err != nil || len(sent) != 1 {
		t.Fatalf("%v %s", err, s.texts[0])
	}
	for k, v := range old {
		if sent[0][k] != v {
			t.Fatalf("%s: sent %v, the plugin keeps %v", k, sent[0][k], v)
		}
	}
	if plugin.Auths("antigravity")["antigravity"] == nil {
		t.Fatal("the old sign-in was taken away")
	}
}

// With nothing to move, or an agy-cli that takes no accounts from a file
// (older than the version that does), nothing is imported.
func TestMoveToAgyNeedsAccountsAndAnImport(t *testing.T) {
	signIn(t)
	s := standInAgy(t, false)
	if _, err := MoveToAgy(context.Background()); err == nil || len(s.installed) != 0 {
		t.Fatalf("nothing to move: %v, installed %v", err, s.installed)
	}
	writePluginAuth(t, map[string]any{"antigravity": map[string]any{"type": "oauth", "refresh": "1//a|p", "accountId": "a@x.com"}})
	if _, err := MoveToAgy(context.Background()); err == nil || !strings.Contains(err.Error(), "takes no Antigravity accounts") || len(s.texts) != 0 {
		t.Fatalf("no import: %v, imported %v", err, s.texts)
	}
}

// Where Google is reached only through Antigravity's own proxy (magpie in
// a container, with that proxy set on Antigravity alone), agy is
// installed and the accounts checked through it, and agy-cli keeps it for
// its requests: going direct, the install failed every time (huiqiang6 on
// X). Each account with a proxy of its own keeps that one.
func TestMoveToAgyGoesThroughAntigravitysProxy(t *testing.T) {
	signIn(t)
	writePluginAuth(t, map[string]any{
		"antigravity":   map[string]any{"type": "oauth", "refresh": "1//a|p", "accountId": "a@x.com"},
		"antigravity#b": map[string]any{"type": "oauth", "refresh": "1//b|p", "accountId": "B@x.com"},
	})
	if err := store(file{Providers: []Provider{{ID: "antigravity", Proxy: "http://10.0.0.7:3128", AccountProxies: map[string]string{"b@x.com": "socks5://10.0.0.8:1080"}}}}); err != nil {
		t.Fatal(err)
	}
	s := standInAgy(t, true)
	agyCLIMissing = func(string) string { return "Antigravity CLI" }
	if _, err := MoveToAgy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.cliInstalled || s.cliVia != "http://10.0.0.7:3128" || s.importVia != "http://10.0.0.7:3128" {
		t.Fatalf("agy installed (%v) through %q, imported through %q", s.cliInstalled, s.cliVia, s.importVia)
	}
	if got := ProxyOfLogin(agyCLIID, "a@x.com"); got != "http://10.0.0.7:3128" {
		t.Fatalf("a@x.com goes through %q", got)
	}
	if got := ProxyOfLogin(agyCLIID, "b@x.com"); got != "socks5://10.0.0.8:1080" {
		t.Fatalf("b@x.com goes through %q", got)
	}
}

// A proxy the user chose in agy-cli stays its own; a moving account keeps
// Antigravity's as its own, as its requests went.
func TestMoveToAgyKeepsAgyCLIsOwnProxy(t *testing.T) {
	signIn(t)
	writePluginAuth(t, map[string]any{"antigravity": map[string]any{"type": "oauth", "refresh": "1//a|p", "accountId": "a@x.com"}})
	if err := store(file{Providers: []Provider{{ID: "antigravity", Proxy: "http://10.0.0.7:3128"}, {ID: agyCLIID, Proxy: "direct"}}}); err != nil {
		t.Fatal(err)
	}
	s := standInAgy(t, true)
	agyCLIMissing = func(string) string { return "Antigravity CLI" }
	if _, err := MoveToAgy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ProxyOf(agyCLIID) != "direct" || s.cliVia != "direct" {
		t.Fatalf("agy-cli's proxy %q, installed through %q", ProxyOf(agyCLIID), s.cliVia)
	}
	if got := ProxyOfLogin(agyCLIID, "a@x.com"); got != "http://10.0.0.7:3128" {
		t.Fatalf("a@x.com goes through %q", got)
	}
}

// With no proxy on Antigravity, agy-cli is left following the global one.
func TestMoveToAgyWithoutAProxyWritesNone(t *testing.T) {
	signIn(t)
	writePluginAuth(t, map[string]any{"antigravity": map[string]any{"type": "oauth", "refresh": "1//a|p", "accountId": "a@x.com"}})
	s := standInAgy(t, true)
	agyCLIMissing = func(string) string { return "Antigravity CLI" }
	if _, err := MoveToAgy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedPicks(agyCLIID); ok || s.cliVia != "" {
		t.Fatalf("agy-cli written (%v), installed through %q", ok, s.cliVia)
	}
}
