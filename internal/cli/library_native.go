package cli

import (
	"fmt"

	"github.com/yetone/magpie/internal/library"
)

// libraryNativeCmd is magpie library native: what the plugins Codex and
// Claude Code (#7266) and the packages Pi installed themselves declare. It reads and writes
// nothing; the agent loads them, and whether one is loaded in a session
// isn't known from its files.
func libraryNativeCmd(rest []string) error {
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
		for _, x := range p.Parts {
			fmt.Printf("    %-8s %s", x.Kind, x.Name)
			if x.Detail != "" && x.Kind != "skill" {
				fmt.Printf("  %s", x.Detail)
			}
			fmt.Println()
		}
	}
	return nil
}
