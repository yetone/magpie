package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key+URL provider passes the agent's User-Agent on by default (#1851,
// nb5p on Discord): user-agent=magpie sends magpie's in its place,
// user-agent=agent goes back to the default, and a signed-in account, which
// goes as its own client, is refused agent.
func TestProviderUserAgentPair(t *testing.T) {
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: "https://plan.example/v1", Key: "k"}
	if err := applyPairs(&p, []string{"user-agent=magpie"}); err != nil || !p.MagpieUserAgent || p.PassesUserAgent() {
		t.Fatalf("magpie: %v %v", p.MagpieUserAgent, err)
	}
	if err := applyPairs(&p, []string{"user-agent=agent"}); err != nil || p.MagpieUserAgent || !p.PassesUserAgent() {
		t.Fatalf("agent: %v %v", p.MagpieUserAgent, err)
	}
	if err := applyPairs(&p, []string{"user-agent=claude"}); err == nil || !strings.Contains(err.Error(), "agent|magpie") {
		t.Fatalf("a made-up value: %v", err)
	}
	a := provider.Provider{ID: "zcode", Name: "ZCode", Account: &provider.Account{Agent: "zcode"}}
	if err := applyPairs(&a, []string{"user-agent=agent"}); err == nil || a.MagpieUserAgent {
		t.Fatalf("an account took it: %v", err)
	}
	if err := applyPairs(&a, []string{"user-agent=magpie"}); err != nil || a.MagpieUserAgent {
		t.Fatalf("an account turning it off: %v %v", a.MagpieUserAgent, err)
	}
}

// compaction=native has a provider whose wire carries it be asked Codex's
// own compaction (#1099), compaction=magpie goes back to magpie's summary,
// and a provider without a Responses API is refused it.
func TestProviderCompactionPair(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Responses: "https://relay.example/v1", Key: "k"}
	if err := applyPairs(&p, []string{"compaction=native"}); err != nil || !p.NativeCompaction {
		t.Fatalf("native: %v %v", p.NativeCompaction, err)
	}
	if err := applyPairs(&p, []string{"compaction=magpie"}); err != nil || p.NativeCompaction {
		t.Fatalf("magpie: %v %v", p.NativeCompaction, err)
	}
	if err := applyPairs(&p, []string{"compaction=sometimes"}); err == nil || !strings.Contains(err.Error(), "native|magpie") {
		t.Fatalf("a made-up value: %v", err)
	}
	c := provider.Provider{ID: "chat", Name: "Chat", Chat: "https://chat.example/v1", Key: "k"}
	if err := applyPairs(&c, []string{"compaction=native"}); err == nil {
		t.Fatalf("a chat-only provider took it")
	}
	cp := provider.Provider{ID: "copilot", Name: "GitHub Copilot", Responses: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot"}}
	if err := applyPairs(&cp, []string{"compaction=native"}); err != nil || !cp.NativeCompaction {
		t.Fatalf("copilot: %v %v", cp.NativeCompaction, err)
	}
}
