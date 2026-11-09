// Package tools serves the tools the user's plugins give (their OpenCode
// tool hook) to every agent, over magpie's plugin tools MCP server
// (internal/gateway/tools_mcp.go, docs/subsystems/plugin-tools.md). While
// a plugin that is on gives a tool, it stays on.
//
// It serves gwtool.PluginTools on the gateway's tool point (P4 of
// docs/subsystems/plugin-runtime.md). Off, the server lists no tool and a
// call is answered that the plugin is off; the plugins' other hooks (a
// provider, auth) go on as before.
package tools

import (
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/pluginrt"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "tools", API: "^1", Requires: []string{"gateway"}, Essential: true,
		Name: "Plugin tools", Sub: "Offer the tools your plugins give to every agent, as an MCP server",
		Opens: "plugins",
		One:   "1 plugin gives tools", Many: "{n} plugins give tools",
		InUse: plugin.ToolsOn,
		Apply: func(c *pluginrt.Ctx) error {
			c.OnDispose(gwtool.Serve(gwtool.PluginTools))
			return nil
		},
	})
}
