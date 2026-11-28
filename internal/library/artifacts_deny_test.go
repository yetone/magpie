package library

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// Claude Code's deny of its own Artifact (artifacts.go): written only while
// Magpie Artifacts is really given to Claude Code, never again once the
// user took it out by hand, and taken back with the server (rosu_h on X:
// "why does every start write deny: [Artifact]?"; seiry on #7646 deleted
// it and it was back three minutes later).

// One the user deleted by hand while the server stays given is theirs: no
// Sync puts it back. Adding the server again, after it went, is asking
// for it again.
func TestArtifactsDenyTheUserTookOutStaysOut(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"defaultMode": "auto", "deny": ["Bash(rm:*)"]}}`)
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude", "codex"}))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Fatalf("set up: deny %v", deny)
	}

	// the user takes magpie's line out, and magpie starts twice
	write(t, settings, `{"permissions": {"defaultMode": "auto", "deny": ["Bash(rm:*)"]}}`)
	for i := range 2 {
		ok(t)(Sync())
		if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)"}) {
			t.Fatalf("start %d wrote the deny the user took out back: %v", i+1, deny)
		}
	}
	// the server stays given
	if es, _ := targetByID("claude").MCP.entries(); es[ArtifactsServerName] == nil {
		t.Fatalf("Claude Code lost %s", ArtifactsServerName)
	}
	// nor does another change to the server put it back
	ok(t)(ServerTools(ArtifactsServerName, []string{"delete_artifact"}))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", "mcp__magpie-artifacts__delete_artifact"}) {
		t.Fatalf("a tool turned off: deny %v", deny)
	}
	ok(t)(ServerTools(ArtifactsServerName, nil))

	// the user's whole permissions block gone is the same
	ok(t)(RemoveServer(ArtifactsServerName))
	ok(t)(InstallServer(ArtifactsServerName, nil, []string{"claude"}))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Fatalf("added again: deny %v", deny)
	}
	write(t, settings, `{}`)
	ok(t)(Sync())
	if got := read(t, settings); got != `{}` {
		t.Fatalf("written back:\n%s", got)
	}
}

// With the router on, Claude Code reaches Magpie Artifacts only through
// the router's entry: one that couldn't be written gives it nothing, and
// no deny.
func TestArtifactsDenyNeedsTheRoutersEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder's mode doesn't stop a write on Windows")
	}
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{"permissions": {"deny": ["Bash(rm:*)"]}}`)
	ok(t)(SetMCPHub(true))
	// ~/.claude.json, where Claude Code's servers go, can't be written
	if err := os.Chmod(h, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h, 0o755) })
	res, err := InstallServer(ArtifactsServerName, nil, []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Problems, func(p Problem) bool { return p.Agent == "claude" }) {
		t.Fatalf("set up: the router's entry was written: %+v", res.Problems)
	}
	if es, _ := targetByID("claude").MCP.entries(); es[HubEntry] != nil {
		t.Fatalf("set up: Claude Code has %s", HubEntry)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)"}) {
		t.Errorf("Claude Code given nothing, its own Artifact left out: %v", deny)
	}

	// written once it can be, and the deny with it
	os.Chmod(h, 0o755)
	ok(t)(Sync())
	if es, _ := targetByID("claude").MCP.entries(); es[HubEntry] == nil {
		t.Fatalf("Claude Code has no %s", HubEntry)
	}
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", claudeArtifactTool}) {
		t.Errorf("given through the router: deny %v", deny)
	}
}

// Every way the server isn't given to Claude Code, with the router on and
// off, writes no deny, and takes magpie's back: never added, given to
// another agent only, turned off, unticked, removed, and the 11-24 one
// taken back.
func TestArtifactsDenyOnlyWhileGiven(t *testing.T) {
	for _, hub := range []bool{false, true} {
		name := "direct"
		if hub {
			name = "router"
		}
		t.Run(name, func(t *testing.T) {
			h := sandbox(t)
			settings := filepath.Join(h, ".claude", "settings.json")
			const mine = `{"permissions": {"defaultMode": "auto"}}`
			write(t, settings, mine)
			ok(t)(SetMCPHub(hub))
			none := func(when string) {
				t.Helper()
				if deny, _ := claudeDeny(t, settings); len(deny) != 0 {
					t.Fatalf("%s: deny %v", when, deny)
				}
			}
			denied := func(when string) {
				t.Helper()
				if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{claudeArtifactTool}) {
					t.Fatalf("%s: deny %v", when, deny)
				}
			}

			// never added, with another server Claude Code has
			ok(t)(Sync())
			ok(t)(SaveServer("", Server{Name: "tools", Transport: "stdio", Command: "true", Agents: []string{"claude", "codex"}}))
			none("never added")
			// given to Codex only
			ok(t)(InstallServer(ArtifactsServerName, nil, []string{"codex"}))
			none("given to Codex only")

			ok(t)(ServerAgents(ArtifactsServerName, []string{"claude", "codex"}))
			denied("ticked on Claude Code")
			ok(t)(ServerAgents(ArtifactsServerName, []string{"codex"}))
			none("unticked")
			if got := read(t, settings); got != mine {
				t.Errorf("unticked, settings.json isn't as it was:\n%s", got)
			}

			ok(t)(ServerAgents(ArtifactsServerName, []string{"claude"}))
			denied("ticked again")
			ok(t)(ServerOn(ArtifactsServerName, false))
			none("turned off")
			ok(t)(Sync())
			none("turned off, at the next start")
			ok(t)(ServerOn(ArtifactsServerName, true))
			denied("turned on")
			ok(t)(RemoveServer(ArtifactsServerName))
			none("removed")
			ok(t)(Sync())
			none("removed, at the next start")

			// the one the 11-24 start added, taken back by the next
			givenUnasked(t)
			denied("added unasked on 11-24")
			ok(t)(Sync())
			none("taken back")
			ok(t)(Sync())
			none("taken back, at the next start")
			if got := read(t, settings); got != mine {
				t.Errorf("settings.json isn't as it was:\n%s", got)
			}
		})
	}
}
