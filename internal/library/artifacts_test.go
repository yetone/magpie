package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// givenUnasked puts the library where a Sync of 11-24 left it: Magpie
// Artifacts added for every agent that takes it, and ArtifactsOffered.
func givenUnasked(t *testing.T) []string {
	t.Helper()
	m := artifactsServer()
	s := m.server
	selfCommand(&s)
	var agents []string
	for _, tg := range Targets() {
		if tg.MCP != nil && tg.MCP.supports(&s) == nil && !slices.Contains(agents, tg.Agent.ID) {
			agents = append(agents, tg.Agent.ID)
		}
	}
	ok(t)(InstallServer(ArtifactsServerName, nil, agents))
	ok(t)(change(func(l *Library) error { l.ArtifactsOffered = true; return nil }))
	return agents
}

// Nothing gives Magpie Artifacts to an agent unasked: a Sync adds no
// server and leaves Claude Code's own Artifact tool alone.
func TestArtifactsNotGivenUnasked(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Bash(rm:*)"]}}`)
	ok(t)(Sync())
	l, _ := load()
	for _, s := range l.MCP {
		if serverKey(s) == artifactsKey {
			t.Fatalf("given unasked: %+v", s)
		}
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)"}) {
		t.Errorf("deny: %v", deny)
	}
}

// The one a Sync of 11-24 added unasked is taken back by the next Sync,
// once, with Claude Code's deny of its own Artifact; added again by the
// user, it stays.
func TestArtifactsGivenUnaskedTakenBack(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Bash(rm:*)"]}}`)
	agents := givenUnasked(t)
	if !slices.Contains(agents, "claude") || !slices.Contains(agents, "codex") {
		t.Fatalf("the old adding's agents: %v", agents)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Fatalf("set up: deny %v", deny)
	}
	if es, _ := targetByID("claude").MCP.entries(); es[ArtifactsServerName] == nil {
		t.Fatalf("set up: Claude Code's config has no %s", ArtifactsServerName)
	}

	ok(t)(Sync())
	l, _ := load()
	if l.server(ArtifactsServerName) != nil || !l.ArtifactsWithdrawn {
		t.Fatalf("not taken back: %+v withdrawn=%v", l.server(ArtifactsServerName), l.ArtifactsWithdrawn)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)"}) {
		t.Errorf("Claude's own Artifact still left out: %v", deny)
	}
	if es, _ := targetByID("claude").MCP.entries(); es[ArtifactsServerName] != nil {
		t.Errorf("Claude Code's config still has %s", ArtifactsServerName)
	}
	if es, _ := targetByID("codex").MCP.entries(); es[ArtifactsServerName] != nil {
		t.Errorf("Codex's config still has %s", ArtifactsServerName)
	}

	// the user adds it again: theirs, and it stays
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude"}))
	ok(t)(Sync())
	l, _ = load()
	if s := l.server(ArtifactsServerName); s == nil || !slices.Equal(s.Agents, []string{"claude"}) {
		t.Fatalf("the user's own was taken: %+v", s)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Errorf("chosen for Claude Code, its own Artifact not left out: %v", deny)
	}
}

// One the user changed since it was added (an agent unticked) is theirs.
func TestArtifactsChangedStays(t *testing.T) {
	sandbox(t)
	agents := givenUnasked(t)
	l, _ := load()
	mine := *l.server(ArtifactsServerName)
	mine.Agents = slices.DeleteFunc(slices.Clone(agents), func(a string) bool { return a == "codex" })
	ok(t)(SaveServer(ArtifactsServerName, mine))
	ok(t)(Sync())
	l, _ = load()
	if s := l.server(ArtifactsServerName); s == nil || slices.Contains(s.Agents, "codex") || !slices.Contains(s.Agents, "claude") {
		t.Fatalf("the user's changed one: %+v", s)
	}
	if !l.ArtifactsWithdrawn {
		t.Error("not recorded: a later Sync would look again")
	}
}

// A rule of the user's for Claude Code's Artifact is theirs: one that
// allows it keeps it, and one that denies it stays when magpie's goes.
func TestArtifactsKeepTheUsersRule(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"allow": ["Artifact"]}}`)
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude"}))
	if got := read(t, settings); strings.Contains(got, "deny") {
		t.Errorf("denied what the user allowed:\n%s", got)
	}

	h = sandbox(t)
	settings = filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Artifact"]}}`)
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude"}))
	l, _ := load()
	if r := l.applied("claude").ToolRules; slices.Contains(r, claudeArtifactTool) {
		t.Errorf("the user's rule recorded as magpie's: %v", r)
	}
	ok(t)(RemoveServer(ArtifactsServerName))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{claudeArtifactTool}) {
		t.Errorf("the user's own deny went: %v", deny)
	}
}

// Work › Artifacts' empty page says which agents can make one: those
// Magpie Artifacts is ticked on, none once it is turned off or removed.
func TestArtifactsAgents(t *testing.T) {
	sandbox(t)
	if got, err := ArtifactsAgents(); err != nil || len(got) != 0 {
		t.Fatalf("before it is given: %v %v", got, err)
	}
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"codex", "claude"}))
	if got, err := ArtifactsAgents(); err != nil || !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("given to two: %v %v", got, err)
	}
	ok(t)(ServerOn(ArtifactsServerName, false))
	if got, _ := ArtifactsAgents(); len(got) != 0 {
		t.Fatalf("turned off: %v", got)
	}
	ok(t)(ServerOn(ArtifactsServerName, true))
	ok(t)(RemoveServer(ArtifactsServerName))
	if got, _ := ArtifactsAgents(); len(got) != 0 {
		t.Fatalf("removed: %v", got)
	}
}
