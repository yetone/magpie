package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// ClaudeSubagent is the model set for Claude Code's subagents
// (env.CLAUDE_CODE_SUBAGENT_MODEL, the Subagents field), as magpie wrote
// it ("<model>[:<effort>]"), for gateway.ClaudeSubagent: Claude Code's
// built-in Explore and Plan declare model "inherit", which it reads before
// the variable, so they ran on the lead's model whatever was set there
// (#7176), and the gateway moves their turns itself. "" when agent isn't
// Claude Code, the settings aren't routed through magpie or none is set.
// Read where StandIn reads: settings.json, then claude-magpie's launch
// file, then a running WSL distro's.
func ClaudeSubagent(agent string) string {
	if agent != "claude" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	at := here(home)
	if m := claudeSubagentIn(at, filepath.Join(at.claudeDir(), "settings.json")); m != "" {
		return m
	}
	if m := claudeSubagentIn(at, ClaudeLaunchPath()); m != "" || runtime.GOOS != "windows" {
		return m
	}
	return wslClaudeFirst(func(at place) string {
		return claudeSubagentIn(at, filepath.Join(at.claudeDir(), "settings.json"))
	})
}

// claudeSubagentIn is ClaudeSubagent for the settings file at path: its
// subagent model when it sends Claude Code through magpie and names one
// of magpie's models. A value of the user's own ("inherit", a tier alias)
// is Claude Code's to read, not the gateway's.
func claudeSubagentIn(at place, path string) string {
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return claudeReadOwn(v) }
	v := env("CLAUDE_CODE_SUBAGENT_MODEL")
	if v == "" {
		return ""
	}
	if u := env("ANTHROPIC_BASE_URL"); u != at.gw() && !claudeProxied(at, path, u) {
		return ""
	}
	if m, _ := tierAt(v); !isMagpie(strings.TrimSuffix(m, "[1m]")) {
		return ""
	}
	return v
}
