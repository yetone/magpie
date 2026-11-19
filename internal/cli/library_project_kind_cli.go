package cli

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

// projectKind is one of the library's resources a project takes besides
// skills: the same add, rm and agents verbs over the calls the Library page
// makes (#7248, jinpwng on X).
type projectKind struct {
	word    string // "server": the command word and what is said of it
	plural  string
	reads   string // what an agent reads it from, for messages
	takes   string // libraryAgents' kind
	folder  func(a library.AgentView) string
	of      func(p *library.ProjectView) map[string][]string
	set     func(dir string, names, agents []string) (*library.Result, error)
	problem string // prefix of a Problems key
}

var projectServers = projectKind{
	word: "server", plural: "servers", reads: "MCP servers", takes: "mcp", problem: "mcp:",
	folder: func(a library.AgentView) string { return a.ProjectMCP },
	of:     func(p *library.ProjectView) map[string][]string { return p.Servers },
	set: func(dir string, names, agents []string) (*library.Result, error) {
		var last *library.Result
		for _, n := range names {
			res, err := library.ProjectServer(dir, n, agents)
			if err != nil {
				return nil, err
			}
			last = res
		}
		return last, nil
	},
}

var projectSubagents = projectKind{
	word: "subagent", plural: "subagents", reads: "subagents", takes: "mcp", problem: "subagent:",
	folder: func(a library.AgentView) string { return a.ProjectSubagents },
	of:     func(p *library.ProjectView) map[string][]string { return p.Subagents },
	set:    library.ProjectSubagents,
}

var projectHooks = projectKind{
	word: "hook", plural: "hooks", reads: "hooks", problem: "hook:",
	folder: func(a library.AgentView) string { return a.ProjectHooks },
	of:     func(p *library.ProjectView) map[string][]string { return p.Hooks },
	set:    library.ProjectHooks,
}

// agentsOf is a comma list of agents, none for no agent, each refused when
// magpie knows of no place it reads this kind from in a project.
func (k projectKind) agentsOf(v *library.View, s string) ([]string, error) {
	out := []string{}
	for _, a := range agentList(s) {
		id := strings.ToLower(a)
		found := false
		for _, x := range v.Agents {
			if (strings.EqualFold(x.ID, a) || strings.EqualFold(x.Name, a)) && k.folder(x) != "" {
				id, found = x.ID, true
			}
		}
		if !found {
			return nil, fmt.Errorf("magpie knows of no place %s reads a project's %s from", a, k.reads)
		}
		out = addOnce(out, id)
	}
	return out, nil
}

// defaults is who a resource added with no agents= is for, as the page's
// menu does: the agents the project's ones are for already, else every
// agent here that reads a project's.
func (k projectKind) defaults(v *library.View, p *library.ProjectView) []string {
	var all []string
	for _, a := range v.Agents {
		if k.folder(a) != "" && !a.Gone && !a.Unseen && a.Asleep == "" {
			all = append(all, a.ID)
		}
	}
	var used []string
	for _, ids := range k.of(p) {
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

// projectKindCmd is magpie library project server|subagent …
func projectKindCmd(k projectKind, rest []string, usage func() error) error {
	if len(rest) == 0 {
		return usage()
	}
	verb, args := rest[0], rest[1:]
	if !slices.Contains([]string{"add", "rm", "remove", "agents", "list", "help", "-h", "--help"}, verb) {
		verb, args = "list", rest
	}
	switch verb {
	case "help", "-h", "--help":
		fmt.Print("  " + libraryProjectUsage)
		return nil
	case "list":
		asJSON := slices.Contains(args, "--json")
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--json" })
		if len(args) != 1 || strings.HasPrefix(args[0], "-") {
			return usage()
		}
		return k.list(args[0], asJSON)
	}
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || verb == "agents" && len(args) != 3 {
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
	var names, agents []string
	switch verb {
	case "agents":
		if _, ok := k.of(p)[args[1]]; !ok {
			return fmt.Errorf("%s has no %s %s", dir, k.word, args[1])
		}
		if strings.HasPrefix(args[2], "-") {
			return usage()
		}
		if agents, err = k.agentsOf(v, args[2]); err != nil {
			return err
		}
		names = []string{args[1]}
	case "rm", "remove":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				return usage()
			}
			if _, ok := k.of(p)[a]; !ok {
				return fmt.Errorf("%s has no %s %s", dir, k.word, a)
			}
			names = addOnce(names, a)
		}
	default:
		given := false
		for _, a := range args[1:] {
			if s, ok := strings.CutPrefix(a, "agents="); ok {
				if agents, err = k.agentsOf(v, s); err != nil {
					return err
				}
				given = true
				continue
			}
			if strings.HasPrefix(a, "-") {
				return usage()
			}
			names = addOnce(names, a)
		}
		if !given {
			agents = k.defaults(v, p)
		}
		if len(agents) == 0 {
			return fmt.Errorf("no agent here reads a project's %s: name them with agents=a,b…", k.reads)
		}
	}
	res, err := k.set(dir, names, agents)
	if err != nil {
		return err
	}
	v, err = library.Read(res.Problems)
	if err != nil {
		return err
	}
	if p = projectView(v, dir); p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	if why := p.Problems[""]; why != "" {
		fmt.Println(amber.Render("!"), dir+":", why)
	}
	if len(agents) == 0 {
		fmt.Println(green.Render("✓"), strings.Join(names, ", "), "taken out of", dir)
		return nil
	}
	var bad []string
	for _, n := range names {
		key := k.problem + n
		if why := p.Problems[key]; why != "" {
			bad = append(bad, n)
			fmt.Println(amber.Render("!"), n, muted.Render("not placed:"), why)
			continue
		}
		fmt.Println(green.Render("✓"), n, "→", strings.Join(k.of(p)[n], ", "))
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s not placed in %s", strings.Join(bad, ", "), dir)
	}
	return nil
}

func (k projectKind) list(typed string, asJSON bool) error {
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
	have := k.of(p)
	if asJSON {
		return printJSON(map[string]any{"dir": p.Dir, "name": p.Name, k.plural: have, "items": k.items(p)})
	}
	names := make([]string, 0, len(have))
	for n := range have {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println(bold.Render(p.Name) + " " + muted.Render(p.Dir) + " " + muted.Render(k.plural))
	if len(names) == 0 {
		fmt.Println(" ", muted.Render("no "+k.plural+" yet · magpie library project "+k.word+" add "+quoteArg(p.Dir)+" <"+k.word+"…>"))
	}
	for _, n := range names {
		fmt.Printf("  %s → %s\n", bold.Render(n), strings.Join(have[n], ", "))
	}
	if own := p.Own[k.plural]; len(own) > 0 {
		fmt.Println(" ", muted.Render("the project's own, read-only:"))
		for _, r := range own {
			fmt.Printf("  %s %s\n", r.Name, muted.Render(strings.TrimSpace(r.Event+" "+r.Path)))
		}
	}
	return nil
}

// projectItem is one row of a project's list, with where it comes from:
// source "magpie" for what magpie placed (agents says for whom), "project"
// for what the project's own files give (read-only, #7247).
type projectItem struct {
	Name        string   `json:"name"`
	Source      string   `json:"source"`
	Agents      []string `json:"agents,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	Path        string   `json:"path,omitempty"`
	Event       string   `json:"event,omitempty"`
	Transport   string   `json:"transport,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (k projectKind) items(p *library.ProjectView) []projectItem {
	have := k.of(p)
	names := make([]string, 0, len(have))
	for n := range have {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []projectItem{}
	for _, n := range names {
		out = append(out, projectItem{Name: n, Source: "magpie", Agents: have[n]})
	}
	for _, r := range p.Own[k.plural] {
		out = append(out, projectItem{Name: r.Name, Source: "project", Agent: r.Agent, Path: r.Path, Event: r.Event, Transport: r.Transport, Description: r.Description})
	}
	return out
}

var _ = os.Stdout
