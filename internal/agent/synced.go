package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/filememo"
)

// What magpie's Library writes in each folder an agent shares with an app
// of its vendor's (Agent.synced, Agent.syncedKeys), as internal/library
// writes it: targetOf's instructions file and MCP file, and the specs of
// hooks, subagents, commands, rules, variables, Claude Code's plugins and
// rtk. A test there syncs every kind into each and checks the agent is no
// target after the app that made it one is gone.
var (
	// ~/.codex: AGENTS.md, hooks.json, agents/<name>.toml, rtk's RTK.md,
	// and in config.toml the MCP servers and the variables
	codexSynced     = []string{"AGENTS.md", "hooks.json", "agents/", "RTK.md"}
	codexSyncedKeys = map[string][]string{"config.toml": {"mcp_servers", "shell_environment_policy"}}
	// ~/.claude: CLAUDE.md, agents/, commands/ (the skill router's too),
	// rules/, rtk's RTK.md, and in settings.json the hooks, permissions
	// (the router's allow, MCP tools turned off), variables and plugins;
	// its MCP servers are in ~/.claude.json, outside the folder
	claudeSynced     = []string{"CLAUDE.md", "agents/", "commands/", "rules/", "RTK.md"}
	claudeSyncedKeys = map[string][]string{"settings.json": {"hooks", "permissions", "env", "enabledPlugins", "extraKnownMarketplaces"}}
	// ~/.cursor: mcp.json, hooks.json, agents/
	cursorSynced = []string{"mcp.json", "hooks.json", "agents/"}
	// ~/.copilot: copilot-instructions.md, mcp-config.json, agents/,
	// rtk's hooks/
	copilotSynced = []string{"copilot-instructions.md", "mcp-config.json", "agents/", "hooks/"}
)

// syncedOnly reports whether the file at p, in Dir, holds only keys
// magpie's Library writes there (syncedKeys). One that can't be read or
// parsed, or holds no key at all, isn't known to be magpie's: it is the
// agent's, as before.
func (a *Agent) syncedOnly(p string) bool {
	n := filepath.Base(p)
	keys, ok := a.syncedKeys[n]
	if !ok || a.Dir == "" || filepath.Dir(p) != filepath.Clean(a.Dir) {
		return false
	}
	have, err := filememo.Read("agent top keys", p, topKeys(strings.EqualFold(filepath.Ext(n), ".toml")))
	if err != nil || len(have) == 0 {
		return false
	}
	for _, k := range have {
		if !slices.Contains(keys, k) {
			return false
		}
	}
	return true
}

// topKeys parses a TOML or JSON object's top-level keys.
func topKeys(isTOML bool) func([]byte) ([]string, error) {
	return func(b []byte) ([]string, error) {
		var keys []string
		if isTOML {
			var m map[string]any
			if err := toml.Unmarshal(b, &m); err != nil {
				return nil, err
			}
			for k := range m {
				keys = append(keys, k)
			}
			return keys, nil
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		if m == nil {
			return nil, errors.New("not an object")
		}
		for k := range m {
			keys = append(keys, k)
		}
		return keys, nil
	}
}
