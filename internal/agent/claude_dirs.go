package agent

// Claude Code kept in more than one config folder (CLAUDE_CONFIG_DIR, as in
// `alias claude-work='CLAUDE_CONFIG_DIR=~/.claude-work claude'`): the one
// the variable (or ~/.claude) names is the claude row; every other folder
// beside it under the home folder that is named .claude… and holds a
// Claude Code's own files is a row of its own, claude#<name> (the way omp's
// profiles are omp#<name>), with its own settings.json to connect, a model
// to pick and Check. BieJian21872 on X: only the one at the default path
// was found.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopiso"
	"github.com/yetone/magpie/internal/dirent"
)

// claudeDirName is the part of a folder's name after ".claude" as a row's
// name: ".claude-work" is "work", ".claude2" is "2". "" for a name that is
// no extra Claude Code folder (the file ~/.claude.json and its backups, the
// folder .claude itself, the launch file's).
func claudeDirName(base string) string {
	rest, ok := strings.CutPrefix(strings.ToLower(base), ".claude")
	if !ok || rest == "" || strings.HasPrefix(rest, ".") {
		return ""
	}
	rest = strings.Trim(rest, "-_")
	if rest == "" || rest == "launch" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, rest)
}

// claudeDirIsClaude says whether dir holds what Claude Code itself keeps in
// a config folder: its settings.json or its sign-in file, or its projects.
// Another tool's .claude-… folder (a router's, a launcher's) has none.
func claudeDirIsClaude(dir string) bool {
	for _, f := range []string{"settings.json", ".credentials.json"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return isDir(filepath.Join(dir, "projects"))
}

// claudeDirAgents are the Claude Code config folders beside the claude
// row's, each an agent of its own. None while the folder has no Claude
// Code's files: a leftover is no agent.
func claudeDirAgents(home string) []*Agent {
	es, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	main := appdir.ClaudeDirIn(home)
	var names []string
	byName := map[string]string{}
	for _, e := range es {
		name := claudeDirName(e.Name())
		dir := filepath.Join(home, e.Name())
		if name == "" || !dirent.IsFolder(home, e) || dir == main || !claudeDirIsClaude(dir) {
			continue
		}
		if _, dup := byName[name]; dup {
			continue
		}
		byName[name] = dir
		names = append(names, name)
	}
	// with CLAUDE_CONFIG_DIR elsewhere, the default folder is one of the others
	if def := filepath.Join(home, ".claude"); def != main && isDir(def) && claudeDirIsClaude(def) {
		byName["default"] = def
		names = append(names, "default")
	}
	sort.Strings(names)
	var out []*Agent
	for _, name := range names {
		dir := byName[name]
		id := "claude#" + name
		a := claudeIn(place{home: home, id: id, dir: dir})
		a.ID, a.Name = id, "Claude Code · "+name
		// the isolated Claude Desktop's Code tab's (#6459), by its app's name
		if dir == desktopIsolatedClaudeDir(home) {
			a.Name = "Claude Code · " + desktopiso.Name
		}
		// the claude row keeps the command, the names and the requests
		a.Aliases, a.UA, a.Bin = nil, nil, ""
		a.detect = func() bool { return claudeDirIsClaude(dir) }
		out = append(out, a)
	}
	return out
}
