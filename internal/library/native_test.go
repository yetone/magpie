package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chromeManifest is the shape of the real chrome plugin in Codex's cache
// (openai-bundled/chrome/26.1029.61003): the hooks are inline in
// plugin.json, as {"hooks": {event: [{"hooks": [..]}]}}, and the skills
// are a folder.
const chromeManifest = `{
  "name": "chrome",
  "hooks": {"hooks": {
    "Stop": [{"hooks": [{"type": "mcp_tool", "server": "node_repl", "tool": "turn_ended", "input": {"turn_id": "${turn_id}"}}]}],
    "Interrupt": [{"hooks": [{"type": "mcp_tool", "server": "node_repl", "tool": "turn_ended"}]}]
  }},
  "version": "26.1029.61003",
  "description": "Chrome automation",
  "skills": "./skills/"
}`

// appToolsManifest and its .mcp.json are codex-app-tools' (the servers
// are in a file the manifest names).
const appToolsManifest = `{"version": "26.1029.61003", "name": "codex-app-tools", "mcpServers": "./.mcp.json", "description": "Exposes Codex desktop app tools."}`
const appToolsMCP = `{"mcpServers": {"codex_app": {"command": "/x/launch_codex_app_tools_mcp", "args": ["./server.mjs"], "enabled": false, "env": {"A": "1"}}}}`

const skillMD = "---\nname: visualize\ndescription: \"Create visualizations\n  in conversation.\"\n---\n\n# Visualize\n"

func nativeCodexHome(t *testing.T) (home, cache string) {
	t.Helper()
	home = sandbox(t)
	cache = filepath.Join(home, ".codex", "plugins", "cache")
	write(t, filepath.Join(home, ".codex", "config.toml"), `[marketplaces.openai-bundled]
source = "/x/bundled-marketplaces/openai-bundled"

[plugins."chrome@openai-bundled"]
enabled = false

[plugins."codex-app-tools@openai-bundled"]
enabled = true
`)
	c := filepath.Join(cache, "openai-bundled")
	write(t, filepath.Join(c, "chrome", "26.1029.61003", ".codex-plugin", "plugin.json"), chromeManifest)
	write(t, filepath.Join(c, "chrome", "26.1029.61003", "skills", "chrome", "SKILL.md"), "---\nname: chrome\ndescription: Drive Chrome\n---\n")
	// "latest" is a copy of the version beside it
	write(t, filepath.Join(c, "chrome", "latest", ".codex-plugin", "plugin.json"), chromeManifest)
	write(t, filepath.Join(c, "chrome", "latest", "skills", "chrome", "SKILL.md"), "---\nname: chrome\ndescription: Drive Chrome\n---\n")
	write(t, filepath.Join(c, "codex-app-tools", "26.1029.61003", ".codex-plugin", "plugin.json"), appToolsManifest)
	write(t, filepath.Join(c, "codex-app-tools", "26.1029.61003", ".mcp.json"), appToolsMCP)
	write(t, filepath.Join(c, "visualize", "1.0.74", ".codex-plugin", "plugin.json"), `{"name": "visualize", "version": "1.0.74", "skills": "./skills/"}`)
	write(t, filepath.Join(c, "visualize", "1.0.74", "skills", "visualize", "SKILL.md"), skillMD)
	return home, cache
}

func nativeOf(t *testing.T, v []NativePlugin, agent, name string) NativePlugin {
	t.Helper()
	var got []NativePlugin
	for _, p := range v {
		if p.Agent == agent && p.Name == name {
			got = append(got, p)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want one %s %s, got %d in %+v", agent, name, len(got), v)
	}
	return got[0]
}

func partsOf(p NativePlugin, kind string) []NativePart {
	var out []NativePart
	for _, x := range p.Parts {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

// A plugin Codex installed from a marketplace is listed once, with its
// skills, MCP servers and hooks, read from where Codex reads them, and
// nothing is written.
func TestNativeCodexPlugins(t *testing.T) {
	home, _ := nativeCodexHome(t)
	before := dirDump(t, filepath.Join(home, ".codex"))
	v := ReadNative()

	chrome := nativeOf(t, v, "codex", "chrome")
	if chrome.Version != "26.1029.61003" || chrome.Scope != "user" || chrome.Loaded != "unknown" {
		t.Errorf("chrome: %+v", chrome)
	}
	if chrome.Enabled == nil || *chrome.Enabled {
		t.Errorf("config.toml turns chrome off: %v", chrome.Enabled)
	}
	if !strings.HasPrefix(chrome.Source, "openai-bundled") {
		t.Errorf("source %q", chrome.Source)
	}
	if s := partsOf(chrome, "skill"); len(s) != 1 || s[0].Name != "chrome" || s[0].Detail != "Drive Chrome" {
		t.Errorf("chrome skills: %+v", s)
	}
	h := partsOf(chrome, "hook")
	if len(h) != 2 || h[0].Name != "Interrupt" || h[1].Name != "Stop" || h[1].Detail != "mcp_tool node_repl/turn_ended" {
		t.Errorf("chrome hooks (inline in plugin.json): %+v", h)
	}

	app := nativeOf(t, v, "codex", "codex-app-tools")
	if m := partsOf(app, "mcp"); len(m) != 1 || m[0].Name != "codex_app" || m[0].Detail != "stdio" {
		t.Errorf("app tools' servers (a file plugin.json names): %+v", m)
	}
	if app.Enabled == nil || !*app.Enabled {
		t.Errorf("enabled: %v", app.Enabled)
	}

	viz := nativeOf(t, v, "codex", "visualize")
	if s := partsOf(viz, "skill"); len(s) != 1 || s[0].Name != "visualize" || s[0].Detail != "Create visualizations in conversation." {
		t.Errorf("visualize: %+v", s)
	}
	if viz.Enabled != nil {
		t.Errorf("config.toml says nothing of visualize: %v", *viz.Enabled)
	}
	if after := dirDump(t, filepath.Join(home, ".codex")); after != before {
		t.Errorf("reading changed Codex's folder:\n%s\n--\n%s", before, after)
	}
}

// What a manifest names stays inside the plugin: a path that leaves it
// reads nothing, a broken file is skipped, and the default files are read
// when the manifest names none.
func TestNativeCodexStaysInsidePlugin(t *testing.T) {
	home, cache := nativeCodexHome(t)
	secret := filepath.Join(home, "secret.json")
	write(t, secret, `{"mcpServers": {"stolen": {"command": "x"}}}`)
	root := filepath.Join(cache, "m", "evil", "1")
	write(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{"name": "evil", "mcpServers": "../../../../../secret.json", "hooks": "../../../../../secret.json", "skills": "../../../../../"}`)
	d := filepath.Join(cache, "m", "defaults", "1")
	write(t, filepath.Join(d, ".codex-plugin", "plugin.json"), `{"name": "defaults"}`)
	write(t, filepath.Join(d, ".mcp.json"), `{"web": {"url": "https://x.example/mcp"}}`)
	write(t, filepath.Join(d, "hooks", "hooks.json"), `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`)
	write(t, filepath.Join(d, "skills", "s", "SKILL.md"), "---\nname: s\n---\n")
	write(t, filepath.Join(cache, "m", "broken", "1", ".codex-plugin", "plugin.json"), `{"name": "broken", "mcpServers": "./nope.json"`)

	v := ReadNative()
	if e := nativeOf(t, v, "codex", "evil"); len(e.Parts) != 0 {
		t.Errorf("a path leaving the plugin was read: %+v", e.Parts)
	}
	def := nativeOf(t, v, "codex", "defaults")
	if m := partsOf(def, "mcp"); len(m) != 1 || m[0].Name != "web" || m[0].Detail != "http" {
		t.Errorf("default .mcp.json: %+v", m)
	}
	if h := partsOf(def, "hook"); len(h) != 1 || h[0].Name != "PreToolUse · Bash" || h[0].Detail != "echo hi" {
		t.Errorf("default hooks/hooks.json: %+v", h)
	}
	if s := partsOf(def, "skill"); len(s) != 1 || s[0].Name != "s" {
		t.Errorf("default skills/: %+v", s)
	}
	for _, p := range v {
		if p.Name == "broken" {
			t.Errorf("a plugin whose manifest doesn't parse is listed: %+v", p)
		}
	}
}

// The same plugin, as a version and its "latest" copy, is listed once.
func TestNativeCodexListedOnce(t *testing.T) {
	nativeCodexHome(t)
	n := 0
	for _, p := range ReadNative() {
		if p.Name == "chrome" {
			n++
			if filepath.Base(p.Dir) == "latest" {
				t.Errorf("the copy stands for the plugin: %s", p.Dir)
			}
		}
	}
	if n != 1 {
		t.Errorf("chrome listed %d times", n)
	}
}

func dirDump(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			raw, _ := os.ReadFile(p)
			b.WriteString(p + " " + string(raw) + "\n")
		}
		return nil
	})
	return b.String()
}

// A Pi package is listed with the skills its package.json "pi" manifest
// names (and those a "!" entry drops), one with no manifest by its
// skills/ folder; a package Pi hasn't installed yet is not listed.
func TestNativePiPackages(t *testing.T) {
	here, _ := piTwo(t)
	nm := filepath.Join(here, "npm", "node_modules")
	write(t, filepath.Join(here, "settings.json"), `{"packages": ["npm:@acme/pi-kit", "npm:plain-skills", "npm:not-installed", {"source": "npm:off-pkg", "extensions": [], "skills": [], "prompts": [], "themes": []}]}`)
	write(t, filepath.Join(nm, "@acme", "pi-kit", "package.json"), `{"name": "@acme/pi-kit", "version": "1.2.0", "keywords": ["pi-package"], "pi": {"extensions": ["./ext"], "skills": ["./skills", "!**/draft"]}}`)
	write(t, filepath.Join(nm, "@acme", "pi-kit", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review code\n---\n")
	write(t, filepath.Join(nm, "@acme", "pi-kit", "skills", "draft", "SKILL.md"), "---\nname: draft\n---\n")
	write(t, filepath.Join(nm, "@acme", "pi-kit", "ext", "index.ts"), "export default () => {}\n")
	write(t, filepath.Join(nm, "plain-skills", "package.json"), `{"name": "plain-skills", "version": "0.3.1"}`)
	write(t, filepath.Join(nm, "plain-skills", "skills", "tidy", "SKILL.md"), "---\nname: tidy\n---\n")
	write(t, filepath.Join(nm, "off-pkg", "package.json"), `{"name": "off-pkg"}`)

	v := ReadNative()
	var pis []NativePlugin
	for _, p := range v {
		if strings.HasPrefix(p.Agent, "pi") {
			pis = append(pis, p)
		}
	}
	if len(pis) != 3 {
		t.Fatalf("want the three installed packages, got %+v", pis)
	}
	kit := nativeOf(t, v, pis[0].Agent, "@acme/pi-kit")
	if kit.Version != "1.2.0" || kit.Scope != "user" || kit.Loaded != "unknown" || kit.Source != "npm:@acme/pi-kit" {
		t.Errorf("kit: %+v", kit)
	}
	if s := partsOf(kit, "skill"); len(s) != 1 || s[0].Name != "review" || s[0].Detail != "Review code" {
		t.Errorf("kit skills (the \"pi\" manifest, \"!**/draft\" drops one): %+v", s)
	}
	if s := partsOf(nativeOf(t, v, pis[0].Agent, "plain-skills"), "skill"); len(s) != 1 || s[0].Name != "tidy" {
		t.Errorf("a package with no manifest keeps its skills/: %+v", s)
	}
	if off := nativeOf(t, v, pis[0].Agent, "off-pkg"); off.Enabled == nil || *off.Enabled {
		t.Errorf("a package with every filter empty is off: %+v", off.Enabled)
	}
}

// A project's own .pi/settings.json packages are listed under it, as
// project scope.
func TestNativePiProjectPackages(t *testing.T) {
	here, _ := piTwo(t)
	_ = here
	proj := t.TempDir()
	if _, err := AddProject(proj); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(proj, ".pi", "settings.json"), `{"packages": ["npm:proj-kit"]}`)
	write(t, filepath.Join(proj, ".pi", "npm", "node_modules", "proj-kit", "package.json"), `{"name": "proj-kit", "version": "2.0.0"}`)
	write(t, filepath.Join(proj, ".pi", "npm", "node_modules", "proj-kit", "skills", "p", "SKILL.md"), "---\nname: p\n---\n")
	var got *NativePlugin
	for _, p := range ReadNative() {
		if p.Name == "proj-kit" {
			p := p
			got = &p
		}
	}
	if got == nil {
		t.Fatalf("project package not listed")
	}
	if got.Scope != "project" || got.Project != proj || got.Enabled != nil || len(partsOf(*got, "skill")) != 1 {
		t.Errorf("%+v", *got)
	}
}

// An unreadable settings.json lists nothing for that Pi, and a machine
// with no Codex and no Pi lists an empty list, not null.
func TestNativeNothingIsEmptyList(t *testing.T) {
	sandbox(t)
	if v := ReadNative(); v == nil || len(v) != 0 {
		t.Errorf("%#v", v)
	}
}

// A plugin Claude Code installed itself (`claude plugin install`, #7266)
// is listed for each scope installed_plugins.json records, named by its
// id, with the skills, hooks and subagents its manifest adds to the
// default folders, and whether that scope's settings turn it on. The
// record is the shape Claude Code 2.1.327 wrote for a user and a project
// install of one fixture plugin; nothing is written.
func TestNativeClaudePlugins(t *testing.T) {
	home := sandbox(t)
	cc := filepath.Join(home, ".claude")
	inst := filepath.Join(cc, "plugins", "cache", "fixmarket", "fixture", "0.1.0")
	proj := t.TempDir()
	write(t, filepath.Join(cc, "plugins", "installed_plugins.json"), `{
  "version": 2,
  "plugins": {
    "fixture@fixmarket": [
      {"scope": "user", "installPath": "`+inst+`", "version": "0.1.0", "installedAt": "2026-11-20T02:39:31.789Z"},
      {"scope": "project", "installPath": "`+inst+`", "version": "0.1.0", "projectPath": "`+proj+`"}
    ],
    "gone@fixmarket": [{"scope": "user", "installPath": "`+filepath.Join(cc, "nowhere")+`", "version": "1.0.0"}]
  }
}`)
	write(t, filepath.Join(cc, "settings.json"), `{"enabledPlugins": {"fixture@fixmarket": true}}`)
	write(t, filepath.Join(proj, ".claude", "settings.json"), `{"enabledPlugins": {"fixture@fixmarket": false}}`)
	write(t, filepath.Join(inst, ".claude-plugin", "plugin.json"), `{"name": "fixture", "version": "0.1.0",
  "skills": "./extra/", "agents": ["./more/fix-agent.md"],
  "hooks": {"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/usr/bin/true"}]}]}}}`)
	write(t, filepath.Join(inst, "skills", "fix-skill", "SKILL.md"), "---\nname: fix-skill\ndescription: A fixture skill\n---\n")
	write(t, filepath.Join(inst, "extra", "more-skill", "SKILL.md"), "---\nname: more-skill\ndescription: Another\n---\n")
	write(t, filepath.Join(inst, "more", "fix-agent.md"), "---\nname: fix-agent\ndescription: A fixture agent\n---\nYou review.\n")
	write(t, filepath.Join(inst, "agents", "plain.md"), "---\nname: plain\ndescription: Default folder\n---\nHi.\n")
	write(t, filepath.Join(inst, "agents", "notes.md"), "no front matter\n")
	write(t, filepath.Join(inst, "hooks", "hooks.json"), `{"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "echo done"}]}]}}`)
	write(t, filepath.Join(inst, ".mcp.json"), `{"mcpServers": {"fixdb": {"url": "https://x.example/mcp"}}}`)
	before := dirDump(t, cc)

	var got []NativePlugin
	for _, p := range ReadNative() {
		if p.Agent == "claude" {
			got = append(got, p)
		}
	}
	if len(got) != 2 {
		t.Fatalf("want the user and the project record, got %+v", got)
	}
	user, pr := got[0], got[1]
	if user.Name != "fixture@fixmarket" || user.Scope != "user" || user.Project != "" || user.Version != "0.1.0" ||
		user.Source != "fixmarket" || user.AgentName != "Claude Code" || user.Loaded != "unknown" || user.Enabled == nil || !*user.Enabled {
		t.Errorf("user: %+v", user)
	}
	if pr.Scope != "project" || pr.Project != proj || pr.Enabled == nil || *pr.Enabled {
		t.Errorf("project (its settings.json turns it off): %+v", pr)
	}
	names := func(kind string) (out []string) {
		for _, x := range partsOf(user, kind) {
			out = append(out, x.Name+"="+x.Detail)
		}
		return
	}
	if s := strings.Join(names("skill"), ","); s != "fix-skill=A fixture skill,more-skill=Another" {
		t.Errorf("skills (skills/ and the manifest's): %s", s)
	}
	if s := strings.Join(names("subagent"), ","); s != "fix-agent=A fixture agent,plain=Default folder" {
		t.Errorf("subagents (agents/ and the manifest's): %s", s)
	}
	if s := strings.Join(names("hook"), ","); s != "SessionStart=/usr/bin/true,Stop=echo done" {
		t.Errorf("hooks (inline and hooks/hooks.json): %s", s)
	}
	if s := strings.Join(names("mcp"), ","); s != "fixdb=http" {
		t.Errorf("servers: %s", s)
	}
	if after := dirDump(t, cc); after != before {
		t.Errorf("reading changed Claude's folder")
	}
}
