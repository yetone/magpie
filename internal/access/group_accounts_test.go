package access

import (
	"slices"
	"testing"
)

// A routing group's accounts narrow whoever asks through it: a request
// with no key is held to the group's, a key's own list meets the group's
// (a provider both name keeps only the accounts both allow, and none
// when they share none), and a provider the group doesn't name is the
// key's as before. A group naming nothing changes nothing.
func TestHeldToGroup(t *testing.T) {
	anyone := Identity{}
	if got := anyone.HeldToGroup("Work", nil); got.Group != "" || len(got.Accounts) != 0 {
		t.Fatalf("a group with no accounts: %+v", got)
	}
	got := anyone.HeldToGroup("Work", []string{"codex/work@x.com"})
	if got.Group != "Work" || !got.AllowsAccount("codex", "work@x.com") || got.AllowsAccount("codex", "me@x.com") || !got.AllowsAccount("relay", "k") {
		t.Fatalf("no key: %+v", got)
	}
	key := Identity{Accounts: []string{"codex/me@x.com", "codex/work@x.com", "relay/k1"}}
	got = key.HeldToGroup("Work", []string{"codex/work@x.com", "codex/other@x.com"})
	if !slices.Equal(got.Accounts, []string{"relay/k1", "codex/work@x.com"}) {
		t.Fatalf("a key and a group: %v", got.Accounts)
	}
	if got.AllowsAccount("codex", "other@x.com") || got.AllowsAccount("relay", "k2") {
		t.Fatalf("a key and a group allow too much: %v", got.Accounts)
	}
	apart := Identity{Accounts: []string{"codex/me@x.com"}}.HeldToGroup("Work", []string{"codex/work@x.com"})
	if apart.AllowsAccount("codex", "me@x.com") || apart.AllowsAccount("codex", "work@x.com") || !apart.AllowsAccount("relay", "k") {
		t.Fatalf("apart: %v", apart.Accounts)
	}
	if shown := apart.ShownAccounts(); len(shown) != 0 {
		t.Fatalf("apart shows %v", shown)
	}
	// the key it was given is not changed
	if len(key.Accounts) != 3 || key.Group != "" {
		t.Fatalf("the key's identity changed: %+v", key)
	}
}

// An agent's own accounts (nb5p on Discord) hold a request by the group's
// rule, and are not a gateway key's: KeyHeld stays false while they alone
// hold it, so the paths a held key changes don't change for them. A key's
// or a group's accounts beside them are.
func TestHeldToAgent(t *testing.T) {
	anyone := Identity{}
	if got := anyone.HeldToAgent("claude", nil); got.Agent != "" || got.AgentOnly() || got.KeyHeld() {
		t.Fatalf("an agent set to none: %+v", got)
	}
	got := anyone.HeldToAgent("claude", []string{"relay/kA"})
	if got.Agent != "claude" || !got.AllowsAccount("relay", "kA") || got.AllowsAccount("relay", "kB") || !got.AllowsAccount("codex", "me@x.com") {
		t.Fatalf("no key: %+v", got)
	}
	if !got.AgentOnly() || got.KeyHeld() || !got.Restricted() {
		t.Fatalf("held by the agent alone: agentOnly %v, KeyHeld %v, Restricted %v", got.AgentOnly(), got.KeyHeld(), got.Restricted())
	}
	if !got.NamesProvider("relay") || !got.NamesProvider("RELAY") || got.NamesProvider("codex") {
		t.Fatalf("NamesProvider: %v", got.Accounts)
	}
	// a group over it: a group's accounts are not the agent's alone
	if g := got.HeldToGroup("Work", []string{"codex/work@x.com"}); g.AgentOnly() || !g.KeyHeld() {
		t.Fatalf("with a group: agentOnly %v", g.AgentOnly())
	}
	// a key's accounts under it: held as a key is, and the two narrowed
	key := Identity{Accounts: []string{"relay/kA", "relay/kB", "codex/me@x.com"}}
	got = key.HeldToAgent("claude", []string{"relay/kB"})
	if got.AgentOnly() || !got.KeyHeld() || got.AllowsAccount("relay", "kA") || !got.AllowsAccount("relay", "kB") || !got.AllowsAccount("codex", "me@x.com") {
		t.Fatalf("a key and an agent: %v", got.Accounts)
	}
	apart := Identity{Accounts: []string{"relay/kA"}}.HeldToAgent("claude", []string{"relay/kB"})
	if apart.AllowsAccount("relay", "kA") || apart.AllowsAccount("relay", "kB") {
		t.Fatalf("apart: %v", apart.Accounts)
	}
	if len(key.Accounts) != 3 || key.Agent != "" {
		t.Fatalf("the key's identity changed: %+v", key)
	}
}
