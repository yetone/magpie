package library

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// noDsh is a sandbox with the router on and no DeepSeek Harness: a magpie
// in Docker, as bitxeno303 on X ran it, with dsh on their own computer.
func noDsh(t *testing.T) string {
	t.Helper()
	h := hubSandbox(t)
	if err := os.RemoveAll(filepath.Join(h, ".dsh")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"dsh", "dsh.exe"} {
		if err := os.Remove(filepath.Join(h, "bin", f)); err != nil {
			t.Fatal(err)
		}
	}
	clear(hereSeen)
	t.Cleanup(func() { clear(hereSeen) })
	if targetByID("dsh") != nil {
		t.Fatal("dsh is still an agent here")
	}
	ok(t)(SetMCPHub(true))
	return h
}

// An agent on another computer connecting to the router at its own
// address is given, the first time, every server on that goes through the
// router, and is listed with the agents that take MCP servers; its tools
// are then the hub's for it, and the CLI can tick a server for it.
func TestRemoteAgentGivenTheRoutersServers(t *testing.T) {
	noDsh(t)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SaveServer("", Server{Name: "direct", Transport: "stdio", Command: "x", Hub: "direct", Agents: []string{"claude"}}))
	ok(t)(SaveServer("", Server{Name: "off", Transport: "stdio", Command: "x", Hidden: true}))
	h := theHub{}
	ctx := context.Background()
	if got := h.Tools(ctx, "dsh"); len(got) != 0 {
		t.Fatalf("dsh lists %v before it connected", hubNames(got))
	}

	h.Met("dsh")
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.RemoteAgents, []string{"dsh"}) {
		t.Fatalf("remote agents %v", l.RemoteAgents)
	}
	for _, s := range l.MCP {
		if want := s.Name == "fake"; slices.Contains(s.Agents, "dsh") != want {
			t.Errorf("%s given to dsh: %v, want %v (only what goes through the router, and is on)", s.Name, s.Agents, want)
		}
	}
	if got, want := hubNames(h.Tools(ctx, "dsh")), []string{"fake/a", "fake/b", "fake/c"}; !slices.Equal(got, want) {
		t.Fatalf("dsh's tools %v, want %v", got, want)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(v.Agents, func(a AgentView) bool { return a.ID == "dsh" })
	if i < 0 {
		t.Fatalf("dsh isn't listed: %+v", v.Agents)
	}
	if a := v.Agents[i]; !a.Remote || a.Gone || a.MCP != gateway.HubMCPPath+"dsh" || a.Hub != "on" || a.Name == "" {
		t.Fatalf("dsh listed as %+v", a)
	}
	// the CLI ticks a new server for it, as the page's chips do
	if id, err := Takes("dsh", "mcp"); err != nil || id != "dsh" {
		t.Fatalf("Takes(dsh, mcp) = %q, %v", id, err)
	}
	ok(t)(SaveServer("", Server{Name: "later", Transport: "stdio", Command: "x"}))
	ok(t)(EveryServerAgents([]string{"dsh"}, true))
	mu.Lock()
	l, _ = load()
	mu.Unlock()
	if s := l.server("later"); !slices.Contains(s.Agents, "dsh") {
		t.Fatalf("every server for dsh left out later: %v", s.Agents)
	}
	if s := l.server("direct"); slices.Contains(s.Agents, "dsh") {
		t.Fatalf("every server for dsh gave it direct, which the router doesn't serve: %v", s.Agents)
	}
}

// The servers go to it the first time only: one unticked for it after
// stays so when it connects again, and one ticked for it before (a
// synced library's) is all it is given.
func TestRemoteAgentKeepsWhatTheUserTicked(t *testing.T) {
	noDsh(t)
	ok(t)(SaveServer("", Server{Name: "a", Transport: "stdio", Command: "x", Agents: []string{"claude"}}))
	ok(t)(SaveServer("", Server{Name: "b", Transport: "stdio", Command: "x", Agents: []string{"claude"}}))
	theHub{}.Met("dsh")
	ok(t)(ServerAgents("a", []string{"claude"}))
	theHub{}.Met("dsh")
	mu.Lock()
	l, _ := load()
	mu.Unlock()
	if s := l.server("a"); slices.Contains(s.Agents, "dsh") {
		t.Fatalf("a, unticked for dsh, was given it again: %v", s.Agents)
	}

	l2 := &Library{MCP: []*Server{{Name: "a", Agents: []string{"dsh"}}, {Name: "b"}}}
	l2.meetRemote("dsh")
	if !slices.Equal(l2.RemoteAgents, []string{"dsh"}) || len(l2.MCP[1].Agents) != 0 {
		t.Fatalf("with a ticked for it already: remote %v, b's agents %v", l2.RemoteAgents, l2.MCP[1].Agents)
	}
}

// Only an agent magpie knows by its very id, that isn't on this computer,
// with the router on, is met: an agent here, an unknown name, an alias, a
// WSL agent's or a profile's id, and any agent while the router is off are
// recorded as nothing.
func TestRemoteAgentOnlyAKnownOneNotHere(t *testing.T) {
	noDsh(t)
	ok(t)(SaveServer("", Server{Name: "a", Transport: "stdio", Command: "x", Agents: []string{"codex"}}))
	for _, id := range []string{"claude", "nope", "deepseek-harness", "codex@wsl:Ubuntu", "hermes#coder", "../x"} {
		theHub{}.Met(id)
	}
	ok(t)(SetMCPHub(false))
	theHub{}.Met("dsh")
	mu.Lock()
	l, _ := load()
	mu.Unlock()
	if len(l.RemoteAgents) != 0 || !slices.Equal(l.server("a").Agents, []string{"codex"}) {
		t.Fatalf("remote agents %v, a's agents %v", l.RemoteAgents, l.server("a").Agents)
	}
}
