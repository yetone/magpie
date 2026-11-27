package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"gopkg.in/yaml.v3"
)

// A maxTokens typed by hand into a model on any of magpie's dsh routes
// stays, not only on the last route written; and a sync that changes
// nothing writes no stash (each write drops every cache built on the
// files read). One record serves every route: written a route at a time,
// each write dropped the models of the routes before it.
func TestDshKeepsMaxTokensOnEveryRoute(t *testing.T) {
	syncHome(t)
	models := []catalog.Model{
		{ID: "relay/chat-model", Name: "Chat", Context: 200000, Output: 100000},
		{ID: "relay/messages-model", Name: "Messages", Context: 200000, Output: 100000, APIs: []string{"anthropic"}},
	}
	providers := &yaml.Node{Kind: yaml.MappingNode}
	put := func() {
		t.Helper()
		if err := dshPutProviders(providers, true, models, "http://127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
	}
	maxTokens := func(route, id string) *yaml.Node {
		t.Helper()
		ms := yamlKey(yamlKey(providers, route), "models")
		if ms == nil {
			t.Fatalf("no models on route %s", route)
		}
		for _, m := range ms.Content {
			if v := yamlKey(m, "id"); v != nil && v.Value == id {
				if mt := yamlKey(m, "maxTokens"); mt != nil {
					return mt
				}
			}
		}
		t.Fatalf("no maxTokens for %s on route %s", id, route)
		return nil
	}
	put()
	chat, msgs := dshRoute, dshRoute+"-anthropic"
	wantChat, wantMsgs := maxTokens(chat, "relay/chat-model").Value, maxTokens(msgs, "relay/messages-model").Value

	gen := filememo.Gen()
	put()
	if filememo.Gen() != gen {
		t.Error("a sync that changed nothing wrote the stash")
	}

	// typed by hand on each route: both stay
	maxTokens(chat, "relay/chat-model").Value = "1234"
	maxTokens(msgs, "relay/messages-model").Value = "5678"
	put()
	if got := maxTokens(chat, "relay/chat-model").Value; got != "1234" {
		t.Errorf("maxTokens typed on the Chat route = %s after a sync, want 1234 kept (magpie's %s)", got, wantChat)
	}
	if got := maxTokens(msgs, "relay/messages-model").Value; got != "5678" {
		t.Errorf("maxTokens typed on the Messages route = %s after a sync, want 5678 kept (magpie's %s)", got, wantMsgs)
	}
}
