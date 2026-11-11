package appdir

import (
	"os"
	"path/filepath"
	"strings"
)

// CodexHome is the folder Codex CLI keeps its files in: config.toml,
// auth.json, the model catalog magpie writes, sessions, AGENTS.md and
// skills. It is $CODEX_HOME when that is set, as codex-rs's find_codex_home
// reads it, else ~/.codex. Every place magpie reads or writes Codex's files
// goes through here (Wakkana on Discord: with CODEX_HOME set, a model picked
// in magpie went to ~/.codex/config.toml, which Codex never read).
// CODEX_HOME is read through LookupEnv, so the value a login shell lends the
// desktop app (proc.UserPath) counts, and a relative one, which Codex would
// take from whatever folder it runs in, reads as unset.
func CodexHome() string {
	home, _ := os.UserHomeDir()
	return CodexHomeIn(home)
}

// CodexHomeIn is CodexHome with home as the user's home folder.
func CodexHomeIn(home string) string {
	if d := Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(home, ".codex")
}

// CodexHomes are every Codex folder of the user's on this computer:
// CodexHome first, then the others. Codex keeps one ChatGPT sign-in a
// folder (codex-rs login/src/auth/storage.rs: auth.json, or the keychain
// item named by the folder, under CODEX_HOME) and writes its sessions
// there too (rollout/src/lib.rs: sessions/, archived_sessions/), and
// `--profile <name>` only lays $CODEX_HOME/<name>.config.toml over
// config.toml (config/src/loader/mod.rs), the sign-in shared. So more than
// one account is Codex run with CODEX_HOME set to a folder of each's,
// ~/.codex-work beside ~/.codex, and magpie read only the one it runs
// with (phantom3792 on X: only the default profile's sessions counted,
// and the others' accounts read no quota).
//
// Nothing records those folders, so they are looked for where they are
// made: in the home folder, and beside CodexHome, a folder whose name
// starts with "codex" or ".codex", or one folder down in such a folder
// (~/.codex-profiles/work), that holds auth.json or sessions/.
func CodexHomes() []string {
	home, _ := os.UserHomeDir()
	return CodexHomesIn(home)
}

// CodexHomesIn is CodexHomes with home as the user's home folder.
func CodexHomesIn(home string) []string {
	main := CodexHomeIn(home)
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
			if !strings.HasPrefix(name, "codex") {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, _, isDir, err := DirStat(dir); err != nil || !isDir {
				continue
			}
			if codexHomeAt(dir) {
				add(dir)
				continue
			}
			subs, _ := os.ReadDir(dir)
			for _, s := range subs {
				if sub := filepath.Join(dir, s.Name()); codexHomeAt(sub) {
					add(sub)
				}
			}
		}
	}
	return out
}

// codexHomeAt says dir is a folder Codex has run in: it signed in there
// (auth.json) or kept a session there.
func codexHomeAt(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "auth.json")); err == nil && fi.Mode().IsRegular() {
		return true
	}
	_, _, isDir, err := DirStat(filepath.Join(dir, "sessions"))
	return err == nil && isDir
}
