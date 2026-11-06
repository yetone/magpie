package provider

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A routing group is asked on Responses when one member is best there and
// every other takes a Responses request as it is too, or translates every
// request anyway (zyh6036 on X: subscriptions that all serve Responses
// were asked on Chat in a group, as only a group all of whose members were
// best on Responses went there, #1594). A member Responses doesn't reach
// keeps the group on Chat, where it is relayed.
func TestGroupAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	resp := Provider{ID: "resp", Responses: "http://127.0.0.1:1/v1"}                                // best on Responses
	both := Provider{ID: "both", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1"} // Chat first, Responses too
	chat := Provider{ID: "chat", Chat: "http://127.0.0.1:1/v1"}
	anth := Provider{ID: "anth", Anthropic: "http://127.0.0.1:1"}
	claude := Provider{ID: "claude", Anthropic: "http://127.0.0.1:1", Account: &Account{Agent: "claude"}} // translated anyway
	of := func(ps ...Provider) []Member {
		var ms []Member
		for _, p := range ps {
			ms = append(ms, Member{Provider: p, Model: "gpt-5.5"})
		}
		return ms
	}
	if n := both.Native("gpt-5.5"); n != Chat {
		t.Fatalf("the Chat-first relay's GPT is best on %q; the cases below assume Chat", n)
	}
	for _, c := range []struct {
		name      string
		ms        []Member
		api       Protocol
		endpoints []string
	}{
		{"all best on Responses", of(resp, resp), Responses, []string{"/v1/responses"}},
		{"one best there, one serving it too", of(resp, both), Responses, []string{"/v1/responses"}},
		{"the one best there second", of(both, resp), Responses, []string{"/v1/responses"}},
		{"with a subscription translated anyway", of(resp, claude), Responses, nil},
		{"none best on Responses", of(both, both), "", []string{"/v1/chat/completions", "/v1/responses"}},
		{"a Chat-only member", of(resp, chat), "", nil},
		{"a Messages-only member", of(resp, anth), "", nil},
		{"Messages-only", of(anth, anth), "", []string{"/v1/messages"}},
		{"no members", nil, "", nil},
	} {
		if got := GroupAPI(c.ms); got != c.api {
			t.Errorf("%s: API %q, want %q", c.name, got, c.api)
		}
		if got := GroupEndpoints(c.ms); !slices.Equal(got, c.endpoints) {
			t.Errorf("%s: endpoints %v, want %v", c.name, got, c.endpoints)
		}
	}
}
