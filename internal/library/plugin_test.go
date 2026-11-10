package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlugin writes a Claude Code plugin's folder: a skill, a subagent
// that names it, a hook on shell tools, a Stop hook, an MCP server, a
// slash command and a hook that isn't a command.
func writePlugin(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, ".claude-plugin/plugin.json"), `{"name":"acme-kit","version":"1.2.0","description":"Acme's review kit"}`)
	write(t, filepath.Join(dir, "skills/tdd/SKILL.md"), "---\nname: tdd\ndescription: Test first\n---\nWrite the test first.\n")
	write(t, filepath.Join(dir, "agents/reviewer.md"), "---\nname: reviewer\ndescription: Reviews a diff\nskills: tdd\n---\nYou review code.\n")
	write(t, filepath.Join(dir, "commands/ship.md"), "Ship it\n")
	write(t, filepath.Join(dir, "hooks/hooks.json"), `{"hooks":{
	  "PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/lint.sh","timeout":20}]}],
	  "Stop":[{"hooks":[{"type":"command","command":"echo done"}]}],
	  "PreCompact":[{"hooks":[{"type":"command","command":"echo compacting"},{"type":"prompt","prompt":"x"}]}]}}`)
	write(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"acme-db":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/db","args":["--data","${CLAUDE_PLUGIN_DATA}"],"env":{"K":"v"}}}}`)
}

func pluginAgents(t *testing.T, h string) {
	t.Helper()
	write(t, filepath.Join(h, ".claude/settings.json"), "{}\n")
	write(t, filepath.Join(h, ".codex/config.toml"), "")
	write(t, filepath.Join(h, ".config/opencode/opencode.json"), "{}\n")
}

func TestPluginImportCarryOnOffRemove(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)

	p, err := ProbePlugins(src)
	if err != nil || len(p.Candidates) != 1 {
		t.Fatalf("probe: %v %+v", err, p)
	}
	c := p.Candidates[0]
	if c.Name != "acme-kit" || c.Skills != 1 || c.MCP != 1 || c.Hooks != 3 || c.Subagents != 1 || len(c.Left) != 2 {
		t.Fatalf("candidate: %+v", c)
	}

	res := ok(t)(InstallPlugins(src, nil, []string{"claude", "codex"}))
	if len(res.Installed) != 1 || res.Installed[0] != "acme-kit" {
		t.Fatalf("installed: %+v", res.Installed)
	}
	root := filepath.Join(Dir(), "plugins", "acme-kit")
	data := filepath.Join(Dir(), "plugin-data", "acme-kit")

	// every part is an ordinary item of its page, marked with the plugin
	v := View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].Name != "acme-kit" || v.Plugins[0].Version != "1.2.0" {
		t.Fatalf("plugins: %+v", v.Plugins)
	}
	if len(v.Skills) != 1 || v.Skills[0].Bundle != "acme-kit" {
		t.Fatalf("skills: %+v", v.Skills)
	}

	// carried, each part to the agents that take it
	if got := read(t, filepath.Join(h, ".claude/agents/reviewer.md")); !strings.Contains(got, "You review code.") || !strings.Contains(got, `skills: ["tdd"]`) {
		t.Errorf("claude subagent:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(h, ".codex/agents/reviewer.toml")); err != nil {
		t.Errorf("codex subagent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h, ".claude/skills/tdd")); err != nil {
		t.Errorf("claude skill: %v", err)
	}
	settings := read(t, filepath.Join(h, ".claude/settings.json"))
	for _, want := range []string{filepath.Join(root, "scripts/lint.sh"), "Bash", "echo done"} {
		if !strings.Contains(settings, want) {
			t.Errorf("claude settings lack %q:\n%s", want, settings)
		}
	}
	if strings.Contains(settings, "compacting") == false {
		t.Errorf("claude's own PreCompact hook missing:\n%s", settings)
	}
	if codexHooks := read(t, filepath.Join(h, ".codex/hooks.json")); strings.Contains(codexHooks, "compacting") {
		t.Errorf("codex was given claude's own event:\n%s", codexHooks)
	}
	cj := read(t, filepath.Join(h, ".claude.json"))
	if !strings.Contains(cj, filepath.Join(root, "bin/db")) || !strings.Contains(cj, data) {
		t.Errorf("claude's server lacks the plugin's folders:\n%s", cj)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".codex/config.toml")), "acme-db") {
		t.Errorf("codex lacks the server")
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("data folder: %v", err)
	}

	// off: every part leaves every agent, and stays in the library
	ok(t)(PluginOn("acme-kit", false))
	for _, f := range []string{".claude/agents/reviewer.md", ".claude/skills/tdd", ".codex/agents/reviewer.toml"} {
		if _, err := os.Lstat(filepath.Join(h, f)); err == nil {
			t.Errorf("%s stays while the plugin is off", f)
		}
	}
	if s := read(t, filepath.Join(h, ".claude/settings.json")); strings.Contains(s, "echo done") || strings.Contains(s, "compacting") {
		t.Errorf("hooks stay while off:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".claude.json")); strings.Contains(s, "acme-db") {
		t.Errorf("server stays while off:\n%s", s)
	}
	if v := View0(t); len(v.Skills) != 1 || len(v.Servers) != 1 || len(v.Subagents) != 1 || !v.Plugins[0].Off {
		t.Fatalf("off keeps the parts in the library: %+v", v)
	}

	// on again, and for one agent only
	ok(t)(PluginAgents("acme-kit", []string{"codex"}))
	ok(t)(PluginOn("acme-kit", true))
	if _, err := os.Stat(filepath.Join(h, ".claude/agents/reviewer.md")); err == nil {
		t.Errorf("claude got the subagent it wasn't ticked for")
	}
	if _, err := os.Stat(filepath.Join(h, ".codex/agents/reviewer.toml")); err != nil {
		t.Errorf("codex: %v", err)
	}

	// removed: nothing of it is left, in an agent or the library
	ok(t)(RemovePlugin("acme-kit"))
	v = View0(t)
	if len(v.Plugins)+len(v.Skills)+len(v.Servers)+len(v.Hooks)+len(v.Subagents) != 0 {
		t.Errorf("left after remove: %+v", v)
	}
	if _, err := os.Stat(root); err == nil {
		t.Errorf("its folder stays")
	}
	if _, err := os.Stat(filepath.Join(h, ".codex/agents/reviewer.toml")); err == nil {
		t.Errorf("codex's subagent stays")
	}
}

// The user's own items by the same names are never written over, and a
// plugin never takes them for its own.
func TestPluginNameClashKeepsTheUsersOwn(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	ok(t)(SaveServer("", Server{Name: "acme-db", Transport: "stdio", Command: "mine", Agents: []string{"claude"}}))
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	res := ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	if len(res.Skipped) == 0 {
		t.Fatalf("nothing said about the clash")
	}
	v := View0(t)
	var own, theirs int
	for _, s := range v.Servers {
		if s.Name == "acme-db" && s.Bundle == "" && s.Command == "mine" {
			own++
		}
		if s.Name == "acme-kit-acme-db" && s.Bundle == "acme-kit" {
			theirs++
		}
	}
	if own != 1 || theirs != 1 {
		t.Fatalf("servers: %+v", v.Servers)
	}
	ok(t)(RemovePlugin("acme-kit"))
	if v := View0(t); len(v.Servers) != 1 || v.Servers[0].Command != "mine" {
		t.Errorf("remove took the user's own: %+v", v.Servers)
	}
	_ = h
}

// A marketplace lists several plugins, each a folder in it; one picked is
// installed, and an update reads it again from the same place keeping
// where it was given.
func TestPluginMarketplaceAndUpdate(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	mk := filepath.Join(t.TempDir(), "market")
	write(t, filepath.Join(mk, ".claude-plugin/marketplace.json"), `{"name":"m","plugins":[
	  {"name":"one","source":"./plugins/one","description":"First"},
	  {"name":"two","source":"./plugins/two"},
	  {"name":"far","source":{"source":"github","repo":"o/r"}},
	  {"name":"out","source":"../../etc"}]}`)
	write(t, filepath.Join(mk, "plugins/one/skills/a/SKILL.md"), "---\nname: a\ndescription: A\n---\nA\n")
	write(t, filepath.Join(mk, "plugins/two/.mcp.json"), `{"two-srv":{"url":"https://example.com/mcp"}}`)

	p, err := ProbePlugins(mk)
	if err != nil || len(p.Candidates) != 4 || p.Marketplace != "m" {
		t.Fatalf("probe: %v %+v", err, p)
	}
	if !p.Candidates[2].Remote || len(p.Candidates[3].Left) == 0 {
		t.Fatalf("candidates: %+v", p.Candidates)
	}
	if _, err := InstallPlugins(mk, nil, nil); err == nil {
		t.Fatalf("a marketplace with no pick installs nothing")
	}
	ok(t)(InstallPlugins(mk, []string{"one", "two"}, []string{"claude"}))
	v := View0(t)
	if len(v.Plugins) != 2 || v.Plugins[0].Description != "First" || len(v.Skills) != 1 || len(v.Servers) != 1 || v.Servers[0].Transport != "http" {
		t.Fatalf("%+v", v)
	}
	ok(t)(PluginOn("one", false))
	write(t, filepath.Join(mk, "plugins/one/skills/b/SKILL.md"), "---\nname: b\ndescription: B\n---\nB\n")
	ok(t)(UpdatePlugin("one"))
	v = View0(t)
	if len(v.Skills) != 2 || !v.Plugins[0].Off || len(v.Plugins[0].Agents) != 1 {
		t.Fatalf("update keeps off and agents: %+v", v.Plugins[0])
	}
	for _, s := range v.Skills {
		if len(s.Agents) != 0 {
			t.Errorf("%s was given to %v while off", s.Name, s.Agents)
		}
	}
}

func TestPluginRefusesWhatIsNone(t *testing.T) {
	sandbox(t)
	empty := t.TempDir()
	if _, err := ProbePlugins(empty); err == nil {
		t.Errorf("an empty folder is no plugin")
	}
	if _, err := ProbePlugins("-oProxyCommand=x"); err == nil {
		t.Errorf("an option isn't an address")
	}
	cmds := t.TempDir()
	write(t, filepath.Join(cmds, "commands/x.md"), "x")
	if _, err := InstallPlugins(cmds, nil, nil); err == nil {
		t.Errorf("a plugin of slash commands only has nothing to carry")
	}
}

// View0 is the page's view of the library.
func View0(t *testing.T) *View {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
