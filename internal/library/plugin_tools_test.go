package library

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
)

// fakePlugins has the plugins installed be specs (none on when off), each
// noted giving search and crawl, and the gateway's plugin tools be theirs,
// recording each call run.
type fakePlugins struct {
	specs []string
	off   bool
	err   error
	ran   []string
}

func fakePluginTools(t *testing.T, specs ...string) *fakePlugins {
	t.Helper()
	f := &fakePlugins{specs: specs}
	oldI, oldN, oldT, oldR := pluginsInstalled, toolsNoted, pluginToolsOf, runPluginToolOf
	t.Cleanup(func() { pluginsInstalled, toolsNoted, pluginToolsOf, runPluginToolOf = oldI, oldN, oldT, oldR })
	pluginsInstalled = func() (plugin.List, error) {
		var l plugin.List
		for _, s := range f.specs {
			l.Plugins = append(l.Plugins, plugin.Entry{Spec: s, Off: f.off})
		}
		return l, f.err
	}
	toolsNoted = func() map[string][]string {
		m := map[string][]string{}
		for _, s := range f.specs {
			m[s] = []string{"crawl", "search"}
		}
		return m
	}
	pluginToolsOf = func(_ context.Context, name string) ([]plugin.Tool, error) {
		for _, s := range f.specs {
			if plugin.Name(s) == name && !f.off {
				return []plugin.Tool{
					{Name: "search", Description: "Search the web.", Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
					{Name: "crawl", Description: "A page."},
				}, nil
			}
		}
		return nil, nil
	}
	runPluginToolOf = func(_ context.Context, name, tool string, args json.RawMessage, agentID string) (string, error) {
		f.ran = append(f.ran, name+" "+tool+" "+string(args)+" "+agentID)
		return "found " + string(args), nil
	}
	return f
}

const search1api = "@magpie-community/tools-search1api@0.1.1"

// A plugin that gives tools (Search1API, chenxiang0010 on Discord) is a
// Library server of its own once its tools are noted: on, with the hub on,
// for the agents given the hub's entry, its tools in their hub as
// search1api's, run in the plugin for the calling agent, and switched on
// and off per agent and per tool like any server's. With the hub off an
// agent is given the gateway's address of that plugin's tools.
func TestPluginToolsInHub(t *testing.T) {
	h := hubSandbox(t)
	f := fakePluginTools(t, search1api)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))
	ok(t)(Sync())

	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.server("search1api")
	if s == nil {
		t.Fatalf("no server for the plugin: %+v", l.MCP)
	}
	if s.Plugin != "@magpie-community/tools-search1api" || !s.Local || !slices.Equal(s.Agents, []string{"claude"}) ||
		s.URL != strings.TrimRight(gateway.URL(), "/")+gateway.ToolsMCPOne+"search1api" {
		t.Fatalf("the plugin's server: %+v", s)
	}

	ctx := context.Background()
	hub := theHub{}
	got := hubNames(hub.Tools(ctx, "claude"))
	if !slices.Contains(got, "search1api/search") || !slices.Contains(got, "search1api/crawl") {
		t.Fatalf("claude's hub tools %v, want search1api's", got)
	}
	for _, x := range hub.Tools(ctx, "claude") {
		if x.Tool == "search" && !strings.Contains(string(x.Spec["inputSchema"]), "query") {
			t.Errorf("search's schema: %s", x.Spec["inputSchema"])
		}
	}
	res, err := hub.Call(ctx, "claude", "search1api", "search", json.RawMessage(`{"query":"magpie"}`))
	if err != nil || hubText(t, res) != `found {"query":"magpie"}` {
		t.Fatalf("search: %s %v", res, err)
	}
	if want := `@magpie-community/tools-search1api search {"query":"magpie"} claude`; len(f.ran) != 1 || f.ran[0] != want {
		t.Errorf("ran %q, want %q", f.ran, want)
	}
	if _, err := hub.Call(ctx, "codex", "search1api", "search", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "isn't switched on for codex") {
		t.Errorf("a plugin not switched on for codex ran: %v", err)
	}
	// a tool turned off is neither listed nor run
	ok(t)(change(func(l *Library) error { l.server("search1api").DisabledTools = []string{"crawl"}; return nil }))
	if got := hubNames(hub.Tools(ctx, "claude")); slices.Contains(got, "search1api/crawl") {
		t.Errorf("crawl, turned off, listed: %v", got)
	}
	if _, err := hub.Call(ctx, "claude", "search1api", "crawl", json.RawMessage(`{}`)); err == nil {
		t.Error("crawl, turned off, ran")
	}

	// edited in the Library's editor, which doesn't send it, it stays the
	// plugin's
	ed := *l.server("search1api")
	ed.Plugin, ed.Local, ed.Agents = "", false, []string{"claude", "codex"}
	ok(t)(SaveServer("search1api", ed))
	if l, _ := load(); l.server("search1api").Plugin == "" || !l.server("search1api").Local {
		t.Fatalf("edited, no longer the plugin's: %+v", l.server("search1api"))
	}

	// the hub off, claude is given the address of the plugin's tools
	ok(t)(SetMCPHub(false))
	e, err := targetByID("claude").MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := e["search1api"]["url"].(string); u != strings.TrimRight(gateway.URL(), "/")+gateway.ToolsMCPOne+"search1api" {
		t.Errorf("claude's search1api entry: %v\n%s", e["search1api"], read(t, filepath.Join(h, ".claude.json")))
	}
}

// The server is offered once: removed by the user, it isn't added again
// until the plugin is removed and added again. A plugin removed takes its
// server with it; one turned off keeps it; plugins.json unreadable takes
// nothing out and adds nothing.
func TestPluginToolsServerComesAndGoes(t *testing.T) {
	hubSandbox(t)
	f := fakePluginTools(t, search1api)
	has := func() bool {
		t.Helper()
		l, err := load()
		if err != nil {
			t.Fatal(err)
		}
		return l.server("search1api") != nil
	}
	ok(t)(Sync())
	if !has() {
		t.Fatal("no server for the plugin")
	}
	l, _ := load()
	if a := l.server("search1api").Agents; len(a) != 0 {
		t.Errorf("with the hub off, on for %v, want none", a)
	}
	ok(t)(RemoveServer("search1api"))
	ok(t)(Sync())
	if has() {
		t.Fatal("a server the user removed came back")
	}
	f.specs = nil
	ok(t)(Sync())
	f.specs = []string{"@magpie-community/tools-search1api@0.1.2"}
	ok(t)(Sync())
	if !has() {
		t.Fatal("the plugin added again, no server")
	}
	f.off = true
	ok(t)(Sync())
	if !has() {
		t.Fatal("the plugin turned off took its server")
	}
	f.off = false
	f.err = errors.New("unreadable")
	f.specs = nil
	ok(t)(Sync())
	if !has() {
		t.Fatal("plugins.json unreadable took the server out")
	}
	f.err = nil
	ok(t)(Sync())
	if has() {
		t.Fatal("the plugin removed kept its server")
	}
	// a name a server of the user's has already
	ok(t)(SaveServer("", Server{Name: "search1api", Transport: "http", URL: "https://example.com/mcp"}))
	f.specs = []string{search1api}
	ok(t)(Sync())
	l, _ = load()
	if s := l.server("search1api-plugin"); s == nil || s.Plugin == "" || l.server("search1api").Plugin != "" {
		t.Fatalf("beside the user's search1api: %+v", l.MCP)
	}
}

// An agent that reaches the gateway at another address (a WSL distro under
// NAT) is given the plugin's tools at that address.
func TestPluginToolsServerThroughAnotherAddress(t *testing.T) {
	sandbox(t)
	s := &Server{Name: "search1api", Transport: "http", URL: "http://127.0.0.1:3425" + gateway.ToolsMCPOne + "search1api", Plugin: "@magpie-community/tools-search1api", Local: true}
	c := through(s, "http://172.20.0.1:3425")
	if c.URL != "http://172.20.0.1:3425"+gateway.ToolsMCPOne+"search1api" {
		t.Errorf("through another address: %s", c.URL)
	}
	if s.URL != "http://127.0.0.1:3425"+gateway.ToolsMCPOne+"search1api" {
		t.Errorf("the stored server changed: %s", s.URL)
	}
}

// A plugin's tools hidden from every agent on the Plugins page
// (chengming0v0 on Discord: MiniMax's tools, the plugin kept for its
// providers): no agent's hub lists or runs them, nor an app's at the
// router's address; hidden they stay through the plugin updated, turned
// off and on, and the server edited; shown again, the agents that had
// them get them back. An agent ticked in the Library shows them too.
func TestPluginToolsHidden(t *testing.T) {
	hubSandbox(t)
	f := fakePluginTools(t, search1api)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))
	ok(t)(Sync())
	const name = "@magpie-community/tools-search1api"
	ctx := context.Background()
	hub := theHub{}
	listed := func(agentID string) bool {
		return slices.Contains(hubNames(hub.Tools(ctx, agentID)), "search1api/search")
	}
	if !listed("claude") || !listed("") || !PluginServers()[name].On {
		t.Fatal("the plugin's tools aren't given to begin with")
	}

	ok(t)(PluginServerOn(name, false))
	hidden := func(when string) {
		t.Helper()
		l, err := load()
		if err != nil {
			t.Fatal(err)
		}
		s := l.server("search1api")
		if s == nil || !s.Hidden || len(s.Agents) != 0 || !slices.Equal(s.Paused, []string{"claude"}) {
			t.Fatalf("%s: %+v", when, s)
		}
		if listed("claude") || listed("") || PluginServers()[name].On {
			t.Fatalf("%s: still listed", when)
		}
		if _, err := hub.Call(ctx, "", "search1api", "search", json.RawMessage(`{}`)); err == nil {
			t.Fatalf("%s: an app at the router's address ran it", when)
		}
	}
	hidden("hidden")
	f.specs = []string{"@magpie-community/tools-search1api@0.1.2"}
	ok(t)(Sync())
	hidden("the plugin updated")
	f.off = true
	ok(t)(Sync())
	f.off = false
	ok(t)(Sync())
	hidden("the plugin off and on")
	l, _ := load()
	ed := *l.server("search1api")
	ed.Hidden, ed.Paused, ed.Note = false, nil, "videos"
	ok(t)(SaveServer("search1api", ed))
	hidden("edited")
	ok(t)(PluginServerOn(name, false))
	hidden("hidden twice")
	ok(t)(EveryServerAgents([]string{"claude"}, true))
	hidden("every server given to claude")
	if l, _ := load(); !slices.Contains(l.server("fake").Agents, "claude") {
		t.Fatal("every server given to claude left out another server")
	}

	ok(t)(PluginServerOn(name, true))
	if l, _ := load(); !slices.Equal(l.server("search1api").Agents, []string{"claude"}) || l.server("search1api").Hidden || !listed("claude") || !listed("") {
		t.Fatalf("shown again: %+v", l.server("search1api"))
	}

	// ticked for an agent in the Library, it is shown
	ok(t)(PluginServerOn(name, false))
	ok(t)(ServerAgents("search1api", []string{"codex"}))
	if l, _ := load(); l.server("search1api").Hidden || l.server("search1api").Paused != nil || !listed("") {
		t.Fatalf("ticked in the Library, still hidden: %+v", l.server("search1api"))
	}

	// none kept: shown, it goes to the agents of the router's entry
	ok(t)(ServerAgents("search1api", []string{}))
	ok(t)(PluginServerOn(name, true))
	if l, _ := load(); !slices.Equal(l.server("search1api").Agents, []string{"claude"}) {
		t.Fatalf("shown with none kept, the router on: %v", l.server("search1api").Agents)
	}
	if _, err := PluginServerOn("@x/none", true); err == nil {
		t.Error("a plugin with no server shown")
	}
}
