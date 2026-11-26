package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// cindyLeftovers takes out of Cindy's folders the servers magpie wrote
// where nothing reads them. It runs before syncMCP, while the applied list
// still names what the last sync wrote; only those names are taken out,
// each file kept in the backup first, and a file left with no server is
// removed, and its folder if that is then empty.
//
//   - pi-agent-home/mcp.json: from v0.1.4777 the Library wrote the servers
//     given Cindy there too, for Cindy's Pi, which never reads it
//     (targets.go). Cindy makes no such file.
//   - codex-home/config.toml in a folder no build of Cindy keeps its data
//     in (agent.CindyData): the cn build's Cindy, which the global build
//     makes for what the two share, was taken for Cindy's data folder
//     whenever it was there, so the global build's servers went into a
//     config.toml there that nothing reads (agent's cindyDirs). One in a
//     folder a build does keep its data in is that build's Codex's, and
//     is left as it is.
func (l *Library) cindyLeftovers(t *Target, b *backups, res *Result) {
	if t.Agent == nil || t.Agent.ID != "cindy" || res.unreached(t) {
		return
	}
	a := l.applied("cindy")
	if len(a.MCP) == 0 {
		return
	}
	for _, d := range agent.CindyFolders() {
		l.dropMine(&mcpFile{Path: filepath.Join(d, "pi-agent-home", "mcp.json"), Format: fmtPiNative}, a.MCP, b, res)
		f := &mcpFile{Path: filepath.Join(d, "codex-home", "config.toml"), Format: fmtCodex}
		if (t.MCP != nil && samePath(t.MCP.Path, f.Path)) || agent.CindyData(d) {
			continue
		}
		l.dropMine(f, a.MCP, b, res)
	}
}

// dropMine takes the names magpie wrote out of f, and removes f and its
// folder when nothing is left in them.
func (l *Library) dropMine(f *mcpFile, names []string, b *backups, res *Result) {
	if !isFile(f.Path) {
		return
	}
	entries, err := f.entries()
	if err != nil {
		res.fail("cindy", "mcp", err)
		return
	}
	took := false
	for name := range entries {
		if !slices.Contains(names, name) {
			continue
		}
		if err := b.keep("cindy", f.Path); err != nil {
			res.fail("cindy", "mcp:"+name, err)
			return
		}
		if err := f.del(name); err != nil {
			res.fail("cindy", "mcp:"+name, err)
			return
		}
		took = true
		res.changed("cindy")
	}
	if took && leftEmpty(f) {
		if os.Remove(f.Path) == nil {
			os.Remove(filepath.Dir(f.Path)) // only when empty
		}
	}
}

// leftEmpty reports whether f holds nothing but an empty list of servers.
func leftEmpty(f *mcpFile) bool {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return false
	}
	if f.Format == fmtCodex {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				return false
			}
		}
		return true
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	for k, v := range m {
		if k != f.key() {
			return false
		}
		if o, ok := v.(map[string]any); !ok || len(o) > 0 {
			return false
		}
	}
	return true
}
