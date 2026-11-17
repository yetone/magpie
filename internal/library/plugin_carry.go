package library

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// A plugin goes with the library to another computer whole (#6685,
// JJasonsun on Discord): its record, and how that computer gets its
// files, and that computer makes its parts again itself. Its parts are
// never carried as the ordinary skills, servers, hooks and subagents they
// are here: each names this computer's folder for the plugin (a hook's
// ${CLAUDE_PLUGIN_ROOT}, made /Users/a/… at install), which on the other
// computer is no folder, and the two would trade their own paths back and
// forth on every sync.
//
// A plugin installed from a repository or a marketplace link is carried
// as its source, and the other computer installs it from there, as
// Update does: the bundle stays small and both end on the same version. A
// plugin from a folder of this computer's, or one whose link holds a
// sign-in, is carried with its files (the skills' budget, readSkills),
// and the other computer installs it from them. Either way the parts are
// read again there, with its own folder in every hook, server and skill.

// CarriedPlugin is a library plugin as a sync carries it.
type CarriedPlugin struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version,omitempty"`
	Source      string   `json:"source,omitempty"`
	Pick        string   `json:"pick,omitempty"`
	Agents      []string `json:"agents"`
	Off         bool     `json:"off,omitempty"`
	// Taken are the parts the user took out one by one (PluginView's
	// Missing), taken out on the other computer too
	Taken []string `json:"taken,omitempty"`
	// Files are a plugin's from a folder, as a skill's are; nil for one
	// fetched from its source
	Files map[string][]byte `json:"files"`
	Exec  []string          `json:"exec,omitempty"`
	Left  []string          `json:"left,omitempty"`
}

// fromFiles reports whether a plugin installed from source goes with its
// files: any source but a repository's link (a folder here, which no
// other computer has, C:\ on Windows too), and a link that holds a
// sign-in, which isn't carried (signedIn).
func fromFiles(source string) bool {
	if source == "" || filepath.IsAbs(expand(source)) || signedIn(source) {
		return true
	}
	if _, ok := githubSource(source); ok {
		return false
	}
	_, ok, err := gitSource(source)
	return !ok || err != nil
}

// signedIn reports whether a link holds a token: https://TOKEN@host/…,
// or a password. ssh://git@host/… names the user SSH signs in as, which
// is no secret.
func signedIn(source string) bool {
	u, err := url.Parse(source)
	if err != nil || u.User == nil {
		return false
	}
	_, pw := u.User.Password()
	return pw || u.Scheme == "http" || u.Scheme == "https"
}

// ownedPart reports whether an item is a part of a plugin the library has:
// such an item goes with its plugin, not on its own.
func (l *Library) ownedPart(bundle string) bool {
	return bundle != "" && l.plugin(bundle) != nil
}

// carriedPlugins are the library's plugins for Collect; dirs are the
// folders whose files go with them, read with the skills', one for each
// plugin carried with files, in order, and at the index in out.
func (l *Library) carriedPlugins() (out []*CarriedPlugin, dirs []string, at []int) {
	out = []*CarriedPlugin{}
	missing := map[string][]string{}
	for _, v := range l.pluginsView() {
		missing[v.Name] = v.Missing
	}
	for _, p := range sorted(l.Plugins, func(p *Plugin) string { return p.Name }) {
		c := &CarriedPlugin{Name: p.Name, Description: p.Description, Version: p.Version, Source: p.Source, Pick: p.Pick,
			Agents: orNone(slices.Sorted(slices.Values(p.Agents))), Off: p.Off}
		if m := missing[p.Name]; len(m) > 0 {
			c.Taken = slices.Sorted(slices.Values(m))
		}
		if fromFiles(p.Source) && !p.Pending {
			if signedIn(p.Source) {
				c.Source = ""
			}
			dirs, at = append(dirs, realDir(pluginDir(p.Name))), append(at, len(out))
		}
		out = append(out, c)
	}
	return out, dirs, at
}

// heldParts are the parts of this computer's plugins, kept out of l while
// Put replaces its items with a bundle's (which carries none of them),
// and put back after.
type heldParts struct {
	skills    []*Skill
	servers   []*Server
	hooks     []*Hook
	subagents []*Subagent
}

func (l *Library) holdPluginParts() *heldParts {
	h := &heldParts{}
	l.Skills = slices.DeleteFunc(l.Skills, func(s *Skill) bool {
		if l.ownedPart(s.Bundle) {
			h.skills = append(h.skills, s)
			return true
		}
		return false
	})
	l.MCP = slices.DeleteFunc(l.MCP, func(s *Server) bool {
		if l.ownedPart(s.Bundle) {
			h.servers = append(h.servers, s)
			return true
		}
		return false
	})
	l.Hooks = slices.DeleteFunc(l.Hooks, func(x *Hook) bool {
		if l.ownedPart(x.Bundle) {
			h.hooks = append(h.hooks, x)
			return true
		}
		return false
	})
	l.Subagents = slices.DeleteFunc(l.Subagents, func(s *Subagent) bool {
		if l.ownedPart(s.Bundle) {
			h.subagents = append(h.subagents, s)
			return true
		}
		return false
	})
	return h
}

// without is b without the items that have the name of a held part: a
// bundle from a magpie before plugins were carried has a plugin's parts as
// its own items, and this computer's plugin keeps its own.
func (h *heldParts) without(b *Bundle) *Bundle {
	c := *b
	skills := map[string]bool{}
	for _, s := range h.skills {
		skills[s.Name] = true
	}
	c.Skills = slices.DeleteFunc(slices.Clone(b.Skills), func(s *CarriedSkill) bool { return skills[s.Name] })
	servers := map[string]bool{}
	for _, s := range h.servers {
		servers[s.Name] = true
	}
	c.MCP = slices.DeleteFunc(slices.Clone(b.MCP), func(s *Server) bool { return servers[s.Name] })
	if b.Hooks != nil {
		names := map[string]bool{}
		for _, x := range h.hooks {
			names[x.Name] = true
		}
		hooks := slices.DeleteFunc(slices.Clone(*b.Hooks), func(x *Hook) bool { return names[x.Name] })
		c.Hooks = &hooks
	}
	if b.Subagents != nil {
		names := map[string]bool{}
		for _, s := range h.subagents {
			names[s.Name] = true
		}
		subs := slices.DeleteFunc(slices.Clone(*b.Subagents), func(s *Subagent) bool { return names[s.Name] })
		c.Subagents = &subs
	}
	return &c
}

// putBack returns the held parts to l, but for one whose name an item the
// bundle brought has now: that one is the library's, and the plugin's part
// is gone (the page lists it as taken out).
func (l *Library) putBack(h *heldParts) {
	for _, s := range h.skills {
		if l.skill(s.Name) == nil {
			l.Skills = append(l.Skills, s)
		}
	}
	for _, s := range h.servers {
		if l.server(s.Name) == nil {
			l.MCP = append(l.MCP, s)
		}
	}
	for _, x := range h.hooks {
		if l.hook(x.Name) == nil {
			l.Hooks = append(l.Hooks, x)
		}
	}
	for _, s := range h.subagents {
		if l.subagent(s.Name) == nil {
			l.Subagents = append(l.Subagents, s)
		}
	}
}

// putPlugins makes l's plugins the ones carried, inside Put's change: a
// plugin as it is here only takes the agents and the switch; one carried
// with its files is installed from them, in stage; one carried by its
// source is recorded as pending, to be fetched once the library is let go
// (fetch); one the bundle hasn't is taken out, its folder after (gone).
func (l *Library) putPlugins(in []*CarriedPlugin, stage string) (fetch []*CarriedPlugin, gone []string, problems []Problem) {
	want := map[string]bool{}
	for _, c := range in {
		want[c.Name] = true
	}
	for _, p := range slices.Clone(l.Plugins) {
		if !want[p.Name] {
			removePluginParts(l, p)
			l.Plugins = slices.DeleteFunc(l.Plugins, func(x *Plugin) bool { return x.Name == p.Name })
			gone = append(gone, p.Name)
		}
	}
	settle := func(p *Plugin, c *CarriedPlugin) {
		p.Agents, p.Off = slices.Clone(orNone(c.Agents)), c.Off
		if c.Description != "" {
			p.Description = c.Description
		}
		takeOut(l, p, c.Taken)
		l.givePlugin(p)
	}
	for _, c := range in {
		have := l.plugin(c.Name)
		local := c.Files != nil
		same := have != nil && !have.Pending && have.Source == c.Source && have.Pick == c.Pick && have.Version == c.Version
		if same && local {
			here := readSkills([]string{realDir(pluginDir(c.Name))})[0]
			same = sameFiles(here, &CarriedSkill{Files: c.Files, Exec: c.Exec})
		}
		if same {
			settle(have, c)
			continue
		}
		if !local {
			p := have
			if p == nil {
				p = &Plugin{Name: c.Name}
				l.Plugins = append(l.Plugins, p)
				sort.Slice(l.Plugins, func(i, j int) bool { return l.Plugins[i].Name < l.Plugins[j].Name })
			}
			// the old version's parts stay until the new one is in
			p.Description, p.Version, p.Source, p.Pick, p.Pending = c.Description, c.Version, c.Source, c.Pick, true
			settle(p, c)
			fetch = append(fetch, c)
			continue
		}
		fail := func(err error) {
			problems = append(problems, Problem{What: "plugin:" + c.Name, Error: err.Error()})
		}
		dir := filepath.Join(stage, ".plugin-"+c.Name)
		files := &CarriedSkill{Files: c.Files, Exec: c.Exec, Left: c.Left}
		if err := writeSkill(dir, files); err != nil {
			fail(err)
			continue
		}
		if err := keepLeft(dir, realDir(pluginDir(c.Name)), files); err != nil {
			fail(err)
			continue
		}
		pl, err := readPlugin(dir, c.Name, c.Name, nil)
		if err != nil {
			fail(err)
			continue
		}
		if pl.parts() == 0 {
			fail(fmt.Errorf("%s has no skills, MCP servers, hooks or subagents magpie can give an agent", c.Name))
			continue
		}
		skipped, err := installPlugin(l, pl, dir, c.Source, c.Pick, c.Agents)
		if err != nil {
			fail(err)
			continue
		}
		problems = append(problems, skipped...)
		if p := l.plugin(c.Name); p != nil {
			if c.Version != "" {
				p.Version = c.Version
			}
			settle(p, c)
		}
	}
	return fetch, gone, problems
}

// takeOut takes out of l the parts of p the user took out by hand on the
// other computer, named as PluginView's Missing names them.
func takeOut(l *Library, p *Plugin, taken []string) {
	stamp := time.Now().Format("2006-01-02_15-04-05.000")
	for _, t := range taken {
		switch {
		case strings.HasPrefix(t, "skill "):
			if s := l.skill(strings.TrimPrefix(t, "skill ")); s != nil && s.Bundle == p.Name {
				_ = removeSkill(l, s.Name, stamp)
			}
		case strings.HasPrefix(t, "MCP server "):
			if s := l.server(strings.TrimPrefix(t, "MCP server ")); s != nil && s.Bundle == p.Name {
				_ = removeServer(l, s.Name)
			}
		case strings.HasPrefix(t, "hook "):
			n := strings.TrimPrefix(t, "hook ")
			l.Hooks = slices.DeleteFunc(l.Hooks, func(h *Hook) bool { return h.Name == n && h.Bundle == p.Name })
		case strings.HasPrefix(t, "subagent "):
			n := strings.TrimPrefix(t, "subagent ")
			l.Subagents = slices.DeleteFunc(l.Subagents, func(s *Subagent) bool { return s.Name == n && s.Bundle == p.Name })
		}
	}
}

// fetchPlugins installs the plugins Put recorded as pending from their
// sources, the library let go of while each downloads; what fails stays
// pending, said in problems, for Update or the next sync to try again.
func fetchPlugins(in []*CarriedPlugin, res *Result) {
	for _, c := range in {
		r, err := UpdatePlugin(c.Name)
		if err != nil {
			res.Problems = append(res.Problems, Problem{What: "plugin:" + c.Name,
				Error: "not fetched from its source on this computer yet (Update tries again): " + err.Error()})
			continue
		}
		if len(c.Taken) > 0 {
			if t, err := change(func(l *Library) error {
				if p := l.plugin(c.Name); p != nil {
					takeOut(l, p, c.Taken)
				}
				return nil
			}); err == nil {
				r.Changed = append(r.Changed, t.Changed...)
				r.Problems = append(r.Problems, t.Problems...)
			}
		}
		for _, a := range r.Changed {
			if !slices.Contains(res.Changed, a) {
				res.Changed = append(res.Changed, a)
			}
		}
		res.Problems = append(res.Problems, r.Problems...)
		res.Problems = append(res.Problems, r.Skipped...)
	}
}

// removePluginDirs takes the folders of the plugins Put took out.
func removePluginDirs(names []string) {
	for _, n := range names {
		os.RemoveAll(pluginDir(n))
	}
}
