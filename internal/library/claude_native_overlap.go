package library

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/lazyre"
)

// A plugin the library has can be Claude Code's own plugin as well, turned
// on in Claude Code's settings (enabledPlugins, "name@marketplace"): then
// Claude Code loads it natively, and the parts magpie gives Claude Code
// from the library's copy run a second time, each hook twice (jinpwng on
// X). So where Claude Code loads it natively, magpie gives Claude Code
// none of its parts:
//
//   - user-wide, when the user's settings.json turns it on: the parts are
//     left out of Claude Code's own files for the sync (claudeAside) and of
//     what the MCP hub and skill hub give it;
//   - in a project, when Claude Code's settings for that folder turn it on
//     (the project's .claude/settings.local.json over its settings.json
//     over the user's), or the project turns it on through magpie
//     (ClaudePlugins): the project's plugin parts aren't written for Claude
//     Code there (withPlugins, projectHooks).
//
// The user's enabledPlugins are only read, never written. A settings file
// that can't be read says nothing: the parts are given as before. One case
// stays doubled, as magpie can't take a part given to every project out of
// one: a plugin given to Claude Code user-wide that a project turns on
// natively for itself. The project's card says so (ProjectView.ClaudeTwice).

// claudeEnabledIn are a settings file's enabledPlugins, id → on; nil
// when it has none or can't be read.
func claudeEnabledIn(file string) map[string]bool {
	b, err := readClaudeJSON(file)
	if err != nil || b == nil {
		return nil
	}
	var out map[string]bool
	gjson.GetBytes(b, "enabledPlugins").ForEach(func(k, v gjson.Result) bool {
		if v.IsBool() && strings.Contains(k.String(), "@") {
			if out == nil {
				out = map[string]bool{}
			}
			out[k.String()] = v.Bool()
		}
		return true
	})
	return out
}

// claudeLoads are the plugin ids Claude Code turns on in dir, a project's
// folder, as Claude Code reads its settings there: the folder's
// .claude/settings.local.json over its .claude/settings.json over the
// user's. "" is the user's alone.
func claudeLoads(dir string) map[string]bool {
	files := []string{claudeSettings()}
	if dir != "" {
		files = append(files, filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"))
	}
	on := map[string]bool{}
	for _, f := range files {
		maps.Copy(on, claudeEnabledIn(f))
	}
	maps.DeleteFunc(on, func(_ string, v bool) bool { return !v })
	return on
}

// claudeMarketRepos are the GitHub repositories of the marketplaces Claude
// Code knows, by name, as owner/repo in lower case.
func claudeMarketRepos() map[string]string {
	out := map[string]string{claudeOfficial: "anthropics/claude-plugins-official"}
	add := func(r gjson.Result) {
		r.ForEach(func(k, v gjson.Result) bool {
			src := v.Get("source")
			repo := src.Get("repo").String()
			if repo == "" {
				repo = src.Get("url").String()
			}
			if s := githubRepo(repo); s != "" {
				out[k.String()] = s
			}
			return true
		})
	}
	if b, _ := readClaudeJSON(claudeSettings()); b != nil {
		add(gjson.GetBytes(b, "extraKnownMarketplaces"))
	}
	if b, _ := readClaudeJSON(claudeMarketplaces()); b != nil {
		add(gjson.ParseBytes(b))
	}
	return out
}

var githubRepoRe = lazyre.New(`(?i)^(?:(?:https?://|git@|ssh://git@)?github\.com[/:])?([a-z0-9][a-z0-9-]*/[a-z0-9._-]+?)(?:\.git)?/?$`)

// githubRepo is s as owner/repo in lower case when it names a GitHub
// repository (owner/repo, or a github.com address), "" when it doesn't.
func githubRepo(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") || filepath.IsAbs(s) {
		return ""
	}
	m := githubRepoRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// claudeID is the id of Claude Code's plugin among on that b is: one
// whose name is b's (its name in the library, in its marketplace, or in
// its manifest), from a marketplace that isn't another GitHub repository
// than b's own source. "" is none.
func (b *Plugin) claudeID(on map[string]bool, repos map[string]string) string {
	if b == nil || b.Off || len(on) == 0 {
		return ""
	}
	names := []string{b.Name, b.Pick}
	if m, err := readJSONFile(filepath.Join(pluginDir(b.Name), ".claude-plugin", "plugin.json")); err == nil {
		names = append(names, plStr(m["name"]))
	}
	own := githubRepo(b.Source)
	for _, id := range slices.Sorted(maps.Keys(on)) {
		name, market, _ := strings.Cut(id, "@")
		if name == "" || !slices.Contains(names, name) {
			continue
		}
		if r := repos[market]; own != "" && r != "" && r != own {
			continue
		}
		return id
	}
	return ""
}

// claudeNatives are, by plugin name, the ids under which Claude Code loads
// the library's plugins natively in dir ("" the user's settings alone).
// extra are ids turned on there beside the settings (a project's
// ClaudePlugins).
func (l *Library) claudeNatives(dir string, extra ...string) map[string]string {
	if len(l.Plugins) == 0 {
		return nil
	}
	on := claudeLoads(dir)
	for _, id := range extra {
		on[id] = true
	}
	if len(on) == 0 {
		return nil
	}
	repos := claudeMarketRepos()
	var out map[string]string
	for _, b := range l.Plugins {
		if id := b.claudeID(on, repos); id != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[b.Name] = id
		}
	}
	return out
}

// projectClaudeNatives are the plugins Claude Code loads natively in p.
func (l *Library) projectClaudeNatives(p *Project) map[string]string {
	return l.claudeNatives(p.Dir, p.ClaudePlugins...)
}

// claudeCode is whether t is Claude Code on this computer, whose settings
// claudeSettings reads.
func claudeCode(t *Target) bool {
	return t != nil && t.Agent != nil && t.Agent.ID == "claude" && t.Agent.WSL == ""
}

// claudeAside takes Claude Code out of the agents of every part of the
// plugins it loads natively user-wide, for one step of a sync: the
// returned func puts each back as it was, before the library is saved.
func (l *Library) claudeAside() func() {
	natives := l.claudeNatives("")
	if len(natives) == 0 {
		return func() {}
	}
	var undo []func()
	take := func(agents *[]string) {
		if i := slices.Index(*agents, "claude"); i >= 0 {
			old := *agents
			*agents = slices.Delete(slices.Clone(old), i, i+1)
			undo = append(undo, func() { *agents = old })
		}
	}
	for name := range natives {
		b := l.plugin(name)
		for _, n := range b.Skills {
			if s := l.skill(n); s != nil && s.Bundle == name {
				take(&s.Agents)
			}
		}
		for _, n := range b.MCP {
			if s := l.server(n); s != nil && s.Bundle == name {
				take(&s.Agents)
			}
		}
		for _, n := range b.Hooks {
			if h := l.hook(n); h != nil && h.Bundle == name {
				take(&h.Agents)
			}
		}
		for _, n := range b.Subagents {
			if s := l.subagent(n); s != nil && s.Bundle == name {
				take(&s.Agents)
			}
		}
	}
	return func() {
		for _, u := range slices.Backward(undo) {
			u()
		}
	}
}

// claudeHubNatives are the plugins Claude Code loads natively where the
// hub serves agent id, in project p or (nil) user-wide: none for any
// other agent.
func (l *Library) claudeHubNatives(id string, p *Project) map[string]string {
	if id != "claude" {
		return nil
	}
	if p != nil {
		return l.projectClaudeNatives(p)
	}
	return l.claudeNatives("")
}

// projectClaudeOverlap are, for the project card, the plugins p gives
// Claude Code that it loads natively there (left out), and those given
// to Claude Code user-wide that it loads natively in p alone (run twice
// there). user is claudeNatives("").
func (l *Library) projectClaudeOverlap(p *Project, user map[string]string) (native, twice map[string]string) {
	here := l.projectClaudeNatives(p)
	for name, id := range here {
		if slices.Contains(p.Plugins[name], "claude") {
			if native == nil {
				native = map[string]string{}
			}
			native[name] = id
		}
		if b := l.plugin(name); b != nil && !b.Off && slices.Contains(b.Agents, "claude") && user[name] == "" {
			if twice == nil {
				twice = map[string]string{}
			}
			twice[name] = id
		}
	}
	return native, twice
}

// withoutClaudeNative are a project's agents for plugin name, without
// Claude Code when it loads the plugin natively there.
func withoutClaudeNative(agents []string, natives map[string]string, name string) []string {
	if _, ok := natives[name]; !ok || !slices.Contains(agents, "claude") {
		return agents
	}
	return slices.DeleteFunc(slices.Clone(agents), func(a string) bool { return a == "claude" })
}
