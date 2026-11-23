package library

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
)

// A project's plugins (jinpwng on X): a plugin the library keeps is given
// to a project whole, for some of its agents, and taken out of it whole.
// Its skills, MCP servers and subagents go where the project's own do
// (projects.go, project_mcp.go, project_subagents.go), beside them: for
// a sync, syncProject adds them to the project's own sets (withPlugins),
// and puts those back as they were after. So a part the project also has
// of its own stays when the plugin goes, and taking the plugin out is
// only deleting it from Plugins: what magpie placed or wrote for it,
// and nothing else, comes out on that sync, by the records each part
// keeps already (Placed, Wrote, AgentFiles). Its hooks are written in the
// project's own hooks file (projectHookFiles), entry by entry, as the
// agent's own are (hookWrite): an entry the user changed since is theirs
// and stays.

// projectHookFiles are the files, in a project, an agent reads hooks
// from that magpie writes a project's plugins' hooks into: Claude Code's
// .claude/settings.local.json (its local project settings, which it
// keeps out of git itself), and Codex's .codex/hooks.json, which
// codex-rs/hooks loads from a project's .codex folder in a project the
// user trusts, each hook in it once the user has reviewed it in /hooks.
var projectHookFiles = map[string]string{
	"claude": claudeSettingsRel,
	"codex":  ".codex/hooks.json",
}

// ProjectHooksFile is the file in a project an agent reads hooks from:
// "" for one magpie can't give a project's hooks to.
func ProjectHooksFile(agent string) string { return projectHookFiles[agent] }

// projectPluginTakes says whether an agent takes any part of a plugin in
// a project.
func projectPluginTakes(id string) bool {
	return ProjectSkillsDir(id) != "" || ProjectMCPFile(id) != "" || ProjectSubagentsDir(id) != "" || ProjectHooksFile(id) != ""
}

func projectHookSpec(p *Project, id string) *hookSpec {
	rel := projectHookFiles[id]
	abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
	switch id {
	case "claude":
		return claudeHookSpec(abs)
	case "codex":
		return codexHookSpec(abs)
	}
	return nil
}

// withPlugins are the project's skills, servers and subagents with the
// parts of its plugins added, each for those of the plugin's agents that
// take it in a project: new maps, the project's own left as they are. A
// plugin turned off, or gone from the library, gives nothing; a part
// taken out of the library one by one, or one that is another's by now,
// isn't the plugin's.
func (l *Library) withPlugins(p *Project) (skills, servers, subagents map[string][]string) {
	if len(p.Plugins) == 0 {
		return p.Skills, p.Servers, p.Subagents
	}
	clone := func(m map[string][]string) map[string][]string {
		out := make(map[string][]string, len(m))
		for k, v := range m {
			out[k] = slices.Clone(v)
		}
		return out
	}
	skills, servers, subagents = clone(p.Skills), clone(p.Servers), clone(p.Subagents)
	add := func(m map[string][]string, name string, agents []string, takes func(string) bool) {
		for _, a := range agents {
			if takes(a) {
				m[name] = set(m[name], a, true)
			}
		}
	}
	natives := l.projectClaudeNatives(p)
	for name, agents := range p.Plugins {
		b := l.plugin(name)
		if b == nil || b.Off {
			continue
		}
		agents = withoutClaudeNative(agents, natives, name)
		for _, n := range b.Skills {
			if s := l.skill(n); s != nil && s.Bundle == b.Name {
				add(skills, n, agents, func(a string) bool { return ProjectSkillsDir(a) != "" })
			}
		}
		for _, n := range b.MCP {
			if s := l.server(n); s != nil && s.Bundle == b.Name {
				add(servers, n, agents, func(a string) bool { return ProjectMCPFile(a) != "" })
			}
		}
		for _, n := range b.Subagents {
			if s := l.subagent(n); s != nil && s.Bundle == b.Name {
				add(subagents, n, agents, func(a string) bool { return ProjectSubagentsDir(a) != "" })
			}
		}
	}
	return skills, servers, subagents
}

// projectHooks are, by agent, the hooks the project's plugins give it.
func (l *Library) projectHooks(p *Project) map[string][]*Hook {
	out := map[string][]*Hook{}
	var natives map[string]string
	if len(p.Plugins) > 0 {
		natives = l.projectClaudeNatives(p)
	}
	for _, name := range slices.Sorted(maps.Keys(p.Hooks)) {
		h := l.hook(name)
		if h == nil {
			continue
		}
		for _, a := range p.Hooks[name] {
			if ProjectHooksFile(a) != "" && hookTakes(h, a) && !slices.Contains(out[a], h) {
				out[a] = append(out[a], h)
			}
		}
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range p.Plugins {
			if !yield(k) {
				return
			}
		}
	}) {
		b := l.plugin(name)
		if b == nil || b.Off {
			continue
		}
		agents := withoutClaudeNative(p.Plugins[name], natives, name)
		for _, n := range b.Hooks {
			h := l.hook(n)
			if h == nil || h.Bundle != b.Name {
				continue
			}
			for _, a := range agents {
				if ProjectHooksFile(a) != "" && hookTakes(h, a) && !slices.Contains(out[a], h) {
					out[a] = append(out[a], h)
				}
			}
		}
	}
	return out
}

// syncProjectHooks writes the hooks of the project's plugins in each
// agent's project hooks file, and takes out those magpie wrote there that
// no plugin gives any more. A file magpie made is taken away again once
// nothing is left in it.
func (l *Library) syncProjectHooks(p *Project, fail func(what string, err error)) {
	want := l.projectHooks(p)
	ids := map[string]bool{}
	for id := range want {
		ids[id] = true
	}
	for id := range p.HookRecs {
		ids[id] = true
	}
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for k := range ids {
			if !yield(k) {
				return
			}
		}
	}) {
		rel := projectHookFiles[id]
		sp := projectHookSpec(p, id)
		if sp == nil {
			// an agent magpie wrote hooks for once and no longer can:
			// its records go, the entries stay the user's
			delete(p.HookRecs, id)
			continue
		}
		if err := p.inside(sp.path); err != nil {
			for _, h := range want[id] {
				fail("hook:"+h.Name, err)
			}
			continue
		}
		made := false
		if len(want[id]) > 0 && !exists(sp.path) {
			if err := p.mkdirs(filepath.Dir(sp.path)); err != nil {
				for _, h := range want[id] {
					fail("hook:"+h.Name, err)
				}
				continue
			}
			made = true
		}
		w := &hookWrite{id: id, sp: sp, want: want[id], recs: p.HookRecs[id], fail: fail}
		if id == "codex" {
			w.rekeyCfg = filepath.Join(codexDir(), "config.toml")
		}
		recs, _, ok := w.run()
		if !ok {
			continue
		}
		if made && exists(sp.path) && !slices.Contains(p.MadeFiles, rel) {
			p.MadeFiles = append(p.MadeFiles, rel)
			sort.Strings(p.MadeFiles)
		}
		if len(recs) == 0 {
			delete(p.HookRecs, id)
			// Claude Code's file holds the skills the project turns off
			// too (syncSkillsOff)
			if id != "claude" || len(p.Overrides) == 0 && len(p.ClaudeWrote) == 0 {
				p.dropMadeFile(rel)
			}
			continue
		}
		if p.HookRecs == nil {
			p.HookRecs = map[string][]HookEntry{}
		}
		p.HookRecs[id] = recs
	}
	if len(p.HookRecs) == 0 {
		p.HookRecs = nil
	}
}

// ProjectPlugin sets which agents get a library plugin in a project: each
// of its parts goes to those of them that take it there. None takes the
// plugin out of the project, and with it only what magpie wrote for it.
func ProjectPlugin(dir, name string, agents []string) (*Result, error) {
	for _, a := range agents {
		if err := checkAgent(a); err != nil {
			return nil, err
		}
	}
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if name == "" {
			return errors.New("no plugin given")
		}
		var ids []string
		for _, id := range agents {
			if !projectPluginTakes(id) {
				return fmt.Errorf("magpie knows of nowhere %s reads a project's skills, servers, subagents or hooks from", id)
			}
			ids = set(ids, id, true)
		}
		if len(ids) == 0 {
			delete(p.Plugins, name)
			if len(p.Plugins) == 0 {
				p.Plugins = nil
			}
			return nil
		}
		if l.plugin(name) == nil {
			return fmt.Errorf("no plugin called %s", name)
		}
		if p.Plugins == nil {
			p.Plugins = map[string][]string{}
		}
		p.Plugins[name] = ids
		return nil
	})
}

// ProjectHooks sets which agents get each of some library hooks in a
// project, one at a time, not as part of a plugin (#7253); none takes them
// out, and with them only the entries magpie wrote.
func ProjectHooks(dir string, names, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if len(names) == 0 {
			return errors.New("no hooks given")
		}
		for _, n := range names {
			if l.hook(n) == nil {
				return fmt.Errorf("no hook called %s", n)
			}
		}
		var ids []string
		for _, id := range agents {
			if ProjectHooksFile(id) == "" {
				return fmt.Errorf("magpie knows of no file %s reads a project's hooks from", id)
			}
			ids = set(ids, id, true)
		}
		for _, n := range names {
			if len(ids) == 0 {
				delete(p.Hooks, n)
				continue
			}
			if p.Hooks == nil {
				p.Hooks = map[string][]string{}
			}
			p.Hooks[n] = slices.Clone(ids)
		}
		if len(p.Hooks) == 0 {
			p.Hooks = nil
		}
		return nil
	})
}

// dropProjectHooks takes hooks leaving the library out of every project.
func (l *Library) dropProjectHooks(names []string) {
	for _, p := range l.Projects {
		for _, n := range names {
			delete(p.Hooks, n)
		}
		if len(p.Hooks) == 0 {
			p.Hooks = nil
		}
	}
}

// dropProjectPlugin takes a plugin out of every project, and their
// worktrees: it is leaving the library.
func (l *Library) dropProjectPlugin(name string) {
	for _, p := range l.Projects {
		delete(p.Plugins, name)
		if len(p.Plugins) == 0 {
			p.Plugins = nil
		}
	}
}
