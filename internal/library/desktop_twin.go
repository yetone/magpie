package library

// Claude Magpie, the isolated Desktop, is an app of its own to the library
// (agent.DesktopTwinApp, xMopf7RF on X). Before, Claude Desktop's servers
// and Cowork skills were written into its folder too, as Claude Desktop's:
// carryTwin records each of those still there as given to Claude Magpie,
// ticked for it, so it keeps what it has and each can then be taken from
// one app and not the other.

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/agent"
)

// carryTwin ticks for Claude Magpie each server and skill magpie gave
// Claude Desktop that is in Claude Magpie's folder, while nothing is
// recorded for Claude Magpie yet: once, as what magpie wrote is recorded
// from then on.
func (l *Library) carryTwin() {
	desk := l.Applied["claude-desktop"]
	if desk == nil || len(desk.MCP) == 0 && len(desk.Skills) == 0 || l.Applied[agent.DesktopTwinID] != nil {
		return
	}
	tw := agent.DesktopTwinApp(home())
	if tw == nil || !isDir(tw.Dir) {
		return
	}
	got := &Applied{}
	if b, err := os.ReadFile(tw.Path); err == nil {
		servers := gjson.GetBytes(b, "mcpServers")
		for _, name := range desk.MCP {
			if servers.IsObject() && servers.Get(gjson.Escape(name)).Exists() {
				got.MCP = append(got.MCP, name)
				if s := l.server(name); s != nil && !slices.Contains(s.Agents, tw.ID) {
					s.Agents = append(s.Agents, tw.ID)
				}
			}
		}
	}
	roots := desktopSkillRootsIn([]string{tw.Dir})
	for _, name := range desk.Skills {
		if !slices.ContainsFunc(roots, func(r string) bool { return isDir(filepath.Join(r, "skills", name)) }) {
			continue
		}
		got.Skills = append(got.Skills, name)
		if s := l.skill(name); s != nil && !slices.Contains(s.Agents, tw.ID) {
			s.Agents = append(s.Agents, tw.ID)
		}
	}
	if !got.empty() {
		l.Applied[tw.ID] = got
	}
}
