package provider

import (
	"slices"
	"testing"
)

// An agent set to one key of a provider (nb5p on Discord) keeps it by the
// key's id, found by its id or name in any case; it is listed as
// "<provider>/<id>" for the gateway; a key replaced keeps the agent on it;
// a key taken out holds the agent to nothing; "" lets it go, and a key
// the provider hasn't is refused.
func TestSetAgentAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-0",
		Keys: []KeyAccount{{Name: "Work", Key: "sk-work"}, {Name: "Personal", Key: "sk-home"}}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccount("relay", "Claude", "work"); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccount("relay", "codex", KeyID("sk-home")); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccount("relay", "codex", "nobody"); err == nil {
		t.Fatal("a key the provider hasn't was taken")
	}
	if err := SetAgentAccount("relay", " ", "work"); err == nil {
		t.Fatal("no agent was taken")
	}
	if got := AgentAccounts("claude"); !slices.Equal(got, []string{"relay/" + KeyID("sk-work")}) {
		t.Fatalf("claude: %v", got)
	}
	if got := AgentAccounts("codex"); !slices.Equal(got, []string{"relay/" + KeyID("sk-home")}) {
		t.Fatalf("codex: %v", got)
	}
	if got := AgentAccounts("opencode"); len(got) != 0 {
		t.Fatalf("an agent set to none: %v", got)
	}
	// the key's value replaced: the agent stays on it
	if _, _, err := ReplaceKey("relay", KeyID("sk-work"), "sk-work-2"); err != nil {
		t.Fatal(err)
	}
	if got := AgentAccounts("claude"); !slices.Equal(got, []string{"relay/" + KeyID("sk-work-2")}) {
		t.Fatalf("after the key was replaced: %v", got)
	}
	// the key taken out: nothing held
	if err := RemoveKey("relay", KeyID("sk-home")); err != nil {
		t.Fatal(err)
	}
	if got := AgentAccounts("codex"); len(got) != 0 {
		t.Fatalf("a key taken out: %v", got)
	}
	// a provider saved as the editor saves it keeps what is set
	p, _ := Find("relay")
	if p.AgentAccounts["claude"] != KeyID("sk-work-2") {
		t.Fatalf("kept %v", p.AgentAccounts)
	}
	// let go
	if err := SetAgentAccount("relay", "claude", ""); err != nil {
		t.Fatal(err)
	}
	if got := AgentAccounts("claude"); len(got) != 0 {
		t.Fatalf("let go: %v", got)
	}
}
