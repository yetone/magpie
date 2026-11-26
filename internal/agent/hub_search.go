package agent

// Whether an agent that searches its MCP tools itself has that search
// turned off by the user, for the router (gateway.HubSearchOff): Claude
// Code with ENABLE_TOOL_SEARCH=false sends every tool it is given whole on
// every request, so magpie's MCP router gives it find_tools and call_tool
// in their place, as for any agent that doesn't search (jjaychen1e on X,
// #7349: 89 tools, ~38K tokens, on each request).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
)

// claudeBetasEnv turns Claude Code's experimental betas off, tool search
// with them: Claude Code 2.1.316's oae keeps tool search off while it is
// on, whatever ENABLE_TOOL_SEARCH says, unless managed settings force it.
const claudeBetasEnv = "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"

// claudeSearchOffValue is whether Claude Code reads a value of
// ENABLE_TOOL_SEARCH as tool search off (2.1.316's oae): 0, false, no or
// off in any case, or auto:N with N at 100 or more (MJo holds N to 100), a
// share of the context the tools never reach. Unset, empty, true, 1, auto,
// auto:N below 100 and force search.
func claudeSearchOffValue(v string) bool {
	if n, ok := strings.CutPrefix(v, "auto:"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return err == nil && f >= 100
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// claudeSearchOffAt is whether the Claude Code whose settings.json is at
// path keeps tool search off by its settings files, read as Claude Code
// reads them (claudeEnvAt): ENABLE_TOOL_SEARCH off, or experimental betas
// off and no managed ENABLE_TOOL_SEARCH=force. A project's settings and
// the shell it is started in aren't known, and a value magpie wrote
// (claudeSearchEnv's "true" while Claude Code is on magpie) is in
// settings.json over the shell's, so neither is read: a file that can't
// be read says nothing, and Claude Code searches by default.
func claudeSearchOffAt(at place, path string) bool {
	v, file, set := claudeEnvAt(at, path, claudeSearchEnv)
	if set && !claudeWhole(file) {
		return false
	}
	if set && claudeSearchOffValue(v) {
		return true
	}
	betas, from, off := claudeEnvAt(at, path, claudeBetasEnv)
	forced := set && file == claudeManagedAt(at) && strings.EqualFold(strings.TrimSpace(v), "force")
	return off && claudeSwitchOn(betas) && !forced && claudeWhole(from)
}

// claudeWhole is whether a settings file parses whole: Claude Code passes
// over one that doesn't, and one half-written reads as nothing yet.
func claudeWhole(file string) bool {
	raw, ok := edit.GetJSONRaw(file, "@this")
	return ok && json.Valid([]byte(raw))
}

// desktopSearchOff is whether Claude Desktop on magpie keeps its Code
// tab's tool search off: its toolSearchEnabled policy (desktopSeedToolSearch)
// isn't on in the profile it applies, magpie's or the user's import of
// it, so it starts Claude Code with experimental betas off. Every set of
// Desktop's folders on magpie must have it off; a profile that can't be
// read, or a value that isn't true or false, says nothing.
func desktopSearchOff(home string) bool {
	off := false
	for _, p := range desktopSets(desktopdir.OS, home, os.Getenv) {
		if !desktopWired(p) {
			continue
		}
		prof := p.prof
		if f := desktopAppliedGateway(p); f != "" {
			prof = f
		}
		b, err := os.ReadFile(prof)
		var m map[string]json.RawMessage
		if err != nil || json.Unmarshal(b, &m) != nil || m == nil {
			return false
		}
		switch v := strings.TrimSpace(string(m["toolSearchEnabled"])); v {
		case "true":
			return false
		case "", "false", "null":
			off = true
		default:
			return false
		}
	}
	return off
}

// hubSearchOffNow is HubSearchOff read now, by the agent's id: claude,
// claude#<name> (a config folder, claudeDirsIn), claude@wsl:<distro> and
// claude-desktop. Any other agent's search is its own.
func hubSearchOffNow(id string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	name, distro, inWSL := strings.Cut(id, "@wsl:")
	kind, prof, _ := strings.Cut(name, "#")
	switch {
	case kind == "claude" && inWSL:
		if prof != "" {
			return false
		}
		for _, d := range wslReady("claude") {
			if d.Name == distro {
				return claudeSearchOffAt(d.place(id), filepath.Join(d.local(d.Home), ".claude", "settings.json"))
			}
		}
		return false
	case kind == "claude":
		at := here(home)
		if prof != "" {
			_, dirs := claudeDirsIn(home)
			if dirs[prof] == "" {
				return false
			}
			at = place{home: home, id: id, dir: dirs[prof]}
		}
		return claudeSearchOffAt(at, filepath.Join(at.claudeDir(), "settings.json"))
	case id == "claude-desktop":
		return desktopSearchOff(home)
	}
	return false
}

// hubSearchFor is how long HubSearchOff keeps what it read: the router
// asks on every tools/list, and Claude Code reads its settings as it
// starts.
const hubSearchFor = 5 * time.Second

var hubSearch struct {
	sync.Mutex
	seen map[string]hubSearchSeen
}

type hubSearchSeen struct {
	off bool
	at  time.Time
}

// HubSearchOff is whether agent, one that searches its MCP tools itself,
// has its search turned off by the user's own settings, so magpie's MCP
// router gives it its tools on demand (find_tools and call_tool) as it
// does an agent that doesn't search. Read again after hubSearchFor. For
// gateway.HubSearchOff.
func HubSearchOff(agent string) bool {
	hubSearch.Lock()
	s, ok := hubSearch.seen[agent]
	hubSearch.Unlock()
	if ok && time.Since(s.at) < hubSearchFor {
		return s.off
	}
	off := hubSearchOffNow(agent)
	hubSearch.Lock()
	if hubSearch.seen == nil {
		hubSearch.seen = map[string]hubSearchSeen{}
	}
	hubSearch.seen[agent] = hubSearchSeen{off, time.Now()}
	hubSearch.Unlock()
	return off
}

// hubSearchForget drops what HubSearchOff read, for tests.
func hubSearchForget() {
	hubSearch.Lock()
	hubSearch.seen = nil
	hubSearch.Unlock()
}
