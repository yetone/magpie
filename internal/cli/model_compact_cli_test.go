package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model compact off runs Codex's and Claude Code's conversations to
// the model's whole window, on compacts them at the working window again
// (Max on Discord found no way to it but the app's Settings); anything else
// is refused.
func TestModelCompactCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"compact"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"compact", "off"}); err != nil || !settings.Load().FullContext {
		t.Fatal(err, settings.Load().FullContext)
	}
	if err := modelCmd([]string{"compact"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"compact", "on"}); err != nil || settings.Load().FullContext {
		t.Fatal(err, settings.Load().FullContext)
	}
	if err := modelCmd([]string{"full-context", "full"}); err != nil || !settings.Load().FullContext {
		t.Fatal(err, settings.Load().FullContext)
	}
	if err := modelCmd([]string{"compact", "maybe"}); err == nil {
		t.Fatal("took maybe")
	}
}

// magpie model compact codex 300k gives Codex a Long conversations of its
// own, which Claude Code doesn't take; every takes it away, and an agent
// that writes no compaction window is refused (molianyin4227 on Discord).
func TestModelCompactAgentCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	if err := modelCmd([]string{"compact", "codex", "300k"}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	if s.ForAgent("codex").Compact() != 300000 || s.ForAgent("claude").Compact() == 300000 {
		t.Fatalf("codex %d, claude %d", s.ForAgent("codex").Compact(), s.ForAgent("claude").Compact())
	}
	if err := modelCmd([]string{"compact", "grok", "off"}); err != nil || !settings.Load().ForAgent("grok").FullContext || settings.Load().FullContext {
		t.Fatal(err, settings.Load().AgentCompacts)
	}
	if err := modelCmd([]string{"compact", "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"compact", "codex", "every"}); err != nil {
		t.Fatal(err)
	}
	if _, has := settings.Load().AgentCompacts["codex"]; has {
		t.Fatal("every kept Codex's own")
	}
	if err := modelCmd([]string{"compact", "codex", "lots"}); err == nil {
		t.Fatal("took lots")
	}
	if len(settings.Load().AgentCompacts) != 1 {
		t.Fatal(settings.Load().AgentCompacts)
	}
}
