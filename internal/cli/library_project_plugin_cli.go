package cli

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

// projectPluginCmd is magpie library project plugin …: the project card's
// Plugins tab from a terminal (jinpwng on X), through the two calls the
// page's chips make. A library plugin goes to its agents
// (library.ProjectPlugin, "projects/plugin"); a plugin Claude Code has
// installed itself, named name@marketplace, is turned on in the project's
// .claude/settings.local.json (library.ProjectClaudePlugin,
// "projects/claude-plugin").
func projectPluginCmd(rest []string, usage func() error) error {
	if len(rest) == 0 {
		return usage()
	}
	verb, args := rest[0], rest[1:]
	if !slices.Contains([]string{"on", "add", "off", "rm", "remove", "list", "help", "-h", "--help"}, verb) {
		verb, args = "list", rest
	}
	if verb == "help" || verb == "-h" || verb == "--help" {
		fmt.Print("  " + libraryProjectUsage)
		return nil
	}
	if verb == "list" {
		asJSON := slices.Contains(args, "--json")
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--json" })
		if len(args) != 1 || strings.HasPrefix(args[0], "-") {
			return usage()
		}
		return listProjectPlugins(args[0], asJSON)
	}
	on := verb == "on" || verb == "add"
	if len(args) < 2 || strings.HasPrefix(args[0], "-") {
		return usage()
	}
	dir, err := projectDir(args[0])
	if err != nil {
		return err
	}
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	p := projectView(v, dir)
	if p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	// every name and agent is checked before anything is written
	var typed []string
	var agents []string
	given := false
	for _, a := range args[1:] {
		if s, ok := strings.CutPrefix(a, "agents="); ok && on {
			if agents, err = projectPluginAgents(v, s); err != nil {
				return err
			}
			given = true
			continue
		}
		if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			return usage()
		}
		typed = addOnce(typed, a)
	}
	if len(typed) == 0 {
		return usage()
	}
	type pick struct {
		name   string
		claude bool // Claude Code's own, name@marketplace
	}
	var picks []pick
	for _, n := range typed {
		name, claude, err := resolveProjectPlugin(v, p, n, on)
		if err != nil {
			return err
		}
		if claude && given && !slices.Equal(agents, []string{"claude"}) {
			return fmt.Errorf("%s is Claude Code's own plugin: only Claude Code reads a project's enabledPlugins (agents=claude)", name)
		}
		picks = append(picks, pick{name, claude})
	}
	if on && !given {
		agents = defaultPluginAgents(v, p)
	}
	if on && slices.ContainsFunc(picks, func(k pick) bool { return !k.claude }) && len(agents) == 0 {
		return fmt.Errorf("no agent here reads a project's skills, servers, subagents or hooks: name them with agents=a,b…")
	}
	var bad []string
	for _, k := range picks {
		var res *library.Result
		if k.claude {
			res, err = library.ProjectClaudePlugin(dir, k.name, on)
		} else if on {
			res, err = library.ProjectPlugin(dir, k.name, agents)
		} else {
			res, err = library.ProjectPlugin(dir, k.name, nil)
		}
		if err != nil {
			return err
		}
		if v, err = library.Read(res.Problems); err != nil {
			return err
		}
		if p = projectView(v, dir); p == nil {
			return fmt.Errorf("%s isn't a project", dir)
		}
		if why := projectPluginProblem(v, p, k.name, k.claude); why != "" {
			bad = append(bad, k.name)
			fmt.Println(amber.Render("!"), k.name, muted.Render("not written:"), why)
			continue
		}
		switch {
		case !on:
			fmt.Println(green.Render("✓"), k.name, "taken out of", dir)
		case k.claude:
			fmt.Println(green.Render("✓"), k.name, "→ claude", muted.Render(".claude/settings.local.json"))
		default:
			fmt.Println(green.Render("✓"), k.name, "→", strings.Join(p.Plugins[k.name], ", "))
		}
	}
	if why := p.Problems[""]; why != "" {
		fmt.Println(amber.Render("!"), dir+":", why)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s not written in %s", strings.Join(bad, ", "), dir)
	}
	return nil
}

// resolveProjectPlugin is the plugin a name stands for: a library plugin
// by its name, else a plugin Claude Code has installed, by its
// name@marketplace or, when only one is called so, its name alone. Off
// takes only one the project has.
func resolveProjectPlugin(v *library.View, p *library.ProjectView, n string, on bool) (string, bool, error) {
	if !strings.Contains(n, "@") {
		for _, b := range v.Plugins {
			if b.Name == n {
				if !on {
					if _, ok := p.Plugins[n]; !ok {
						return "", false, fmt.Errorf("%s has no plugin %s", p.Dir, n)
					}
				}
				return n, false, nil
			}
		}
	}
	var ids []string
	for _, c := range v.ClaudeInstalled {
		if c.ID == n || !strings.Contains(n, "@") && strings.HasPrefix(c.ID, n+"@") {
			ids = append(ids, c.ID)
		}
	}
	if !on {
		// one Claude Code has uninstalled since can still be turned off
		if slices.Contains(p.ClaudePlugins, n) {
			return n, true, nil
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return !slices.Contains(p.ClaudePlugins, id) })
		if len(ids) == 0 {
			_, ok := p.Plugins[n]
			if ok {
				return n, false, nil // a library plugin taken out of the library since
			}
			return "", false, fmt.Errorf("%s has no plugin %s on", p.Dir, n)
		}
	}
	switch len(ids) {
	case 0:
		return "", false, fmt.Errorf("no plugin called %s: neither the library nor Claude Code has it (magpie library project plugin %s lists them)", n, quoteArg(p.Dir))
	case 1:
		return ids[0], true, nil
	}
	return "", false, fmt.Errorf("%s is more than one of Claude Code's plugins (%s): name one with its @marketplace", n, strings.Join(ids, ", "))
}

// projectPluginAgents is a comma list of agents, refused when magpie knows
// of nowhere the agent reads any part of a plugin from in a project.
func projectPluginAgents(v *library.View, s string) ([]string, error) {
	out := []string{}
	for _, a := range agentList(s) {
		found := false
		for _, x := range v.Agents {
			if (strings.EqualFold(x.ID, a) || strings.EqualFold(x.Name, a)) && pluginReader(x) {
				out, found = addOnce(out, x.ID), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("magpie knows of nowhere %s reads a project's skills, servers, subagents or hooks from", a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no agent named: off takes a plugin out")
	}
	return out, nil
}

func pluginReader(a library.AgentView) bool {
	return a.ProjectSkills != "" || a.ProjectMCP != "" || a.ProjectSubagents != "" || a.ProjectHooks != ""
}

// defaultPluginAgents is who a library plugin turned on with no agents= is
// for: the agents the project's plugins are for already, else every agent
// here that reads some part of one in a project.
func defaultPluginAgents(v *library.View, p *library.ProjectView) []string {
	var all []string
	for _, a := range v.Agents {
		if pluginReader(a) && !a.Gone && !a.Unseen && a.Asleep == "" {
			all = append(all, a.ID)
		}
	}
	var used []string
	for _, ids := range p.Plugins {
		for _, id := range ids {
			if slices.Contains(all, id) && !slices.Contains(used, id) {
				used = append(used, id)
			}
		}
	}
	if len(used) > 0 {
		sort.Strings(used)
		return used
	}
	return all
}

// projectPluginProblem is why a plugin's row says "Not written" on the
// page: the first problem of one of its parts, or of Claude Code's key.
func projectPluginProblem(v *library.View, p *library.ProjectView, name string, claude bool) string {
	if claude {
		return p.Problems["claude-plugin:"+name]
	}
	for _, b := range v.Plugins {
		if b.Name != name {
			continue
		}
		var keys []string
		keys = append(keys, b.Skills...)
		for _, x := range b.MCP {
			keys = append(keys, "mcp:"+x)
		}
		for _, x := range b.Hooks {
			keys = append(keys, "hook:"+x)
		}
		for _, x := range b.Subagents {
			keys = append(keys, "subagent:"+x)
		}
		for _, k := range keys {
			if why := p.Problems[k]; why != "" {
				return why
			}
		}
	}
	return ""
}

func listProjectPlugins(typed string, asJSON bool) error {
	dir, err := projectDir(typed)
	if err != nil {
		return err
	}
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	p := projectView(v, dir)
	if p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	type item struct {
		Name   string   `json:"name"`
		Source string   `json:"source"` // "magpie" for the library's, "claude" for Claude Code's own
		On     bool     `json:"on"`
		Agents []string `json:"agents"`
	}
	items := []item{}
	names := make([]string, 0, len(v.Plugins))
	for _, b := range v.Plugins {
		names = append(names, b.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		ids := p.Plugins[n]
		if ids == nil {
			ids = []string{}
		}
		items = append(items, item{n, "magpie", len(ids) > 0, ids})
	}
	listed := map[string]bool{}
	for _, c := range v.ClaudeInstalled {
		listed[c.ID] = true
		on := slices.Contains(p.ClaudePlugins, c.ID)
		items = append(items, item{c.ID, "claude", on, claudeOnly(on)})
	}
	for _, id := range p.ClaudePlugins {
		if !listed[id] {
			items = append(items, item{id, "claude", true, claudeOnly(true)})
		}
	}
	if asJSON {
		return printJSON(map[string]any{"dir": p.Dir, "name": p.Name, "items": items})
	}
	fmt.Println(bold.Render(p.Name) + " " + muted.Render(p.Dir) + " " + muted.Render("plugins"))
	if len(items) == 0 {
		fmt.Println(" ", muted.Render("no plugins in the library or Claude Code"))
	}
	for _, it := range items {
		who := muted.Render("off")
		if it.On {
			who = "→ " + strings.Join(it.Agents, ", ")
		}
		from := ""
		if it.Source == "claude" {
			from = muted.Render(" (Claude Code's)")
		}
		fmt.Printf("  %s%s %s\n", bold.Render(it.Name), from, who)
	}
	return nil
}

func claudeOnly(on bool) []string {
	if on {
		return []string{"claude"}
	}
	return []string{}
}
