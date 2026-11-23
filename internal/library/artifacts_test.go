package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func offeringArtifacts(t *testing.T) {
	t.Helper()
	offerArtifacts = true
	t.Cleanup(func() { offerArtifacts = false })
}

// Magpie Artifacts is given to every agent that takes it on the first
// Sync, once: removed, it stays removed. Claude Code given it leaves its
// own Artifact tool out, and has it back when the server goes.
func TestArtifactsGivenOnce(t *testing.T) {
	h := sandbox(t)
	offeringArtifacts(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Bash(rm:*)"]}}`)
	ok(t)(Sync())
	l, _ := load()
	s := l.server(ArtifactsServerName)
	if s == nil || !l.ArtifactsOffered || !slices.Contains(s.Agents, "claude") || !slices.Contains(s.Agents, "codex") {
		t.Fatalf("not given: %+v offered=%v", s, l.ArtifactsOffered)
	}
	if serverKey(s) != "magpie mcp artifacts" {
		t.Fatalf("not magpie's own: %+v", s)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Errorf("deny: %v", deny)
	}
	es, _ := targetByID("claude").MCP.entries()
	if es[ArtifactsServerName] == nil {
		t.Errorf("Claude Code's config has no %s: %v", ArtifactsServerName, es)
	}

	ok(t)(RemoveServer(ArtifactsServerName))
	ok(t)(Sync())
	l, _ = load()
	if l.server(ArtifactsServerName) != nil {
		t.Fatal("a removed Magpie Artifacts came back")
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)"}) {
		t.Errorf("Claude's own Artifact still left out after the server went: %v", deny)
	}
}

// A rule of the user's for Claude Code's Artifact is theirs: one that
// allows it keeps it, and one that denies it stays when magpie's goes.
func TestArtifactsKeepTheUsersRule(t *testing.T) {
	h := sandbox(t)
	offeringArtifacts(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"allow": ["Artifact"]}}`)
	ok(t)(Sync())
	if got := read(t, settings); strings.Contains(got, "deny") {
		t.Errorf("denied what the user allowed:\n%s", got)
	}

	h = sandbox(t)
	settings = filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Artifact"]}}`)
	ok(t)(Sync())
	l, _ := load()
	if r := l.applied("claude").ToolRules; slices.Contains(r, claudeArtifactTool) {
		t.Errorf("the user's rule recorded as magpie's: %v", r)
	}
	ok(t)(RemoveServer(ArtifactsServerName))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{claudeArtifactTool}) {
		t.Errorf("the user's own deny went: %v", deny)
	}
}

// A library that has the server already, added by hand under another
// name, gets no second one.
func TestArtifactsNotTwice(t *testing.T) {
	sandbox(t)
	offeringArtifacts(t)
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude"}))
	l, _ := load()
	mine := *l.server(ArtifactsServerName)
	mine.Name = "pages"
	ok(t)(SaveServer(ArtifactsServerName, mine))
	ok(t)(Sync())
	l, _ = load()
	n := 0
	for _, s := range l.MCP {
		if serverKey(s) == "magpie mcp artifacts" {
			n++
		}
	}
	if n != 1 || !l.ArtifactsOffered {
		t.Fatalf("%d of them: %+v", n, l.MCP)
	}
}
