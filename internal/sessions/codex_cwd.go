package sessions

import (
	"encoding/json"
	"strings"
	"time"
)

// CodexThreadCwd is the folder Codex thread id runs in, as its newest
// rollout's session_meta says; "" when no rollout of it is read. magpie's
// MCP hub asks it for Codex, which says no roots (roots/list answers
// none) but names its thread in each tool call's _meta, so magpie-image
// behind the hub still saves into the thread's project.
func CodexThreadCwd(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	var path string
	var mod time.Time
	for _, f := range codexFiles() {
		if f.cold || !strings.EqualFold(f.key, "codex:"+id) || (path != "" && !f.mod.After(modOf(mod))) {
			continue
		}
		path, mod = f.path, f.mod.Time()
	}
	if path == "" {
		return ""
	}
	var l cxLine
	if json.Unmarshal(firstLine(path), &l) != nil || l.Type != "session_meta" {
		return ""
	}
	return l.Payload.Cwd
}
