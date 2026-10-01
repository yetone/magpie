package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
)

// movedGrok is Grok moved onto its plugin, which lists grok-new.
func movedGrok(t *testing.T) (plugin.Provider, Provider) {
	t.Helper()
	claudeHome(t)
	if err := setMigration("grok", func(m *Migration) { m.State = MovePlugin }); err != nil {
		t.Fatal(err)
	}
	pp := plugin.Provider{ID: "grok", Name: "Grok", NPM: "@ai-sdk/openai", Models: []plugin.Model{{ID: "grok-new", Name: "Grok New", NPM: "@ai-sdk/openai"}}}
	return pp, pluginProvider(pp, pluginLogin{})
}

// A moved provider's models are its plugin's: the list the built-in last
// fetched, still kept under the same id, isn't them.
func TestMovedProviderModelsArePlugins(t *testing.T) {
	_, p := movedGrok(t)
	if err := catalog.SaveLive("grok", "https://cli-chat-proxy.grok.com/v1", []catalog.Model{{ID: "grok-old", Provider: "grok"}}); err != nil {
		t.Fatal(err)
	}
	if p.ID != "grok" || !p.IsPlugin() {
		t.Fatalf("moved grok = %+v", p)
	}
	var ids []string
	for _, m := range p.Available() {
		ids = append(ids, m.ID)
	}
	if len(ids) != 1 || ids[0] != "grok-new" {
		t.Fatalf("moved grok's models = %v, want the plugin's [grok-new]", ids)
	}
	if _, ok := p.Fetched(); ok {
		t.Error("moved grok shows the built-in's list as fetched")
	}
}

// A plugin's model has a price only when the plugin gives one: a plan's
// models come priced 0, as OpenCode prices a model it has none for, and a
// 0 would make them the cheapest to pick. Whether it takes images is said
// when the plugin said it.
func TestPluginCatalogPriceAndImages(t *testing.T) {
	type cost = struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	}
	pp := plugin.Provider{ID: "fakeco", Models: []plugin.Model{
		{ID: "plan", Cost: &cost{}, Image: true, ImageSaid: true},
		{ID: "priced", Cost: &cost{Input: 1, Output: 2}, ImageSaid: true},
		{ID: "unsaid", Cost: &cost{}},
	}}
	got := map[string]catalog.Model{}
	for _, m := range pluginCatalog(pp) {
		got[m.ID] = m
	}
	if p := got["plan"].Price; p != nil {
		t.Errorf("a plan's model priced %+v", *p)
	}
	if p := got["priced"].Price; p == nil || p.Input != 1 || p.Output != 2 {
		t.Errorf("a priced model's price: %+v", p)
	}
	if in := got["plan"].ImageInput; in == nil || !*in {
		t.Errorf("a model said to take images: %v", in)
	}
	if in := got["priced"].ImageInput; in == nil || *in {
		t.Errorf("a model said to take none: %v", in)
	}
	if in := got["unsaid"].ImageInput; in != nil {
		t.Errorf("a model not said of: %v", *in)
	}
}

// A built-in moved onto its plugin stands where the built-in stood among
// the accounts; a plugin's own provider goes last.
func TestMovedAccountKeepsItsPlace(t *testing.T) {
	_, grok := movedGrok(t)
	fake := pluginProvider(plugin.Provider{ID: "fakeco"}, pluginLogin{})
	out := placeMoved([]Provider{{ID: "claude"}, {ID: "codex"}, {ID: "zed"}, {ID: "gemini"}}, []Provider{fake, grok})
	var ids []string
	for _, p := range out {
		ids = append(ids, p.ID)
	}
	if want := []string{"claude", "codex", "grok", "zed", "gemini", "fakeco"}; !slices.Equal(ids, want) {
		t.Fatalf("accounts %v, want %v", ids, want)
	}
	if !out[2].IsPlugin() {
		t.Fatal("the moved Grok isn't its plugin's")
	}
}

// Two built-ins moved onto plugins keep the built-ins' order whatever
// order the plugins list them in.
func TestMovedAccountsKeepTheirOrder(t *testing.T) {
	claudeHome(t)
	var ps []Provider
	for _, id := range []string{WorkBuddyAIID, "workbuddy"} {
		if err := setMigration(id, func(m *Migration) { m.State = MovePlugin }); err != nil {
			t.Fatal(err)
		}
		ps = append(ps, pluginProvider(plugin.Provider{ID: id, Name: id}, pluginLogin{}))
	}
	out := placeMoved([]Provider{{ID: "claude"}, {ID: CommandCodePlanID}, {ID: "zed"}}, ps)
	var ids []string
	for _, p := range out {
		ids = append(ids, p.ID)
	}
	if want := []string{"claude", "workbuddy", WorkBuddyAIID, CommandCodePlanID, "zed"}; !slices.Equal(ids, want) {
		t.Fatalf("accounts %v, want %v", ids, want)
	}
}

// A moved provider's usage is asked through the proxy set for it, or for
// the account: they are kept under its id, not its sign-ins' "plugin:grok".
func TestMovedLoginProxy(t *testing.T) {
	movedGrok(t)
	if err := store(file{Providers: []Provider{{ID: "grok", Proxy: "http://127.0.0.1:1", AccountProxies: map[string]string{"me@example.com": "direct"}}}}); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"me@example.com": "direct", "other@example.com": "http://127.0.0.1:1"} {
		l := Login{Agent: "plugin:grok", User: user}
		if got := netproxy.Choice(ViaLogin(context.Background(), loginProvider(l), l.User)); got != want {
			t.Errorf("%s's usage goes through %q, want %q", user, got, want)
		}
	}
}
