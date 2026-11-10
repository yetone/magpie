package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
)

// The tools the user's plugins give (OpenCode's tool hook: {tool: {name:
// tool({description, args, execute})}}) are an MCP server of magpie's
// own, so every agent that takes an MCP server can call them, not only
// OpenCode: Streamable HTTP at ToolsMCPPath, guarded as the search MCP
// server is (serveMCP), each call held to the calling gateway key's limit
// (reserveFor) and run in the plugin host. plugins/tools serves it on the
// tool point (gwtool.PluginTools); off, it lists no tool and runs none.
//
// A tool's answer goes back to the agent that called it on this computer
// (or a shared one with a key), never to a vendor, so masking has nothing
// to do with it; what the agent then sends a model is masked as any
// request is.

// ToolsMCPPath is where the plugin tools MCP server is served.
const ToolsMCPPath = "/mcp/magpie/tools"

// toolsMCPBody is the longest body the server takes: a tool's arguments
// may carry a file's text.
const toolsMCPBody = 4 << 20

// pluginTools and runPluginTool are the plugin host's, a test's fakes in
// their place.
var (
	pluginTools   = plugin.Tools
	runPluginTool = plugin.RunTool
)

// errToolsOff is what a call is answered while plugins/tools is off.
const errToolsOff = "magpie's Plugin tools plugin is off, so no plugin's tool runs; turn it on in magpie's Settings → Plugins"

func (s *Server) toolsMCP(w http.ResponseWriter, r *http.Request) {
	serveMCP(w, r, "magpie's plugin tools", "magpie-tools", toolsMCPBody, s.toolsMCPCall)
}

// mcpTool is one plugin tool as the server names it.
type mcpTool struct {
	spec string
	plugin.Tool
	name string // <plugin>_<tool>
}

var unsafeToolChars = lazyre.New(`[^A-Za-z0-9_-]+`)

// toolPrefix is the plugin's part of its tools' names: its short name
// (opencode-weather-tools → weather-tools), in the characters MCP clients
// take.
func toolPrefix(spec string) string {
	p := strings.Trim(unsafeToolChars.ReplaceAllString(plugin.ShortName(plugin.Name(spec)), "-"), "-_")
	if p == "" {
		p = "plugin"
	}
	if len(p) > 24 {
		p = p[:24]
	}
	return p
}

// servedTools are the tools of the plugins that are on (namedTools). With
// no plugin on, the plugin host isn't started for it.
func servedTools(ctx context.Context) ([]mcpTool, error) {
	if !gwtool.Served(gwtool.PluginTools) || !anyPluginOn() {
		return nil, nil
	}
	pts, err := pluginTools(ctx)
	if err != nil {
		return nil, err
	}
	return namedTools(pts), nil
}

// cutName is s cut to n bytes at most, where a character begins: a
// plugin's or tool's name may not be ASCII (#2416).
func cutName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// namedTools are pts' tools, each named <plugin>_<tool> (64 characters at
// most, the limit of some clients), a name two would share told apart by
// a number.
func namedTools(pts []plugin.PluginTools) []mcpTool {
	var out []mcpTool
	seen := map[string]bool{}
	for _, pt := range pts {
		pre := toolPrefix(pt.Spec)
		for _, t := range pt.Tools {
			base := pre + "_" + t.Name
			name := cutName(base, 64)
			for i := 2; seen[name]; i++ {
				suf := "_" + strconv.Itoa(i)
				name = cutName(base, 64-len(suf)) + suf
			}
			seen[name] = true
			out = append(out, mcpTool{spec: pt.Spec, Tool: t, name: name})
		}
	}
	return out
}

// ToolNames are the names the plugin tools MCP server gives pts' tools,
// by spec and the tool's own name, as the Plugins page shows them.
func ToolNames(pts []plugin.PluginTools) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, t := range namedTools(pts) {
		if out[t.spec] == nil {
			out[t.spec] = map[string]string{}
		}
		out[t.spec][t.Name] = t.name
	}
	return out
}

// anyPluginOn is whether plugins.json has a plugin that is on: none, and
// there is no tool to list without starting Bun.
var anyPluginOn = func() bool {
	for _, e := range plugin.Load().Plugins {
		if !e.Off {
			return true
		}
	}
	return false
}

func (s *Server) toolsMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	return s.toolsMCPServe(r, m, "", servedTools)
}

// ToolsMCPOne is where one plugin's tools are served, each by its own
// name, the plugin's short name (ToolPrefix) after it: the address of the
// Library server magpie gives each plugin with tools
// (library/plugin_tools.go), so it can be switched on per agent and put
// behind the MCP hub like any other.
const ToolsMCPOne = ToolsMCPPath + "/"

// ToolPrefix is the name a plugin's tools are under: its short name, in
// the characters MCP clients take.
func ToolPrefix(spec string) string { return toolPrefix(spec) }

func (s *Server) toolsMCPOne(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("plugin")
	serveMCP(w, r, "magpie's plugin tools", "magpie-tools", toolsMCPBody, func(r *http.Request, m mcpMessage) (any, map[string]any) {
		return s.toolsMCPServe(r, m, name, func(ctx context.Context) ([]mcpTool, error) {
			spec, ts, err := pluginToolsBy(ctx, func(spec string) bool { return toolPrefix(spec) == name })
			out := make([]mcpTool, 0, len(ts))
			for _, t := range ts {
				out = append(out, mcpTool{spec: spec, Tool: t, name: t.Name})
			}
			return out, err
		})
	})
}

// pluginToolsBy are the tools of the first plugin that is on that is
// picks, and its spec; none while Plugin tools is off, or no plugin that
// is on is it. With no plugin on, the plugin host isn't started for it.
func pluginToolsBy(ctx context.Context, is func(spec string) bool) (string, []plugin.Tool, error) {
	if !gwtool.Served(gwtool.PluginTools) || !anyPluginOn() {
		return "", nil, nil
	}
	pts, err := pluginTools(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, pt := range pts {
		if is(pt.Spec) {
			return pt.Spec, pt.Tools, nil
		}
	}
	return "", nil, nil
}

// PluginToolsOf are the tools of the plugin called name (plugin.Name of
// its spec) while it is on and Plugin tools is served: what the MCP hub
// lists for its Library server.
func PluginToolsOf(ctx context.Context, name string) ([]plugin.Tool, error) {
	_, ts, err := pluginToolsBy(ctx, func(spec string) bool { return plugin.Name(spec) == name })
	return ts, err
}

// RunPluginToolOf runs tool of the plugin called name for agent, as the
// MCP hub calls it: the text it answered, or why it couldn't run. The hub
// has held the call to the caller's key limit already.
func RunPluginToolOf(ctx context.Context, name, tool string, args json.RawMessage, agent string) (string, error) {
	if !gwtool.Served(gwtool.PluginTools) {
		return "", errors.New(errToolsOff)
	}
	spec, ts, err := pluginToolsBy(ctx, func(spec string) bool { return plugin.Name(spec) == name })
	if err != nil {
		return "", fmt.Errorf("magpie couldn't load its plugins: %w", err)
	}
	if !slices.ContainsFunc(ts, func(t plugin.Tool) bool { return t.Name == tool }) {
		return "", fmt.Errorf("the plugin %s isn't on in magpie, or gives no tool %s", name, tool)
	}
	return runPluginTool(ctx, spec, tool, args, agent)
}

// toolsMCPServe answers m for the tools list gives: every plugin's (one
// "") or one plugin's.
func (s *Server) toolsMCPServe(r *http.Request, m mcpMessage, one string, servedTools func(context.Context) ([]mcpTool, error)) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		say := "The tools the plugins added to magpie give, each named <plugin>_<tool>. Add or switch plugins in magpie's Settings → Plugins."
		if one != "" {
			say = "The tools magpie's plugin " + one + " gives. Add or switch plugins in magpie's Settings → Plugins."
		}
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie-tools", "title": "Magpie Plugin Tools", "version": Version},
			"instructions":    say,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		ts, err := servedTools(r.Context())
		if err != nil {
			return nil, map[string]any{"code": -32603, "message": "magpie couldn't load its plugins: " + err.Error()}
		}
		list := make([]any, 0, len(ts))
		for _, t := range ts {
			schema := t.Schema
			if len(schema) == 0 || !json.Valid(schema) {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			list = append(list, map[string]any{
				"name":        t.name,
				"description": t.Description,
				"inputSchema": schema,
			})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		if !gwtool.Served(gwtool.PluginTools) {
			return toolResult(errToolsOff, true), nil
		}
		ts, err := servedTools(r.Context())
		if err != nil {
			return toolResult("magpie couldn't load its plugins: "+err.Error(), true), nil
		}
		for _, t := range ts {
			if t.name == p.Name {
				said, err := s.toolsMCPRun(r, t, p.Arguments)
				if err != nil {
					return toolResult(err.Error(), true), nil
				}
				return toolResult(said, false), nil
			}
		}
		if one != "" {
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": the plugin " + one + " isn't on in magpie, or gives no such tool"}
		}
		return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": no plugin that is on gives it"}
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// toolsMCPRun runs one plugin tool, held to the calling gateway key's
// limit.
func (s *Server) toolsMCPRun(r *http.Request, t mcpTool, args json.RawMessage) (string, error) {
	release, err := reserveFor(access.Caller(r.Context()), int64(len(args)), "", limitClock())
	if err != nil {
		return "", err
	}
	defer release()
	return runPluginTool(r.Context(), t.spec, t.Name, args, callerOf(r).agent)
}
