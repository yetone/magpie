package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A model seals a V2 lead's subagent tasks when its provider is of no
// sign-in on the Responses API (a relay of the ChatGPT backend, #1109), or
// a group has such a member; a chat-only key and another account
// (Copilot) seal none (brethhbbm on X).
func TestCodexSeals(t *testing.T) {
	relay := Provider{ID: "relay", Chat: "https://relay.example/v1", Responses: "https://relay.example/v1"}
	chat := Provider{ID: "ds", Chat: "https://api.deepseek.example/v1"}
	copilot := Provider{ID: "copilot", Responses: "https://api.githubcopilot.example", Account: &Account{Agent: "copilot"}}
	members := map[string][]Member{
		"group/mixed": {{Provider: chat, Model: "deepseek-v4"}, {Provider: relay, Model: "gpt-6-astra"}},
		"group/plain": {{Provider: chat, Model: "deepseek-v4"}, {Provider: copilot, Model: "gpt-6.1-sol"}},
	}
	of := func(id string) []Member { return members[id] }
	for _, c := range []struct {
		e    Entry
		want bool
	}{
		{Entry{ID: "relay/gpt-6-astra", Provider: &relay, Model: "gpt-6-astra"}, true},
		{Entry{ID: "ds/deepseek-v4", Provider: &chat, Model: "deepseek-v4"}, false},
		{Entry{ID: "copilot/gpt-6.1-sol", Provider: &copilot, Model: "gpt-6.1-sol"}, false},
		{Entry{ID: "group/mixed", Group: "mixed"}, true},
		{Entry{ID: "group/plain", Group: "plain"}, false},
	} {
		var m catalog.Model
		CodexSeals(&m, c.e, of)
		if m.Seals != c.want {
			t.Errorf("%s: Seals %v, want %v", c.e.ID, m.Seals, c.want)
		}
	}
}
