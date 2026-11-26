package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/yetone/magpie/internal/agent"
)

// cindyLeftovers takes out of Cindy's folders the servers magpie wrote
// where nothing reads them. From v0.1.4777 the Library wrote the servers
// given Cindy into pi-agent-home/mcp.json too, for Cindy's Pi, which never
// reads it (targets.go). It runs before syncMCP, while the applied list
// still names what the last sync wrote; only those names are taken out,
// each file kept in the backup first, and a file left with no server is
// removed (Cindy makes none there), and its folder if that is then empty.
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
