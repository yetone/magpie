package davsync

import (
	"context"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/settings"
)

// hereAgents sets the agents whose picks stay on this computer, as the
// Agents row's This computer only does (POST /api/agents/here).
func hereAgents(t *testing.T, ids ...string) {
	t.Helper()
	s, err := settings.LoadChecked()
	if err != nil {
		t.Fatal(err)
	}
	s.AgentsHere = ids
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// An agent's picks kept to one computer, as an MCP server can be
// (chenxiang0010 on Discord: Codex on two computers, each with its own
// model). Turning it on changes nothing anywhere. From then on that
// computer's Codex picks, model and effort alike, neither go up nor take
// the others' in, by sync, a Restore from the server or a backup file;
// every other agent's picks still sync both ways. Turned off, the next
// pick here reaches the others again.
func TestAgentHereThroughSync(t *testing.T) {
	f := newGithubFixture(t)
	ctx := context.Background()
	c := githubConfig()
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		if st := Status(); st.Notice != nil || st.Error != "" {
			t.Fatalf("a sync that replaced nothing: %+v %q", st.Notice, st.Error)
		}
	}
	server := func(t *testing.T) map[string]string {
		t.Helper()
		b, err := backup.Open(f.objects[backupAt], c.Passphrase)
		if err != nil {
			t.Fatal(err)
		}
		return b.Agents
	}
	codexIs := func(t *testing.T, at, model, effort string) {
		t.Helper()
		if m, e := codexHere(); m != model || e != effort {
			t.Fatalf("%s: Codex %q %q, want %q %q", at, m, e, model, effort)
		}
	}
	a, b := newComputer(t), newComputer(t)

	a.use(t)
	codexPick(t, "gpt-a1", "low")
	if m, _ := codexHere(); m != "gpt-a1" {
		t.Skipf("Codex isn't seen here: %v", profile.Fields())
	}
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	now(t)

	b.use(t)
	codexPick(t, "gpt-b0", "low")
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Dismiss(); err != nil {
		t.Fatal(err)
	}
	codexIs(t, "B joining", "gpt-a1", "low")

	// B keeps Codex's picks here: nothing changes on either side
	hereAgents(t, "codex")
	now(t)
	codexIs(t, "B turning it on", "gpt-a1", "low")

	// B's picks stay on B
	codexPick(t, "gpt-b1", "high")
	now(t)
	if got := server(t); got["codex.model"] != "gpt-a1" || got["codex.effort"] != "low" {
		t.Fatalf("B's kept picks went up: %q %q", got["codex.model"], got["codex.effort"])
	}

	// A's picks go up, Claude Code's too
	a.use(t)
	now(t)
	codexIs(t, "A after B's kept pick", "gpt-a1", "low")
	codexPick(t, "gpt-a2", "medium")
	if _, err := profile.ApplyFields(map[string]string{"claude.model": "claude-from-a"}); err != nil {
		t.Fatal(err)
	}
	if profile.Fields()["claude.model"] != "claude-from-a" {
		t.Skipf("Claude Code isn't seen here: %v", profile.Fields())
	}
	now(t)
	if got := server(t); got["codex.model"] != "gpt-a2" || got["claude.model"] != "claude-from-a" {
		t.Fatalf("A's picks: server has %q %q", got["codex.model"], got["claude.model"])
	}

	// at B, Claude Code's comes in, Codex's stays B's
	b.use(t)
	now(t)
	codexIs(t, "B after A's pick", "gpt-b1", "high")
	if m := profile.Fields()["claude.model"]; m != "claude-from-a" {
		t.Fatalf("A's Claude Code model didn't reach B: %q", m)
	}
	if v := Status(); len(v.Own) != 0 {
		t.Fatalf("a kept agent is no Follow the others: %+v", v.Own)
	}

	// a Restore from the server and a backup file keep it too
	if _, err := Restore(ctx); err != nil {
		t.Fatal(err)
	}
	codexIs(t, "B after a Restore", "gpt-b1", "high")
	if _, err := backup.Restore(backup.Bundle{Agents: map[string]string{"codex.model": "gpt-file", "codex.effort": "low"}}, backup.Parts{Agents: true}); err != nil {
		t.Fatal(err)
	}
	codexIs(t, "B after a backup file", "gpt-b1", "high")

	// turned off, nothing changes until a pick, which goes up again
	hereAgents(t)
	now(t)
	codexIs(t, "B turning it off", "gpt-b1", "high")
	codexPick(t, "gpt-b2", "high")
	now(t)
	if got := server(t); got["codex.model"] != "gpt-b2" {
		t.Fatalf("B's pick after turning it off didn't go up: %q", got["codex.model"])
	}
	a.use(t)
	now(t)
	codexIs(t, "A after B turned it off", "gpt-b2", "high")
}
