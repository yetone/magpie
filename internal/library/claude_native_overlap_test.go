package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeOn turns plugin ids on in a Claude Code settings file, as the user
// (or Claude Code's /plugin) would, keeping what else it holds: the hooks
// magpie wrote there too.
func claudeOn(t *testing.T, file string, ids ...string) {
	t.Helper()
	doc := map[string]any{}
	if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
	}
	on := map[string]any{}
	for _, id := range ids {
		on[id] = true
	}
	doc["enabledPlugins"], doc["theme"] = on, "dark"
	b, _ := json.MarshalIndent(doc, "", "  ")
	write(t, file, string(b)+"\n")
}

// A plugin Claude Code loads itself user-wide (jinpwng on X) isn't given
// to Claude Code a second time from the library: none of its hooks,
// skills, subagents or servers go into Claude Code's files, while Codex
// still gets them all, the user's enabledPlugins stay as they were, and
// the Library's plugin says why. Turned off in Claude Code, magpie gives
// them again.
func TestClaudeNativePluginNotGivenTwice(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{"claude", "codex"}))
	settings := filepath.Join(h, ".claude/settings.json")
	if got := read(t, settings); !strings.Contains(got, "echo done") {
		t.Fatalf("not native yet, claude gets the hooks:\n%s", got)
	}

	claudeOn(t, settings, "acme-kit@acme")
	ok(t)(Sync())
	got := read(t, settings)
	for _, no := range []string{"echo done", "lint.sh", "compacting", "CLAUDE_PLUGIN_ROOT"} {
		if strings.Contains(got, no) {
			t.Errorf("claude settings still have %q while Claude Code loads the plugin:\n%s", no, got)
		}
	}
	if !strings.Contains(got, `"acme-kit@acme": true`) || !strings.Contains(got, `"theme": "dark"`) {
		t.Errorf("the user's own settings changed:\n%s", got)
	}
	gone(t, filepath.Join(h, ".claude/skills/tdd"))
	gone(t, filepath.Join(h, ".claude/agents/reviewer.md"))
	if cj := read(t, filepath.Join(h, ".claude.json")); strings.Contains(cj, "acme-db") {
		t.Errorf("claude still has the plugin's server:\n%s", cj)
	}
	if c := read(t, filepath.Join(h, ".codex/hooks.json")); !strings.Contains(c, "echo done") {
		t.Errorf("codex lost the plugin's hooks:\n%s", c)
	}
	there(t, filepath.Join(h, ".codex/agents/reviewer.toml"))

	v := View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].ClaudeNative != "acme-kit@acme" {
		t.Fatalf("the plugin's view doesn't say Claude Code loads it: %+v", v.Plugins)
	}
	// the library still gives it to Claude Code: the step put it back
	if b := v.Plugins[0]; len(b.Agents) != 2 {
		t.Errorf("the plugin's agents changed: %v", b.Agents)
	}
	for _, s := range v.Skills {
		if !strings.Contains(strings.Join(s.Agents, ","), "claude") {
			t.Errorf("skill %s lost claude in the library: %v", s.Name, s.Agents)
		}
	}
	for _, s := range loadT(t).MCP {
		if s.Bundle == "acme-kit" && !strings.Contains(strings.Join(s.Agents, ","), "claude") {
			t.Errorf("server %s lost claude in the library: %v", s.Name, s.Agents)
		}
	}
	for _, s := range loadT(t).hubSkills("claude") {
		if s.Name == "tdd" {
			t.Errorf("the skill hub gives claude the plugin's skill")
		}
	}
	for _, s := range loadT(t).hubServers("claude") {
		if s.Name == "acme-db" {
			t.Errorf("the MCP hub gives claude the plugin's server")
		}
	}

	// turned off in Claude Code (false, not gone): magpie gives it again
	write(t, settings, strings.Replace(read(t, settings), `"acme-kit@acme": true`, `"acme-kit@acme": false`, 1))
	ok(t)(Sync())
	if got := read(t, settings); !strings.Contains(got, "echo done") || !strings.Contains(got, `"acme-kit@acme": false`) {
		t.Errorf("off in Claude Code, claude should get the hooks again:\n%s", got)
	}
	there(t, filepath.Join(h, ".claude/skills/tdd"))
	if v := View0(t); v.Plugins[0].ClaudeNative != "" {
		t.Errorf("off in Claude Code, still noted: %q", v.Plugins[0].ClaudeNative)
	}
}

// Another marketplace's plugin of the same name, from another GitHub
// repository than the library's copy, is another plugin: magpie gives
// Claude Code the library's.
func TestClaudeNativeOtherRepoIsAnotherPlugin(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	l := loadT(t)
	b := l.plugin("acme-kit")
	b.Source = "https://github.com/acme/kit"
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(h, ".claude/plugins/known_marketplaces.json"),
		`{"other":{"source":{"source":"github","repo":"someone/else"}},"acme":{"source":{"source":"github","repo":"Acme/Kit"}}}`)
	claudeOn(t, filepath.Join(h, ".claude/settings.json"), "acme-kit@other")
	ok(t)(Sync())
	if got := read(t, filepath.Join(h, ".claude/settings.json")); !strings.Contains(got, "echo done") {
		t.Errorf("another repo's acme-kit kept the library's from claude:\n%s", got)
	}
	claudeOn(t, filepath.Join(h, ".claude/settings.json"), "acme-kit@acme")
	ok(t)(Sync())
	if got := read(t, filepath.Join(h, ".claude/settings.json")); strings.Contains(got, "echo done") {
		t.Errorf("the same repo's plugin is given twice:\n%s", got)
	}
}

// In a project, Claude Code's settings for the folder decide: the
// project's .claude/settings.json, its settings.local.json over that, or
// the plugin turned on through magpie (ClaudePlugins). Where it loads the
// plugin, the project's plugin parts go to every other agent and not to
// Claude Code, and the project's card says so.
func TestClaudeNativePluginInAProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T, dir string)
	}{
		{"settings.json", func(t *testing.T, dir string) {
			claudeOn(t, filepath.Join(dir, ".claude/settings.json"), "acme-kit@acme")
		}},
		{"settings.local.json", func(t *testing.T, dir string) {
			claudeOn(t, filepath.Join(dir, ".claude/settings.json"), "other@acme")
			claudeOn(t, filepath.Join(dir, ".claude/settings.local.json"), "acme-kit@acme")
		}},
		{"ClaudePlugins", func(t *testing.T, dir string) {
			l := loadT(t)
			for _, p := range l.Projects {
				if p.Dir == dir {
					p.ClaudePlugins = []string{"acme-kit@acme"}
				}
			}
			if err := l.save(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, b := pluginProjects(t)
			ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude", "codex"}))
			ok(t)(ProjectPlugin(b, "acme-kit", []string{"claude", "codex"}))
			local := filepath.Join(a, ".claude/settings.local.json")
			if got := read(t, local); !strings.Contains(got, "echo done") {
				t.Fatalf("not native yet:\n%s", got)
			}
			tc.set(t, a)
			before := ""
			if tc.name != "ClaudePlugins" {
				before = read(t, filepath.Join(a, ".claude/settings.json"))
			}
			ok(t)(Sync())
			got := read(t, local)
			if strings.Contains(got, "echo done") || strings.Contains(got, "lint.sh") {
				t.Errorf("claude's project settings still have the plugin's hooks:\n%s", got)
			}
			if tc.name == "settings.local.json" && !strings.Contains(got, `"acme-kit@acme": true`) {
				t.Errorf("the user's enabledPlugins changed:\n%s", got)
			}
			if before != "" && read(t, filepath.Join(a, ".claude/settings.json")) != before {
				t.Errorf("the project's settings.json changed")
			}
			gone(t, filepath.Join(a, ".claude/skills/tdd"))
			gone(t, filepath.Join(a, ".claude/agents/reviewer.md"))
			if m := read(t, filepath.Join(a, ".mcp.json")); strings.Contains(m, "acme-db") {
				t.Errorf(".mcp.json still has the plugin's server:\n%s", m)
			}
			if c := read(t, filepath.Join(a, ".codex/hooks.json")); !strings.Contains(c, "echo done") {
				t.Errorf("codex lost the plugin's hooks:\n%s", c)
			}
			there(t, filepath.Join(a, ".codex/agents/reviewer.toml"))
			// the other project, where Claude Code doesn't load it, keeps them
			if got := read(t, filepath.Join(b, ".claude/settings.local.json")); !strings.Contains(got, "echo done") {
				t.Errorf("the other project lost claude's hooks:\n%s", got)
			}
			there(t, filepath.Join(b, ".claude/skills/tdd"))

			for _, pv := range View0(t).Projects {
				want := ""
				if pv.Dir == a {
					want = "acme-kit@acme"
				}
				if pv.ClaudeNative["acme-kit"] != want {
					t.Errorf("project %s notes %v, want %q", pv.Dir, pv.ClaudeNative, want)
				}
			}
		})
	}
}

// A plugin given to Claude Code user-wide that one project turns on in
// Claude Code for itself runs twice there, which magpie can't take out of
// one project: the card says so.
func TestClaudeNativeInAProjectGivenUserWideIsNoted(t *testing.T) {
	_, a, _ := pluginProjects(t)
	ok(t)(PluginAgents("acme-kit", []string{"claude"}))
	claudeOn(t, filepath.Join(a, ".claude/settings.json"), "acme-kit@acme")
	for _, pv := range View0(t).Projects {
		want := ""
		if pv.Dir == a {
			want = "acme-kit@acme"
		}
		if pv.ClaudeTwice["acme-kit"] != want {
			t.Errorf("project %s twice %v, want %q", pv.Dir, pv.ClaudeTwice, want)
		}
		if len(pv.ClaudeNative) > 0 {
			t.Errorf("project %s notes a plugin it doesn't give: %v", pv.Dir, pv.ClaudeNative)
		}
	}
}

// A settings file that can't be read says nothing: the parts are given as
// before.
func TestClaudeNativeUnreadableSettingsGiveAsBefore(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	a := filepath.Join(t.TempDir(), "a")
	gitInit(t, a)
	ok(t)(AddProject(a))
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"}))
	write(t, filepath.Join(a, ".claude/settings.json"), `{"enabledPlugins": {"acme-kit@acme": tr`)
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	there(t, filepath.Join(a, ".claude/skills/tdd"))
}
