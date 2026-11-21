package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/settings"
)

const libraryPluginUsage = `magpie library plugin [list] [--json]   the plugins in the library, and the agents each is given to
  magpie library plugin import <source> [plugin…] [agents=a,b…|none]   Library › Claude plugins › Find plugins and
                                     Install: <source> is a folder here (a plugin's, a Pi package's or a marketplace's),
                                     owner/repo, or a git address. A marketplace's plugins are named after it; with no
                                     agents= the plugin goes to every agent the Library shows, as the page does, and
                                     agents=none keeps it in the library for projects alone
                                     (magpie library project plugin on <dir> <plugin> gives it to one)
`

// libraryPluginCmd is magpie library plugin … (#7429): the Library's Claude
// plugins card from a terminal. import goes through library.InstallPlugins,
// the call the page's Install makes ("plugins/install").
func libraryPluginCmd(rest []string) error {
	usage := func() error { return fmt.Errorf("usage:\n  %s", libraryPluginUsage) }
	if slices.ContainsFunc(rest, func(a string) bool { return a == "-h" || a == "--help" || a == "help" }) {
		fmt.Print("  " + libraryPluginUsage)
		return nil
	}
	if len(rest) == 0 || rest[0] == "list" || rest[0] == "--json" {
		args := rest
		if len(args) > 0 && args[0] == "list" {
			args = args[1:]
		}
		switch {
		case len(args) == 0:
			return listLibraryPlugins(false)
		case len(args) == 1 && args[0] == "--json":
			return listLibraryPlugins(true)
		}
		return usage()
	}
	if rest[0] != "import" && rest[0] != "install" && rest[0] != "add" {
		return fmt.Errorf("no library plugin command %q\n  %s", rest[0], libraryPluginUsage)
	}
	args := rest[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || strings.Contains(args[0], "=") && !strings.Contains(args[0], "://") {
		return usage()
	}
	source := pluginSource(args[0])
	// every word is checked before anything is fetched or written
	var names []string
	var agents []string
	given := false
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	for _, a := range args[1:] {
		if s, ok := strings.CutPrefix(a, "agents="); ok {
			if given {
				return usage()
			}
			if agents, err = libraryPluginAgents(v, s); err != nil {
				return err
			}
			given = true
			continue
		}
		if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			return usage()
		}
		names = addOnce(names, a)
	}
	if !given {
		agents = shownLibraryAgents(v)
	}
	res, err := library.InstallPlugins(source, names, agents)
	if err != nil {
		return err
	}
	if len(res.Installed) == 0 {
		return errors.New("nothing was installed")
	}
	if v, err = library.Read(res.Problems); err != nil {
		return err
	}
	for _, n := range res.Installed {
		to := muted.Render(", given to no agent yet")
		for _, b := range v.Plugins {
			if b.Name == n && len(b.Agents) > 0 {
				to = " → " + strings.Join(b.Agents, ", ")
			}
		}
		fmt.Println(green.Render("✓"), n, "is in the library"+to)
	}
	for _, p := range res.Skipped {
		fmt.Println(amber.Render("!"), p.Agent, muted.Render(p.What+":"), p.Error)
	}
	for _, p := range res.Problems {
		fmt.Println(amber.Render("!"), p.Agent, muted.Render(p.What+":"), p.Error)
	}
	fmt.Println(muted.Render("  magpie library project plugin on <dir> " + res.Installed[0] + "  gives it to a project"))
	return nil
}

// pluginSource is what the page's field would be given for a source typed
// here: a folder or file relative to the one you're in is made whole, as the
// page takes only a whole path for a folder (anything else is an address).
// One starting . or .. is a folder even when it isn't there, so it is never
// fetched as owner/repo.
func pluginSource(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || filepath.IsAbs(s) || strings.HasPrefix(s, "~") || strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
		return s
	}
	dotted := s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, `.\`) || strings.HasPrefix(s, `..\`)
	if _, err := os.Stat(s); err != nil && !dotted {
		return s
	}
	if abs, err := filepath.Abs(s); err == nil {
		return abs
	}
	return s
}

// shownLibraryAgents is who the page's Install gives a plugin to unless
// changed: every agent the Library shows (not gone, not unseen, not hidden).
func shownLibraryAgents(v *library.View) []string {
	hidden := settings.Load().AgentsHidden
	out := []string{}
	for _, a := range v.Agents {
		if !a.Gone && !a.Unseen && !slices.Contains(hidden, a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}

// libraryPluginAgents is a comma list of agents the Library knows, by id or
// name; none (or nothing) is no agent.
func libraryPluginAgents(v *library.View, s string) ([]string, error) {
	out := []string{}
	for _, a := range agentList(s) {
		i := slices.IndexFunc(v.Agents, func(x library.AgentView) bool {
			return strings.EqualFold(x.ID, a) || strings.EqualFold(x.Name, a)
		})
		if i < 0 {
			return nil, fmt.Errorf("the library gives nothing to an agent called %s", a)
		}
		out = addOnce(out, v.Agents[i].ID)
	}
	return out, nil
}

func listLibraryPlugins(asJSON bool) error {
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	type item struct {
		Name       string   `json:"name"`
		Version    string   `json:"version,omitempty"`
		Source     string   `json:"source,omitempty"`
		On         bool     `json:"on"`
		Agents     []string `json:"agents"`
		Skills     []string `json:"skills,omitempty"`
		MCP        []string `json:"mcp,omitempty"`
		Hooks      []string `json:"hooks,omitempty"`
		Subagents  []string `json:"subagents,omitempty"`
		Extensions []string `json:"extensions,omitempty"`
	}
	items := []item{}
	for _, b := range v.Plugins {
		ag := b.Agents
		if ag == nil {
			ag = []string{}
		}
		items = append(items, item{b.Name, b.Version, b.Source, !b.Off, ag, b.Skills, b.MCP, b.Hooks, b.Subagents, b.Extensions})
	}
	slices.SortFunc(items, func(a, b item) int { return strings.Compare(a.Name, b.Name) })
	if asJSON {
		return printJSON(map[string]any{"plugins": items})
	}
	fmt.Println(bold.Render("Plugins"))
	if len(items) == 0 {
		fmt.Println(" ", muted.Render("none yet · magpie library plugin import <folder | owner/repo | git address>"))
	}
	for _, it := range items {
		who := muted.Render("no agent")
		switch {
		case !it.On:
			who = muted.Render("off")
		case len(it.Agents) > 0:
			who = strings.Join(it.Agents, ", ")
		}
		var parts []string
		for _, p := range []struct {
			n    int
			what string
		}{{len(it.Skills), "skill"}, {len(it.MCP), "MCP server"}, {len(it.Hooks), "hook"}, {len(it.Subagents), "subagent"}, {len(it.Extensions), "Pi extension"}} {
			if p.n == 1 {
				parts = append(parts, "1 "+p.what)
			} else if p.n > 1 {
				parts = append(parts, fmt.Sprintf("%d %ss", p.n, p.what))
			}
		}
		fmt.Printf("  %s %s → %s\n", bold.Render(it.Name), muted.Render(strings.Join(parts, ", ")), who)
		if it.Source != "" {
			fmt.Println("   ", muted.Render(it.Source))
		}
	}
	return nil
}
