package library

// A plugin that gives tools (OpenCode's tool hook, served at the gateway's
// /mcp/magpie/tools) is a server in the Library of its own (chenxiang0010
// on Discord: Search1API's tools in the MCP hub). Once the plugin host has
// noted the plugin's tools (plugin-tools.json), the library adds a server
// named by the plugin's short name (search1api), at the gateway's
// ToolsMCPOne<name>, which serves that plugin's tools alone. It is then a
// server as any other: switched on per agent, its tools turned off one by
// one, behind the MCP hub as <name>__<tool>, or written into an agent as
// its own entry with the hub off.
//
//   - The hub calls the plugin in this process (pluginToolsOf,
//     runPluginToolOf), not through its own address.
//   - An agent is given the address as it reaches the gateway (through),
//     with a key from another machine.
//   - It is kept to this computer (Local): a sync never carries it, as
//     another computer may not have the plugin.
//   - It is offered once (PluginsOffered): a user who removes the server
//     doesn't get it back until the plugin is removed and added again.
//   - It goes with the plugin: once plugins.json no longer has the plugin,
//     the server is taken out. A plugin turned off keeps its server, which
//     lists nothing until the plugin is on again. A plugins.json that
//     can't be read takes nothing out.
//   - Added with the hub on, it is on for the agents given the hub's
//     entry, so its tools are in their hub at once; with the hub off, on
//     for none until the user switches it on.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// pluginToolsOf and runPluginToolOf are the gateway's, a test's fakes in
// their place; pluginsInstalled and toolsNoted are the plugin package's.
var (
	pluginToolsOf    = gateway.PluginToolsOf
	runPluginToolOf  = gateway.RunPluginToolOf
	pluginsInstalled = plugin.ReadList
	toolsNoted       = plugin.ToolNames
)

func init() {
	plugin.ToolsNoted = func() {
		if changed, err := adoptPluginTools(); err != nil {
			log.Printf("library: giving plugin tools a server: %v", err)
		} else if changed {
			if _, err := Sync(); err != nil {
				log.Printf("library: syncing after plugin tools: %v", err)
			}
		}
	}
}

// pluginURL is the address of the plugin's tools at the gateway at base.
func pluginURL(base, spec string) string {
	return strings.TrimRight(base, "/") + gateway.ToolsMCPOne + url.PathEscape(gateway.ToolPrefix(spec))
}

// adoptPluginTools gives each plugin with tools its server, and takes out
// those of plugins removed, saving the library when it changed. It doesn't
// sync.
func adoptPluginTools() (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return false, err
	}
	// and each Remote magpie's router's (bridge_mcp.go)
	if plugins, bridges := l.adoptPlugins(), l.adoptBridges(); !plugins && !bridges {
		return false, nil
	}
	return true, l.save()
}

// adoptPlugins brings the library's plugin servers in line with the
// plugins installed and the tools they were noted giving; true when it
// changed the library.
func (l *Library) adoptPlugins() bool {
	pl, err := pluginsInstalled()
	if err != nil {
		return false // unknown: nothing added, nothing taken out
	}
	noted := toolsNoted()
	installed := map[string]bool{}
	for _, e := range pl.Plugins {
		installed[plugin.Name(e.Spec)] = true
	}
	changed := false
	// the plugin removed: its server goes, and it is offered again once
	// added again
	if n := len(l.PluginsOffered); n > 0 {
		l.PluginsOffered = slices.DeleteFunc(l.PluginsOffered, func(name string) bool { return !installed[name] })
		changed = changed || len(l.PluginsOffered) != n
	}
	if n := len(l.MCP); n > 0 {
		l.MCP = slices.DeleteFunc(l.MCP, func(s *Server) bool { return s.Plugin != "" && !installed[s.Plugin] })
		changed = changed || len(l.MCP) != n
	}
	for _, e := range pl.Plugins {
		name := plugin.Name(e.Spec)
		if e.Off || len(noted[e.Spec]) == 0 || slices.Contains(l.PluginsOffered, name) {
			continue
		}
		l.PluginsOffered = append(l.PluginsOffered, name)
		changed = true
		if slices.ContainsFunc(l.MCP, func(s *Server) bool { return s.Plugin == name }) {
			continue
		}
		l.MCP = append(l.MCP, &Server{Name: l.freeName(gateway.ToolPrefix(e.Spec)), Transport: "http",
			URL: pluginURL(gateway.URL(), e.Spec), Plugin: name, Local: true, Agents: l.hubAgents()})
	}
	// the gateway's address, which the check and the page use, as it is now
	for _, s := range l.MCP {
		if s.Plugin == "" {
			continue
		}
		if u := pluginURL(gateway.URL(), s.Plugin); s.URL != u {
			s.URL, changed = u, true
		}
	}
	return changed
}

// freeName is base, or base-plugin, then a number, whichever no server
// has.
func (l *Library) freeName(base string) string {
	name := base
	for i := 1; l.server(name) != nil; i++ {
		name = base + "-plugin"
		if i > 1 {
			name += strconv.Itoa(i)
		}
	}
	return name
}

// hubAgents are the agents the hub's entry goes to (those with a server
// behind it), with the hub on; none with it off.
func (l *Library) hubAgents() []string {
	out := []string{}
	if !l.MCPHub {
		return out
	}
	for _, s := range l.MCP {
		for _, a := range s.Agents {
			if inHub(s, a) && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	return out
}

// pluginHubTools are the tools the plugin of s gives, as the hub lists a
// server's: its tools/list entries.
func pluginHubTools(ctx context.Context, s *Server) ([]map[string]json.RawMessage, error) {
	ts, err := pluginToolsOf(ctx, s.Plugin)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]json.RawMessage, 0, len(ts))
	for _, t := range ts {
		name, _ := json.Marshal(t.Name)
		desc, _ := json.Marshal(t.Description)
		spec := map[string]json.RawMessage{"name": name, "description": desc}
		if len(t.Schema) > 0 && json.Valid(t.Schema) {
			spec["inputSchema"] = t.Schema
		}
		out = append(out, spec)
	}
	return out, nil
}

// pluginHubCall runs tool of the plugin of s for agent, its answer as an
// MCP tool result.
func pluginHubCall(ctx context.Context, agentID string, s *Server, tool string, args json.RawMessage) (json.RawMessage, error) {
	said, err := runPluginToolOf(ctx, s.Plugin, tool, args, agentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": said}}, "isError": false})
}

// PluginServer is the server of a plugin's tools, as the plugin's row on
// the Plugins page shows it.
type PluginServer struct {
	Name   string `json:"name"`
	Plugin string `json:"plugin"` // plugin.Name, which PluginServerOn takes
	// On is whether any agent is given it
	On bool `json:"on"`
}

// PluginServers are the plugins' servers by plugin (plugin.Name); none
// when the library can't be read.
func PluginServers() map[string]PluginServer {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil
	}
	out := map[string]PluginServer{}
	for _, s := range l.MCP {
		if s.Plugin != "" {
			out[s.Plugin] = PluginServer{Name: s.Name, Plugin: s.Plugin, On: len(s.Agents) > 0}
		}
	}
	return out
}

// PluginServerOn hides a plugin's tools from every agent, or shows them
// again (chengming0v0 on Discord: a plugin's tools no agent should see,
// the plugin kept for its providers). Hidden, no agent is given the
// server, nor an app at the MCP router's address, and the agents it had
// are kept (Paused); shown again, they get it back. With none kept (it was given to none when added, or
// taken from each agent in the Library), on gives it to the agents the MCP
// router's entry goes to, or with the router off or given to none, to every
// agent shown that can take it, as the row's All chip in the Library does.
func PluginServerOn(pluginName string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		var s *Server
		for _, x := range l.MCP {
			if x.Plugin == pluginName {
				s = x
			}
		}
		if s == nil {
			return fmt.Errorf("%s has no MCP server in the library", pluginName)
		}
		if !on {
			if !s.Hidden {
				s.Hidden, s.Paused, s.Agents = true, s.Agents, []string{}
			}
			return nil
		}
		if len(s.Agents) > 0 {
			s.Hidden, s.Paused = false, nil
			return nil
		}
		want := s.Paused
		if len(want) == 0 {
			want = l.hubAgents()
		}
		r := reachNow()
		if len(want) == 0 {
			hidden := settings.Load().AgentsHidden
			for _, a := range r.order {
				if !slices.Contains(hidden, a) {
					want = append(want, a)
				}
			}
		}
		var to []string
		for _, a := range want {
			// one kept from before goes back as it was: its distro may be
			// stopped, or its file unread just now
			if len(s.Paused) > 0 || r.takes(a, s) {
				to = append(to, a)
			}
		}
		if len(to) == 0 {
			return fmt.Errorf("no agent can take %s", s.Name)
		}
		s.Agents, s.Hidden, s.Paused = slices.Compact(slices.Sorted(slices.Values(to))), false, nil
		return nil
	})
}
