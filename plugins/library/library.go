// Package library is the Library page: instructions, MCP servers, skills
// and hooks, kept once and given to every agent. While it gives agents
// anything, it stays on.
package library

import (
	lib "github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/pluginrt"
)

// Settings are the page's settings, at their defaults. Hide is the page's
// tabs the reader left out of it, their ids comma separated
// (instructions, memory, mcp, skills, hooks, env, subagents, commands,
// rules, plugins, rtk, bili, pi): the window's alone, so the library gives
// agents what a hidden tab holds as before.
type Settings struct {
	Hide string `json:"hide" title:"Hidden sections" description:"The Library's tabs left out of its page; agents are given what they hold as before"`
}

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "library", API: "^1", Requires: []string{"window"},
		Name: "Library", Sub: "Instructions, MCP servers, skills and hooks, kept once and given to every agent",
		One: "1 item given to agents", Many: "{n} items given to agents",
		InUse:    lib.InUse,
		Parts:    []pluginrt.Part{{Slot: "nav.page", Key: "library"}},
		Settings: Settings{},
		Client:   []string{"i18n.js", "library.css", "client.js"},
	})
}
