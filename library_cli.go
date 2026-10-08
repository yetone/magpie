package main

import (
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
  magpie library mcp add <name> <url | command args…> [agents=a,b…]
  magpie library mcp agents <name> <a,b…|none>
  magpie library mcp rm <name>
  magpie library skill agents <name> <a,b…|none>
  magpie library skill rm <name>     (skills are installed from the app's Library page)
  magpie library skill rm --all [--yes]   every skill out of the library and the agents (asks first; --yes doesn't)
  magpie library skill update [name] fetch a skill from GitHub again; with no name, every one from there
  magpie library skill how link|copy [agent]   give skills as links to the library's or as copies: every agent's way, or one agent's
  magpie library skill how default <agent>     the agent goes the library's way again
  magpie library skill use-library <name> <agent>   an agent's own skill by that name is in the way: set it aside, link the library's
  magpie library skill keep-own <name> <agent>      …or keep the agent's, and take the agent off the library's
  magpie library rtk                 which agents run their shell commands through RTK (rtk-ai.app), to save tokens
  magpie library rtk on|off <agent>  switch it (on with RTK's own installer; off works with RTK gone)
  magpie library rtk install         install RTK (Homebrew, winget, or RTK's own script)
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
		res *library.Result
		err error
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
			for _, a := range rest[2:] {
				if v, ok := strings.CutPrefix(a, "agents="); ok {
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
			res, err = library.SaveServer("", s)
		case len(rest) == 3 && rest[0] == "agents":
			var as []string
			if as, err = libraryAgents(rest[2], "mcp"); err == nil {
				res, err = library.ServerAgents(rest[1], as)
			}
		case len(rest) == 2 && rest[0] == "rm":
			res, err = library.RemoveServer(rest[1])
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
		case len(rest) >= 2 && rest[0] == "rm" && slices.Contains(rest[1:], "--all"):
			if res, err = removeEverySkill(rest[1:]); res == nil && err == nil {
				return nil
			}
		case len(rest) == 2 && rest[0] == "rm":
			res, err = library.RemoveSkill(rest[1])
		case len(rest) == 3 && rest[0] == "use-library":
			res, err = library.UseLibrarySkill(rest[1], rest[2])
		case len(rest) == 3 && rest[0] == "keep-own":
			res, err = library.KeepAgentSkill(rest[1], rest[2])
		case len(rest) == 2 && rest[0] == "update":
			if res, err = library.UpdateSkill(rest[1]); err == nil {
				fmt.Println(green.Render("✓"), rest[1], "is up to date")
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
		line, _ := stdin.ReadString('\n')
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
		for a, e := range s.Problems {
			fmt.Println("   ", amber.Render("!"), a, muted.Render(e))
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
			if v.Upgrade != "" {
				up = "magpie library rtk upgrade runs: " + v.Upgrade
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
	for _, a := range v.Agents {
		mark := muted.Render("off")
		note := ""
		if a.NoHook {
			mark, note = muted.Render(" – "), muted.Render(" — "+a.Blocked)
		}
		if a.On {
			mark = green.Render("on ")
			if v.Path == "" {
				mark, note = amber.Render("on "), muted.Render(" — its hook calls rtk, which isn't installed: install it, or switch this off")
			} else if v.OffPath {
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
