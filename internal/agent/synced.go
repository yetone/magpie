package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

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
	// its MCP servers are in ~/.claude.json, outside the folder; and the
	// windows of Claude Desktop's models (autoCompactWindow,
	// modelSettings) that magpie writes there for Desktop's Code tab
	// while Desktop is on magpie, which made the folder for a user with no
	// Claude Code a Claude Code row (KorwinTeo on X)
	claudeSynced     = []string{"CLAUDE.md", "agents/", "commands/", "rules/", "RTK.md"}
	claudeSyncedKeys = map[string][]string{"settings.json": {"hooks", "permissions", "env", "enabledPlugins", "extraKnownMarketplaces", "autoCompactWindow", "modelSettings"}}
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
	isTOML := strings.EqualFold(filepath.Ext(n), ".toml")
	have, err := filememo.Read("agent top keys", p, topKeys(isTOML))
	if err != nil || len(have) == 0 {
		return false
	}
	for _, k := range have {
		if !slices.Contains(keys, k) {
			return false
		}
	}
	if isTOML {
		// the keys were read without the checks a whole parse makes (a
		// key given twice); only a file that holds magpie's keys alone
		// needs them, and it is a small one
		if _, err := filememo.Read("agent toml whole", p, tomlWhole); err != nil {
			return false
		}
	}
	return true
}

// tomlWhole parses a whole TOML document, for its errors.
func tomlWhole(b []byte) (struct{}, error) {
	var m map[string]any
	return struct{}{}, toml.Unmarshal(b, &m)
}

// topKeys parses a TOML or JSON object's top-level keys.
func topKeys(isTOML bool) func([]byte) ([]string, error) {
	return func(b []byte) ([]string, error) {
		var keys []string
		if isTOML {
			return tomlTopKeys(b)
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

// tomlTopKeys reads a TOML document's top-level keys: each key set before
// the first table, and the first part of each table's name. It reads the
// syntax only, building no value and making none of a whole parse's
// checks across keys (tomlWhole). Codex's config.toml keeps a table for
// each folder the user trusted, ten thousand and more for some, and a
// whole parse checks each table's name against the ones before it: ~200ms
// of a start, each time the file was read again.
func tomlTopKeys(b []byte) ([]string, error) {
	var p unstable.Parser
	p.Reset(b)
	var keys []string
	inTable := false
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Table, unstable.ArrayTable:
			inTable = true
		case unstable.KeyValue:
			if inTable {
				continue
			}
		default:
			continue
		}
		k := e.Key()
		if !k.Next() {
			continue
		}
		if key := k.Node().Data; !slices.ContainsFunc(keys, func(s string) bool { return s == string(key) }) {
			keys = append(keys, string(key))
		}
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	return keys, nil
}
