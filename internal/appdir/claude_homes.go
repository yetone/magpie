package appdir

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaudeHomes are every Claude Code folder of the user's, ClaudeDir first.
// More than one Claude account is Claude Code run with CLAUDE_CONFIG_DIR set
// to a folder of each's (~/.claude-work beside ~/.claude), a session is
// written into the folder of the account it ran under, and magpie read only
// the one it runs with (@fivesmallq on X: sessions and token use counted one
// Claude folder). Nothing records those folders, so they are looked for as
// CodexHomes looks for Codex's: in the home folder, and beside ClaudeDir, a
// folder whose name starts with "claude" or ".claude", or one folder down in
// such a folder (~/.claude-profiles/work), that holds projects/; and the
// folders magpie runs a saved account's Claude Code in (claude-accounts).
func ClaudeHomes() []string {
	home, _ := os.UserHomeDir()
	return ClaudeHomesIn(home)
}

// ClaudeHomesIn is ClaudeHomes with home as the user's home folder.
func ClaudeHomesIn(home string) []string {
	main := ClaudeDirIn(home)
	out := []string{main}
	seen := map[dirKey]bool{keyOf(main): true}
	add := func(dir string) {
		if k := keyOf(dir); !seen[k] {
			seen[k] = true
			out = append(out, dir)
		}
	}
	roots := []string{home}
	if p := filepath.Dir(main); keyOf(p) != keyOf(home) {
		roots = append(roots, p)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		es, _ := os.ReadDir(root)
		for _, e := range es {
			name := strings.ToLower(strings.TrimPrefix(e.Name(), "."))
			if !strings.HasPrefix(name, "claude") {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, _, isDir, err := DirStat(dir); err != nil || !isDir {
				continue
			}
			if claudeHomeAt(dir) {
				add(dir)
				continue
			}
			subs, _ := os.ReadDir(dir)
			for _, s := range subs {
				if sub := filepath.Join(dir, s.Name()); claudeHomeAt(sub) {
					add(sub)
				}
			}
		}
	}
	// the folders of the Claude accounts saved in magpie
	saved := filepath.Join(Config(), "claude-accounts")
	es, _ := os.ReadDir(saved)
	for _, e := range es {
		if dir := filepath.Join(saved, e.Name()); claudeHomeAt(dir) {
			add(dir)
		}
	}
	return out
}

// claudeHomeAt says dir is a folder Claude Code has kept a session in.
func claudeHomeAt(dir string) bool {
	_, _, isDir, err := DirStat(filepath.Join(dir, "projects"))
	return err == nil && isDir
}
