package cli

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

// libraryNativeCmd is magpie library native: what the plugins Codex and
// Claude Code (#7266) and the packages Pi installed themselves declare. It
// writes nothing; the agent loads them, and whether one is loaded in a
// session isn't known from its files. copy puts one of their MCP servers
// in the library.
func libraryNativeCmd(rest []string) error {
	if len(rest) >= 2 && len(rest) <= 3 && rest[0] == "copy" {
		return libraryNativeCopy(rest[1], rest[2:])
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage:\n  %s", libraryUsage)
	}
	v := library.ReadNative()
	if len(v) == 0 {
		fmt.Println(muted.Render("no plugins or packages installed through Codex, Claude Code or Pi"))
		return nil
	}
	for _, p := range v {
		where := p.Scope
		if p.Project != "" {
			where += " " + p.Project
		}
		state := "state unknown"
		if p.Enabled != nil {
			state = map[bool]string{true: "on", false: "off"}[*p.Enabled] + " in its settings"
		}
		fmt.Printf("%s  %s %s  (%s, %s)\n", p.AgentName, p.Name, p.Version, where, state)
		// a project's own config turning it on or off (#7244): Codex
		// applies it once the project is trusted
		onOff := func(b *bool) string {
			if b == nil {
				return "unknown"
			}
			return map[bool]string{true: "on", false: "off"}[*b]
		}
		for _, in := range p.InProjects {
			if in.Trusted {
				fmt.Printf("    in %s: %s (its .codex/config.toml; loaded unknown)\n", in.Dir, onOff(in.Effective))
			} else {
				fmt.Printf("    in %s: %s (its .codex/config.toml says %s, used once Codex trusts the project)\n", in.Dir, onOff(in.Effective), onOff(in.Set))
			}
		}
		for _, x := range p.Parts {
			fmt.Printf("    %-8s %s", x.Kind, x.Name)
			if x.Detail != "" && x.Kind != "skill" {
				fmt.Printf("  %s", x.Detail)
			}
			if x.NoCopy != "" {
				fmt.Print(muted.Render("  not copied: " + library.NoCopyWhy(x.NoCopy, p.AgentName)))
			}
			fmt.Println()
		}
	}
	return nil
}

// libraryNativeCopy is magpie library native copy <server> [agent], the
// Library's Copy to the library (0xRichardH on X): the MCP server an
// agent's own plugin declares, as a library server given to no agent yet.
// A name more than one plugin declares needs the agent, and one plugin.
func libraryNativeCopy(name string, agent []string) error {
	type at struct{ agent, dir, of string }
	var found []at
	for _, p := range library.ReadNative() {
		if len(agent) == 1 && p.Agent != agent[0] {
			continue
		}
		for _, x := range p.Parts {
			if x.Kind == "mcp" && x.Name == name {
				found = append(found, at{p.Agent, p.Dir, p.AgentName + " · " + p.Name})
			}
		}
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("no plugin an agent installed itself declares a server called %s (magpie library native lists them)", name)
	case 1:
	default:
		var ofs []string
		for _, f := range found {
			ofs = append(ofs, f.of)
		}
		return fmt.Errorf("%s is declared by %s: say which agent's (magpie library native copy %s <agent>)", name, strings.Join(ofs, ", "), name)
	}
	res, err := library.CopyNativeServer(found[0].agent, found[0].dir, name)
	if err != nil {
		return err
	}
	if len(res.Problems) > 0 {
		printLibraryResult(res)
		return nil
	}
	fmt.Println(green.Render("✓"), name, "is in the library, given to no agent yet")
	fmt.Println(muted.Render("  magpie library mcp agents " + name + " <a,b…> gives it to them"))
	return nil
}
