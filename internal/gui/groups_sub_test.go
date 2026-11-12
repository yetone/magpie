package gui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// #1839: a model a subscription account serves says so to the Routing page,
// which tells beside a classifier set to it that every turn starts with a
// request to that subscription; a key's model says nothing of it.
func TestServedRefSaysASubscription(t *testing.T) {
	sub := servedRef(provider.Entry{ID: "codex/gpt-5-mini", Name: "gpt-5-mini", Model: "gpt-5-mini",
		Provider: &provider.Provider{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex", User: "a@x.y"}}}, settings.Settings{})
	if !sub.Sub {
		t.Fatalf("a subscription's model: %+v", sub)
	}
	key := servedRef(provider.Entry{ID: "or/flash", Name: "flash", Model: "flash", Provider: &provider.Provider{ID: "or", Name: "OpenRouter", Key: "k"}}, settings.Settings{})
	if key.Sub {
		t.Fatalf("a key's model: %+v", key)
	}
	b, _ := json.Marshal(key)
	if strings.Contains(string(b), `"sub"`) {
		t.Fatalf("a key's model sends no sub: %s", b)
	}
	b, _ = json.Marshal(sub)
	if !strings.Contains(string(b), `"sub":true`) {
		t.Fatalf("%s", b)
	}
}
