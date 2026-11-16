package provider

import (
	"encoding/json"
	"testing"
)

// A subscription left out of sync is left out under each of its ids: its
// own accounts, its plugin's sign-ins (claude-plugin) and its Codex
// record; With puts what Except took back, without writing over s's own.
func TestSignInsExcept(t *testing.T) {
	s := &SignIns{
		Logins: []savedLogin{{Agent: "claude", User: "c@x.com"}, {Agent: "gemini", User: "g@x.com"}},
		Plugins: map[string]json.RawMessage{
			"claude-plugin#c@x.com": json.RawMessage(`1`),
			"kiro":                  json.RawMessage(`2`),
			"zed":                   json.RawMessage(`3`),
		},
		Codex: "codex|me@x.com|org",
	}
	if k, h := s.Except(nil); k != s || h != nil {
		t.Fatal("nothing left out isn't s as it is")
	}
	if k, h := s.Except([]string{" "}); k != s || h != nil {
		t.Fatal("a blank id left something out")
	}
	for _, ids := range [][]string{{"claude", "kiro-plugin", "codex"}, {"claude-plugin", "kiro", "codex"}} {
		kept, held := s.Except(ids)
		if len(kept.Logins) != 1 || kept.Logins[0].Agent != "gemini" || kept.Codex != "" || len(kept.Plugins) != 1 || kept.Plugins["zed"] == nil {
			t.Fatalf("%v kept %+v", ids, kept)
		}
		if len(held.Logins) != 1 || held.Logins[0].Agent != "claude" || held.Codex != s.Codex || len(held.Plugins) != 2 || held.Plugins["kiro"] == nil || held.Plugins["claude-plugin#c@x.com"] == nil {
			t.Fatalf("%v held %+v", ids, held)
		}
		back := kept.With(held)
		if len(back.Logins) != 2 || len(back.Plugins) != 3 || back.Codex != s.Codex {
			t.Fatalf("%v put back %+v", ids, back)
		}
	}
	if _, h := s.Except([]string{"grok"}); h != nil {
		t.Fatalf("a subscription with no sign-ins here held %+v", h)
	}
	// s's own plugin sign-in and Codex record stay over held's
	mine := &SignIns{Plugins: map[string]json.RawMessage{"kiro": json.RawMessage(`9`)}, Codex: "codex|b@x.com|o"}
	out := mine.With(&SignIns{Plugins: map[string]json.RawMessage{"kiro": json.RawMessage(`2`)}, Codex: "codex|a@x.com|o"})
	if string(out.Plugins["kiro"]) != "9" || out.Codex != mine.Codex {
		t.Fatalf("held wrote over s's own: %+v", out)
	}
	if string(mine.Plugins["kiro"]) != "9" || len(mine.Plugins) != 1 {
		t.Fatal("With changed s")
	}
}
