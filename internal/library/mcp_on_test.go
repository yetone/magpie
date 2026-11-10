package library

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Off, a server is given to no agent, no project and no app at the MCP
// router's address, and a process the router holds for it ends; its
// settings, env and agents are kept. On, it comes back as it was
// (swang20201 on X: a server in Library could only be removed).
func TestServerOnKeepsAndRestores(t *testing.T) {
	h := hubSandbox(t)
	fake := fakeStdio(t, "ok", map[string]string{"TOKEN": "secret"})
	fake.Agents = []string{"claude"}
	fake.DisabledTools = []string{"b"}
	ok(t)(SaveServer("", *fake))
	proj := filepath.Join(t.TempDir(), "app")
	write(t, filepath.Join(proj, ".mcp.json"), `{"mcpServers": {"team": {"command": "team-mcp"}}}`)
	ok(t)(AddProject(proj))
	ok(t)(ProjectServer(proj, fake.Name, []string{"claude"}))
	sub := reviewer
	sub.MCP, sub.Skills, sub.Agents = []string{fake.Name}, nil, []string{"claude"}
	ok(t)(SaveSubagent("", sub))
	ok(t)(Sync())

	claude := filepath.Join(h, ".claude.json")
	mcp := filepath.Join(proj, ".mcp.json")
	kept := func(when string) {
		t.Helper()
		l, err := load()
		if err != nil {
			t.Fatal(err)
		}
		s := l.server(fake.Name)
		if s == nil || !s.Hidden || len(s.Agents) != 0 || !slices.Equal(s.Paused, []string{"claude"}) ||
			s.Env["TOKEN"] != "secret" || !slices.Equal(s.DisabledTools, []string{"b"}) {
			t.Fatalf("%s: kept as %+v", when, s)
		}
	}
	subFile := filepath.Join(h, ".claude", "agents", "reviewer.md")
	inFiles := func() (user, project bool) {
		t.Helper()
		user = servers(t, claude, "mcpServers")[fake.Name] != nil
		if named := strings.Contains(read(t, subFile), `mcpServers: ["fake"]`); named != user {
			t.Fatalf("claude's file has it %v, the subagent names it %v:\n%s", user, named, read(t, subFile))
		}
		return user, servers(t, mcp, "mcpServers")[fake.Name] != nil
	}
	if u, p := inFiles(); !u || !p {
		t.Fatalf("to begin with: in claude's file %v, the project's %v", u, p)
	}

	ok(t)(ServerOn(fake.Name, false))
	for _, when := range []string{"off", "off twice", "synced"} {
		switch when {
		case "off twice":
			ok(t)(ServerOn(fake.Name, false))
		case "synced":
			ok(t)(Sync())
		}
		if u, p := inFiles(); u || p {
			t.Fatalf("%s: in claude's file %v, the project's %v", when, u, p)
		}
		if servers(t, mcp, "mcpServers")["team"] == nil {
			t.Fatalf("%s: the project's own server went", when)
		}
		kept(when)
	}

	ok(t)(ServerOn(fake.Name, true))
	if u, p := inFiles(); !u || !p {
		t.Fatalf("on again: in claude's file %v, the project's %v", u, p)
	}
	env, _ := servers(t, claude, "mcpServers")[fake.Name].(map[string]any)["env"].(map[string]any)
	if l, _ := load(); env["TOKEN"] != "secret" || l.server(fake.Name).Hidden || l.server(fake.Name).Paused != nil ||
		!slices.Equal(l.server(fake.Name).Agents, []string{"claude"}) {
		t.Fatalf("on again: env %v, %+v", env, l.server(fake.Name))
	}

	// at the MCP router's address, the way Docker's gateway mode gives it
	ok(t)(SetMCPHub(true))
	ctx := context.Background()
	hub := theHub{}
	listed := func(agentID string) bool {
		return slices.Contains(hubNames(hub.Tools(ctx, agentID)), "fake/a")
	}
	if !listed("claude") || !listed("") {
		t.Fatal("the router doesn't give it to begin with")
	}
	if _, err := hub.Call(ctx, "", fake.Name, "a", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	hubPool.Lock()
	b := hubPool.m[fake.Name]
	hubPool.Unlock()
	if b == nil {
		t.Fatal("no server held after a call")
	}
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()

	ok(t)(ServerOn(fake.Name, false))
	if listed("claude") || listed("") {
		t.Fatal("off, the router still lists it")
	}
	if _, err := hub.Call(ctx, "", fake.Name, "a", json.RawMessage(`{}`)); err == nil {
		t.Fatal("off, an app at the router's address ran it")
	}
	kept("off at the router")
	hubPrune()
	ended := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if !c.alive() {
			ended = true
			break
		}
	}
	if !ended {
		t.Fatal("the router's process for a server turned off still runs")
	}
	ok(t)(ServerOn(fake.Name, true))
	if !listed("claude") || !listed("") {
		t.Fatal("on again, the router doesn't give it")
	}

	// one ticked for no agent, which the router alone gives other apps
	ok(t)(ServerAgents(fake.Name, nil))
	if !listed("") {
		t.Fatal("a server ticked for no agent isn't given to other apps")
	}
	ok(t)(ServerOn(fake.Name, false))
	if listed("") {
		t.Fatal("off, an app at the router's address still has it")
	}
	ok(t)(ServerOn(fake.Name, true))
	if l, _ := load(); !listed("") || len(l.server(fake.Name).Agents) != 0 {
		t.Fatalf("on again with no agent: %+v", l.server(fake.Name))
	}
	if _, err := ServerOn("nope", false); err == nil {
		t.Fatal("no error for a server that isn't there")
	}
}
