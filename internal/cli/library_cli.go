package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/yetone/magpie/internal/library"
)

const libraryUsage = `magpie library                     what the library gives each agent: instructions, MCP servers, skills
  magpie library sync                write it into the agents again (after one is installed, or edited by hand)
  magpie library instructions        print the shared instructions
  magpie library instructions set <file|->   replace them with a file's text (- for stdin)
  magpie library instructions agents <a,b…|none>   the agents that get them
  magpie library mcp add <name> <url | command args…> [agents=a,b…] [--local]
  magpie library mcp agents <name> <a,b…|none>
  magpie library mcp local <name> on|off   keep it to this computer: a sync or a backup never carries it, and one
                                     coming in neither takes it away nor puts another computer's in its place
  magpie library mcp rm <name>
  magpie library mcp router on|off   give each agent one MCP server, magpie, in place of the library's (magpie runs them, once for every agent)
  magpie library mcp router <name> shared|direct   with the router on, share the server through it (the default) or keep it each
                                     agent's own entry, as with the router off: for one that reads its project from the folder it starts in
  magpie library skill agents <name> <a,b…|none>
  magpie library skill off-here <name>   turn it off on this computer only: a sync neither carries that nor turns it back on
  magpie library skill on-here <name>    …and on again here
  magpie library skill hand-over <name…>|--all   move skills kept in ~/.agents/skills into the library's folder: the agents that read them
                                     there keep them, ticked, and you can turn each off
  magpie library skill hand-back <name>  …and back into ~/.agents/skills, where those agents read it whatever is ticked
  magpie library skill rm <name>     (skills are installed from the app's Library page)
  magpie library skill rm --all [--yes]   every skill out of the library and the agents (asks first; --yes doesn't)
  magpie library skill rm --gone [--yes]  every skill an update found removed from its source (asks first; --yes doesn't)
  magpie library skill update [name] fetch a skill from GitHub again; with no name, every one from there
                                     (one changed here is left as it is; update it by name with --replace-edits)
  magpie library skill how link|copy [agent]   give skills as links to the library's or as copies: every agent's way, or one agent's
  magpie library skill how default <agent>     the agent goes the library's way again
  magpie library skill where agents|shared     skills go to the agents ticked, or also into ~/.agents/skills for every agent and tool that reads it
  magpie library skill use-library <name> <agent>   an agent's own skill by that name is in the way: set it aside, link the library's
  magpie library skill keep-own <name> <agent>      …or keep the agent's, and take the agent off the library's
  magpie library rtk                 which agents run their shell commands through RTK (rtk-ai.app), to save tokens
  magpie library rtk on|off <agent>  switch it (on with RTK's own installer; off works with RTK gone)
  magpie library rtk install         install RTK (Homebrew, winget, or RTK's own script)
  magpie library rtk install <distro>  install RTK in a WSL distro, for the agents there (RTK's own script, run there)
  magpie library rtk upgrade         bring RTK up to its latest release, the way it was installed
  magpie library rtk path            put RTK on the PATH the agents get, when it isn't (their hooks run it by name)
`

// libraryCmd is magpie library …: the instructions, MCP servers and skills
// magpie keeps once and writes into every agent.
func libraryCmd(args []string) error {
	if len(args) < 2 {
		return libraryStatus()
	}
	var (
		res    *library.Result
		err    error
		unused string // a server just added that no agent is given
	)
	switch sub, rest := args[1], args[2:]; sub {
	case "sync":
		res, err = library.Sync()
	case "instructions":
		if len(rest) == 0 {
			v, err := library.ReadInstructions()
			if err != nil {
				return err
			}
			fmt.Print(v.Shared)
			if v.Shared != "" && !strings.HasSuffix(v.Shared, "\n") {
				fmt.Println()
			}
			return nil
		}
		switch {
		case rest[0] == "set" && len(rest) == 2:
			var b []byte
			if rest[1] == "-" {
				b, err = io.ReadAll(os.Stdin)
			} else {
				b, err = os.ReadFile(rest[1])
			}
			if err != nil {
				return err
			}
			text := string(b)
			res, err = library.SaveInstructions(library.InstructionsChange{Shared: &text})
		case rest[0] == "agents" && len(rest) == 2:
			var as []string
			if as, err = libraryAgents(rest[1], "instructions"); err == nil {
				res, err = library.SaveInstructions(library.InstructionsChange{Agents: as})
			}
		default:
			return fmt.Errorf("usage:\n  %s", libraryUsage)
		}
	case "mcp":
		switch {
		case len(rest) >= 3 && rest[0] == "add":
			var cmd, agents []string
			local := false
			for _, a := range rest[2:] {
				if a == "--local" {
					local = true
				} else if v, ok := strings.CutPrefix(a, "agents="); ok {
					if agents, err = libraryAgents(v, "mcp"); err != nil {
						return err
					}
				} else {
					cmd = append(cmd, a)
				}
			}
			var s library.Server
			if s, err = library.ServerOf(rest[1], cmd); err != nil {
				return err
			}
			if agents != nil {
				s.Agents = agents
			}
			s.Local = local
			res, err = library.SaveServer("", s)
			if len(s.Agents) == 0 {
				unused = s.Name
			}
		case len(rest) == 3 && rest[0] == "agents":
			var as []string
			if as, err = libraryAgents(rest[2], "mcp"); err == nil {
				res, err = library.ServerAgents(rest[1], as)
			}
		case len(rest) == 3 && rest[0] == "local" && (rest[2] == "on" || rest[2] == "off") && !strings.HasPrefix(rest[1], "-"):
			if res, err = library.ServerLocal(rest[1], rest[2] == "on"); err == nil && len(res.Changed) == 0 && len(res.Problems) == 0 {
				if rest[2] == "on" {
					fmt.Println(green.Render("✓"), rest[1], "is kept to this computer: a sync or a backup never carries it")
				} else {
					fmt.Println(green.Render("✓"), rest[1], "goes with the library again at the next sync")
				}
				return nil
			}
		case len(rest) == 2 && rest[0] == "rm":
			res, err = library.RemoveServer(rest[1])
		case len(rest) == 2 && (rest[0] == "router" || rest[0] == "hub") && (rest[1] == "on" || rest[1] == "off"):
			res, err = library.SetMCPHub(rest[1] == "on")
		case len(rest) == 3 && (rest[0] == "router" || rest[0] == "hub") && (rest[2] == "shared" || rest[2] == "direct") && !strings.HasPrefix(rest[1], "-"):
			// a server out of the hub, each agent's own entry (#4382)
			res, err = library.ServerHub(rest[1], rest[2] == "direct")
		default:
			return fmt.Errorf("usage:\n  %s", libraryUsage)
		}
	case "skill", "skills":
		switch {
		case len(rest) == 3 && rest[0] == "agents":
			var as []string
			if as, err = libraryAgents(rest[2], "skills"); err == nil {
				res, err = library.SkillAgents(rest[1], as)
			}
		case len(rest) >= 2 && rest[0] == "rm" && slices.Contains(rest[1:], "--gone"):
			if res, err = removeGoneSkills(rest[1:]); res == nil && err == nil {
				return nil
			}
		case len(rest) >= 2 && rest[0] == "rm" && slices.Contains(rest[1:], "--all"):
			if res, err = removeEverySkill(rest[1:]); res == nil && err == nil {
				return nil
			}
		case len(rest) == 2 && (rest[0] == "off-here" || rest[0] == "on-here") && !strings.HasPrefix(rest[1], "-"):
			res, err = library.SkillHere(rest[1], rest[0] == "on-here")
		case len(rest) >= 2 && rest[0] == "hand-over":
			if res, err = handOverSkills(rest[1:]); res == nil && err == nil {
				return nil
			}
		case len(rest) == 2 && rest[0] == "hand-back" && !strings.HasPrefix(rest[1], "-"):
			if res, err = library.HandBackSkill(rest[1]); err == nil {
				fmt.Println(green.Render("✓"), rest[1], "is back in", filepath.Join("~", ".agents", "skills", rest[1]))
			}
		case len(rest) == 2 && rest[0] == "rm":
			res, err = library.RemoveSkill(rest[1])
		case len(rest) == 3 && rest[0] == "use-library":
			res, err = library.UseLibrarySkill(rest[1], rest[2])
		case len(rest) == 3 && rest[0] == "keep-own":
			res, err = library.KeepAgentSkill(rest[1], rest[2])
		case (len(rest) == 2 || len(rest) == 3 && rest[2] == "--replace-edits") && rest[0] == "update" && !strings.HasPrefix(rest[1], "-"):
			// a skill changed here is updated only when asked to be, its
			// changed version kept with the backups (#1449)
			var edited *library.EditedError
			var gone *library.GoneError
			if res, err = library.UpdateSkill(rest[1], len(rest) == 3); err == nil {
				fmt.Println(green.Render("✓"), rest[1], "is up to date")
			} else if errors.As(err, &edited) {
				err = fmt.Errorf("%w\n  magpie library skill update %s --replace-edits  takes the fetched one in its place, keeping yours with the backups", err, rest[1])
			} else if errors.As(err, &gone) {
				// removed there: said, with how to take it out; it's kept till then
				fmt.Println(amber.Render("!"), err)
				fmt.Println(muted.Render("  magpie library skill rm " + rest[1] + "  takes it out (its folder goes to the backups)"))
				return nil
			}
		case len(rest) == 1 && rest[0] == "update":
			res, err = library.UpdateSkills()
		case (len(rest) == 2 || len(rest) == 3) && rest[0] == "how":
			how, agent := rest[1], ""
			if len(rest) == 3 {
				agent = rest[2]
			}
			if how == "default" {
				if agent == "" {
					return fmt.Errorf("usage:\n  %s", libraryUsage)
				}
				how = ""
			}
			res, err = library.SetSkillHow(agent, how)
		case len(rest) == 2 && rest[0] == "where" && (rest[1] == library.SkillsInAgents || rest[1] == library.SkillsInShared):
			res, err = library.SetSkillsWhere(rest[1])
		default:
			return fmt.Errorf("usage:\n  %s", libraryUsage)
		}
	case "rtk":
		return rtkCmd(rest)
	case "help", "-h", "--help":
		fmt.Print("  " + libraryUsage)
		return nil
	default:
		return fmt.Errorf("no library command %q\n  %s", sub, libraryUsage)
	}
	if err != nil {
		return err
	}
	if unused != "" && len(res.Changed) == 0 && len(res.Problems) == 0 {
		// a server added with no agents= is given to none, which isn't
		// every agent having it already
		fmt.Println(green.Render("✓"), unused, "is in the library, given to no agent yet")
		fmt.Println(muted.Render("  magpie library mcp agents " + unused + " <a,b…> gives it to them"))
		return nil
	}
	printLibraryResult(res)
	return nil
}

// removeEverySkill is magpie library skill rm --all [--yes] (#449): every
// skill out of the library, each as rm takes one. It asks first on a
// terminal; with no terminal to ask on, only --yes takes them. A library
// with no skills is said, and answers a nil result.
func removeEverySkill(args []string) (*library.Result, error) {
	yes := false
	for _, a := range args {
		switch a {
		case "--all":
		case "--yes", "-y":
			yes = true
		default:
			return nil, fmt.Errorf("usage:\n  %s", libraryUsage)
		}
	}
	v, err := library.Read(nil)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range v.Skills {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		fmt.Println(green.Render("✓"), "the library has no skills")
		return nil, nil
	}
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, fmt.Errorf("this removes all %s; run it with --yes to do so without being asked", plural(len(names), "skill"))
		}
		fmt.Printf("Remove all %s (%s) from the library and every agent? Folders magpie keeps go to its backups; folders of your own are only unlinked. [y/N] ",
			plural(len(names), "skill"), strings.Join(names, ", "))
		line, _ := stdin().ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return nil, fmt.Errorf("nothing removed")
		}
	}
	res, err := library.RemoveSkills(names)
	if err != nil {
		return nil, err
	}
	gone := 0
	for _, n := range names {
		if !slices.ContainsFunc(res.Unremoved, func(p library.Problem) bool { return p.What == "skill:"+n }) {
			gone++
		}
	}
	fmt.Println(green.Render("✓"), plural(gone, "skill"), "removed")
	return res, nil
}

// removeGoneSkills is magpie library skill rm --gone: every skill an
// update or a check found its source without, asked about first, naming
// the ones changed here.
func removeGoneSkills(args []string) (*library.Result, error) {
	yes := false
	for _, a := range args {
		switch a {
		case "--gone":
		case "--yes", "-y":
			yes = true
		default:
			return nil, fmt.Errorf("usage:\n  %s", libraryUsage)
		}
	}
	v, err := library.Read(nil)
	if err != nil {
		return nil, err
	}
	var names, shown []string
	for _, s := range v.Skills {
		if s.Gone == "" {
			continue
		}
		names = append(names, s.Name)
		if s.Edited {
			shown = append(shown, s.Name+" (changed here)")
		} else {
			shown = append(shown, s.Name)
		}
	}
	if len(names) == 0 {
		fmt.Println(green.Render("✓"), "no skill is removed from its source", muted.Render("(magpie library skill update finds them)"))
		return nil, nil
	}
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, fmt.Errorf("this removes %s removed from their source; run it with --yes to do so without being asked", plural(len(names), "skill"))
		}
		fmt.Printf("Remove %s removed from their source (%s) from the library and every agent? The library's folders go to magpie's backups. [y/N] ",
			plural(len(names), "skill"), strings.Join(shown, ", "))
		line, _ := stdin().ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return nil, fmt.Errorf("nothing removed")
		}
	}
	res, err := library.RemoveGoneSkills(names)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, name := range names {
		if !slices.ContainsFunc(res.Unremoved, func(p library.Problem) bool { return p.What == "skill:"+name }) {
			n++
		}
	}
	fmt.Println(green.Render("✓"), plural(n, "skill"), "removed")
	return res, nil
}

// agentList is a comma list of agents; none (or nothing) is no agent.
func agentList(s string) []string {
	out := []string{}
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" && a != "none" {
			out = append(out, a)
		}
	}
	return out
}

// libraryAgents is agentList with each agent's id, refused when the library
// has no place in it for kind.
func libraryAgents(s, kind string) ([]string, error) {
	out := []string{}
	for _, a := range agentList(s) {
		id, err := library.Takes(a, kind)
		if err != nil {
			return nil, err
		}
		// one in a stopped WSL distro is recorded, and written there once
		// the distro runs: asking would start it (#3896)
		if d := library.AsleepIn(id); d != "" {
			fmt.Fprintln(os.Stderr, amber.Render("!"), muted.Render("WSL "+d+" isn't running: "+id+" is recorded, and magpie writes it there once it sees the distro running"))
		}
		out = append(out, id)
	}
	return out, nil
}

func printLibraryResult(res *library.Result) {
	if len(res.Changed) == 0 {
		fmt.Println(green.Render("✓"), "every agent already has it")
	} else {
		fmt.Println(green.Render("✓"), "written into", strings.Join(res.Changed, ", "))
	}
	for _, p := range res.Problems {
		fmt.Println(amber.Render("!"), p.Agent, muted.Render(p.What+":"), p.Error)
		if n, ok := strings.CutPrefix(p.What, "skill:"); ok && p.Own {
			fmt.Println(muted.Render("  use the library's (the agent's kept aside): magpie library skill use-library " + n + " " + p.Agent))
			fmt.Println(muted.Render("  or keep the agent's:                       magpie library skill keep-own " + n + " " + p.Agent))
		}
	}
	if len(res.Updated) > 0 {
		fmt.Println(green.Render("✓"), "up to date:", strings.Join(res.Updated, ", "))
	}
	for _, p := range res.Unupdated {
		fmt.Println(amber.Render("!"), strings.TrimPrefix(p.What, "skill:"), muted.Render("not updated:"), p.Error)
	}
	if len(res.Gone) > 0 {
		fmt.Println(amber.Render("!"), "removed from their source:", strings.Join(res.Gone, ", "))
		fmt.Println(muted.Render("  kept here till you take them out: magpie library skill rm --gone"))
	}
	for _, p := range res.Unremoved {
		fmt.Println(amber.Render("!"), strings.TrimPrefix(p.What, "skill:"), muted.Render("not removed:"), p.Error)
	}
	for _, m := range res.Missing {
		fmt.Println(amber.Render("!"), "the library no longer has", m)
	}
	if res.Backup != "" {
		fmt.Println(muted.Render("  what was there before is kept in " + res.Backup))
	}
}

func libraryStatus() error {
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	on := func(ids []string) string {
		if len(ids) == 0 {
			return muted.Render("no agent")
		}
		return strings.Join(ids, ", ")
	}
	fmt.Println(bold.Render("Instructions"))
	var to []string
	for _, a := range v.Instructions.Agents {
		if a.On {
			to = append(to, a.Agent)
		}
	}
	if strings.TrimSpace(v.Instructions.Shared) == "" {
		fmt.Println(" ", muted.Render("none yet · magpie library instructions set <file>"))
	} else {
		fmt.Printf("  %d lines → %s\n", strings.Count(strings.TrimRight(v.Instructions.Shared, "\n"), "\n")+1, on(to))
	}
	for _, a := range v.Instructions.Agents {
		if a.Edited {
			fmt.Println(" ", amber.Render("!"), a.Agent, muted.Render("magpie's part was edited in "+a.Path))
		}
	}
	fmt.Println(bold.Render("MCP servers"))
	if len(v.Servers) == 0 {
		fmt.Println(" ", muted.Render("none yet"))
	}
	for _, s := range v.Servers {
		what := s.URL
		if s.Command != "" {
			what = strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
		}
		fmt.Printf("  %s %s → %s\n", bold.Render(s.Name), muted.Render(what), on(s.Agents))
		if s.Local {
			fmt.Println("   ", muted.Render("not synced: a sync never carries it to your other computers · magpie library mcp local "+s.Name+" off"))
		}
		if s.Hub == library.HubDirect {
			fmt.Println("   ", muted.Render("direct: each agent's own entry, never through the hub · magpie library mcp hub "+s.Name+" shared"))
		}
		for a, e := range s.Problems {
			fmt.Println("   ", amber.Render("!"), a, muted.Render(e))
		}
	}
	if v.MCPHub {
		var given []string
		keep := map[string][]string{}
		for _, a := range v.Agents {
			switch a.Hub {
			case "":
			case "on":
				given = append(given, a.ID)
			default:
				keep[a.Hub] = append(keep[a.Hub], a.ID)
			}
		}
		fmt.Printf("  %s → %s\n", bold.Render("magpie")+muted.Render(" (one server in place of these, at "+v.HubPath+"<agent>)"), on(given))
		for _, why := range []string{"no-remote", "taken", "own"} {
			if len(keep[why]) == 0 {
				continue
			}
			say := map[string]string{
				"no-remote": "reaches no MCP server over HTTP, so it keeps each server's own entry",
				"taken":     "a library server is called magpie, so each agent keeps each server's own entry",
				"own":       "has an MCP server called magpie of its own, so it keeps each server's own entry",
			}[why]
			fmt.Println("   ", amber.Render("!"), strings.Join(keep[why], ", "), muted.Render(say))
		}
	}
	var found, own []string
	for _, f := range v.FoundServers {
		if f.Own {
			own = append(own, f.Server.Name+muted.Render(" ("+strings.Join(f.Server.Agents, ", ")+"'s own)"))
		} else {
			found = append(found, f.Server.Name+muted.Render(" ("+strings.Join(f.Server.Agents, ", ")+")"))
		}
	}
	if len(found) > 0 {
		fmt.Println(" ", muted.Render("in your agents, not in the library:"), strings.Join(found, ", "))
	}
	if len(own) > 0 {
		fmt.Println(" ", muted.Render("added by the agent itself, left as they are:"), strings.Join(own, ", "))
	}
	fmt.Println(bold.Render("Skills"))
	if len(v.Skills) == 0 {
		fmt.Println(" ", muted.Render("none yet"))
	}
	for _, s := range v.Skills {
		fmt.Printf("  %s → %s\n", bold.Render(s.Name), on(s.Agents))
		if s.Missing {
			fmt.Println("   ", amber.Render("!"), muted.Render("its folder is gone"))
		}
		if s.Gone != "" {
			fmt.Println("   ", amber.Render("!"), muted.Render("removed from "+s.Source+" · magpie library skill rm --gone"))
		}
		if s.OffHere {
			fmt.Println("   ", muted.Render("off on this computer · magpie library skill on-here "+s.Name))
		}
	}
	fmt.Println(muted.Render("  kept in " + v.Dir + " · magpie library help"))
	return nil
}

// rtkCmd is magpie library rtk …: RTK's hook in each agent.
func rtkCmd(args []string) error {
	var v *library.RTKView
	switch {
	case len(args) == 0:
		v = library.ReadRTK()
	case len(args) == 1 && args[0] == "install":
		fmt.Println(muted.Render("installing rtk…"))
		var err error
		if v, err = library.InstallRTK(); err != nil {
			return err
		}
	case len(args) == 2 && args[0] == "install":
		fmt.Println(muted.Render("installing rtk in WSL " + args[1] + "…"))
		var err error
		if v, err = library.InstallRTKIn(args[1]); err != nil {
			return err
		}
	case len(args) == 1 && args[0] == "path":
		var err error
		if v, err = library.PathRTK(); err != nil {
			return err
		}
	case len(args) == 1 && args[0] == "upgrade":
		fmt.Println(muted.Render("upgrading rtk…"))
		var err error
		if v, err = library.UpgradeRTK(); err != nil {
			return err
		}
	case len(args) == 2 && (args[0] == "on" || args[0] == "off"):
		id, err := library.RTKTakes(args[1])
		if err != nil {
			return err
		}
		if v, err = library.SetRTK(id, args[0] == "on"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("usage:\n  %s", libraryUsage)
	}
	if v.Path == "" {
		fmt.Println(amber.Render("!"), "rtk isn't installed —", v.URL)
		if v.Install != "" {
			fmt.Println(muted.Render("  magpie library rtk install runs: " + v.Install))
		}
	} else {
		fmt.Println(bold.Render("RTK"), muted.Render(v.Version+" · "+v.Path))
		v.CheckLatest()
		switch {
		case v.Note != "":
			fmt.Println(amber.Render("  " + v.Note))
		case v.Waiting != "":
			fmt.Println(amber.Render("  RTK "+v.Latest+" is out · waiting for "+v.Waiting), muted.Render("— "+v.Waiting+" has "+v.WaitingHas+" so far, and usually has a release within a few days"))
		case v.Latest != "" && v.Version != "" && library.RTKNewer(v.Latest, v.Version):
			up := "update it the way it was installed"
			switch {
			case v.Upgrade != "":
				up = "magpie library rtk upgrade runs: " + v.Upgrade
			case v.NoUpgrade == "cargo":
				up = "it was installed with cargo, but magpie can't find cargo: install Rust from https://rustup.rs, or run " + v.Manual
			}
			fmt.Println(amber.Render("  RTK "+v.Latest+" is out"), muted.Render("— "+up))
		case len(args) == 1 && args[0] == "upgrade":
			fmt.Println(green.Render("  up to date"))
		}
		if v.OffPath {
			fmt.Println(amber.Render("  rtk isn't on your PATH"), muted.Render("— the agents' hooks run it by name, so RTK does nothing for them"))
			switch {
			case v.PathDir == "":
				fmt.Println(muted.Render("  add " + filepath.Dir(v.Path) + " to PATH in your shell profile"))
			case v.PathLink:
				fmt.Println(muted.Render("  magpie library rtk path links it into " + v.PathDir))
			default:
				fmt.Println(muted.Render("  magpie library rtk path adds " + v.PathDir + " to your user PATH"))
			}
		}
		switch g := v.Gain; {
		case g != nil:
			fmt.Printf("  %d tokens saved over %d commands (%.0f%% on average)\n", g.Saved, g.Commands, g.Pct)
		case v.GainErr != "":
			fmt.Println(amber.Render("  what rtk saved isn't known:"), muted.Render(v.GainErr))
		}
		switch v.CodexSandbox {
		case "elevated":
			fmt.Println(muted.Render("  Codex runs its commands in its Windows sandbox as its own account, so what RTK saves there isn't counted"))
		case "unelevated":
			fmt.Println(muted.Render("  Codex runs its commands in its Windows sandbox, which can't write RTK's history, so what RTK saves there isn't counted"))
		}
	}
	// an agent in WSL runs its distro's rtk
	wslRTK := map[string]library.RTKDistro{}
	for _, d := range v.WSL {
		wslRTK[d.Name] = d
		switch {
		case d.Asleep:
			fmt.Println(muted.Render("  WSL " + d.Name + " isn't running: its agents are left as they are, and magpie doesn't start it"))
		case d.Path == "" && d.Install != "":
			fmt.Println(amber.Render("  rtk isn't installed in WSL "+d.Name), muted.Render("— magpie library rtk install "+d.Name+" runs there: "+d.Install))
		case d.Path == "":
			fmt.Println(amber.Render("  magpie couldn't look for rtk in WSL " + d.Name))
		case d.OffPath:
			fmt.Println(bold.Render("  WSL "+d.Name), muted.Render(d.Version+" · "+d.Path))
			fmt.Println(amber.Render("  rtk isn't on the PATH of WSL "+d.Name+"'s shells"), muted.Render("— add "+d.Path[:strings.LastIndex(d.Path, "/")]+" to PATH in your shell profile there"))
		default:
			fmt.Println(bold.Render("  WSL "+d.Name), muted.Render(d.Version+" · "+d.Path))
		}
	}
	for _, a := range v.Agents {
		mark := muted.Render("off")
		note := ""
		if a.NoHook {
			mark, note = muted.Render(" – "), muted.Render(" — "+a.Blocked)
		}
		path, offPath := v.Path, v.OffPath
		if a.WSL != "" {
			path, offPath = wslRTK[a.WSL].Path, wslRTK[a.WSL].OffPath
		}
		if a.Asleep {
			mark, note = muted.Render(" ? "), muted.Render(" — WSL "+a.WSL+" isn't running")
		}
		if a.On {
			mark = green.Render("on ")
			if path == "" {
				mark, note = amber.Render("on "), muted.Render(" — its hook calls rtk, which isn't installed: install it, or switch this off")
			} else if offPath {
				mark, note = amber.Render("on "), muted.Render(" — its hook can't find rtk on PATH, so it does nothing")
			}
		}
		fmt.Println(" ", mark, a.Name+note)
	}
	if len(v.Agents) == 0 {
		fmt.Println(" ", muted.Render("none of the agents here is one RTK has a hook for"))
	}
	if len(v.Restart) > 0 {
		fmt.Println(muted.Render("  restart " + strings.Join(v.Restart, ", ") + " for it to take effect"))
	}
	if v.Backup != "" {
		fmt.Println(muted.Render("  what was there before is kept in " + v.Backup))
	}
	return nil
}

// handOverSkills is magpie library skill hand-over: the skills named, or
// with --all every one the library keeps in ~/.agents/skills, moved into
// the library's folder. Each moved is said, and where it went.
func handOverSkills(args []string) (*library.Result, error) {
	names := args
	if slices.Contains(args, "--all") {
		if len(args) != 1 {
			return nil, fmt.Errorf("usage:\n  %s", libraryUsage)
		}
		var err error
		if names, err = library.SharedSkills(); err != nil {
			return nil, err
		}
		if len(names) == 0 {
			fmt.Println(muted.Render("No skill of the library's is kept in ~/.agents/skills."))
			return nil, nil
		}
	} else if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") }) {
		return nil, fmt.Errorf("usage:\n  %s", libraryUsage)
	}
	res, err := library.HandOverSkills(names)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if slices.ContainsFunc(res.Unmoved, func(p library.Problem) bool { return p.What == "skill:"+n }) {
			continue
		}
		fmt.Println(green.Render("✓"), n, "is in the library's folder:", library.SkillPath(n))
	}
	for _, p := range res.Unmoved {
		fmt.Println(amber.Render("!"), strings.TrimPrefix(p.What, "skill:"), "stays where it was:", p.Error)
	}
	return res, nil
}
