package settings

import "testing"

// Grok Build reads magpie's default for every agent as its whole window,
// where it compacts at its own percent (@taktoshilite on X), while a size
// typed for every agent or for Grok Build, and Codex's and Claude Code's
// default, are as they were.
func TestGrokFollowsItsOwnThresholdByDefault(t *testing.T) {
	var s Settings
	if g := s.ForAgent("grok"); !g.FullContext || g.Compact() != 0 || !s.GrokOwnCompact() {
		t.Fatalf("default: grok full %v, at %d", g.FullContext, g.Compact())
	}
	for _, k := range []string{"codex", "claude"} {
		if n := s.ForAgent(k).Compact(); n != WorkingWindow {
			t.Fatalf("%s's default %d", k, n)
		}
	}
	s.CompactAt = 400000
	if n := s.ForAgent("grok").Compact(); n != 400000 || s.GrokOwnCompact() {
		t.Fatalf("400K for every agent: grok %d", n)
	}
	s.CompactAt = 0
	s.AgentCompacts = map[string]AgentCompact{"grok": {}}
	if n := s.ForAgent("grok").Compact(); n != WorkingWindow || s.GrokOwnCompact() {
		t.Fatalf("272K picked for grok: %d", n)
	}
	s.AgentCompacts = map[string]AgentCompact{"codex": {At: 300000}}
	if g := s.ForAgent("grok"); !g.FullContext {
		t.Fatalf("codex's own reached grok: %+v", g.Compact())
	}
}
