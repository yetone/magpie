//go:build !windows

package proc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// UserPath gives the desktop app the PATH a terminal of the user's has. One
// opened from the Finder, the Dock or a login item gets launchd's
// /usr/bin:/bin:/usr/sbin:/sbin and nothing a shell profile adds, so a claude
// or codex installed with a custom npm prefix (~/.npm-global/bin), nvm, bun,
// volta or asdf wasn't found: the agent wasn't seen and its subscription
// couldn't be used. The folders those tools use are added at once, when they
// exist; the login shell's own PATH is asked for after.
//
// The same call brings the agents' own variables (agentenv.Vars:
// PI_CODING_AGENT_DIR, CODEX_HOME, CLAUDE_CONFIG_DIR, ...) the profile sets
// and the app wasn't started with, so that magpie writes an agent's config
// into the folder the agent reads, not its default one in the home (atie on
// Discord: Pi's models.json went to ~/.pi/agent with PI_CODING_AGENT_DIR set
// in the shell). One already in magpie's environment is kept. One holding a
// relative path is lent to the programs magpie starts all the same, but
// magpie itself reads them through appdir.LookupEnv, which passes such a
// value over, as it does the ones magpie started with. Those are read
// as magpie starts (the library is synced into the agents at once), so the
// answer is waited for, but no more than shellWait: a slow profile mustn't
// hold the window up, and what it says later is still taken.
func UserPath() {
	addPath(userDirs(false))
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, vars := shellEnv()
		setUnset(vars)
		if p != "" {
			addPath(filepath.SplitList(p))
			login.Lock()
			login.dirs, login.at = filepath.SplitList(p), time.Now()
			login.Unlock()
		}
	}()
	select {
	case <-done:
	case <-time.After(shellWait):
	}
}

// shellWait is how long UserPath waits for the login shell before going on
// without it.
const shellWait = 2 * time.Second

// setUnset sets each variable magpie's environment doesn't have, to a value
// that isn't empty.
func setUnset(vars map[string]string) {
	for k, v := range vars {
		if _, set := os.LookupEnv(k); !set && v != "" {
			os.Setenv(k, v)
		}
	}
}

var login struct {
	sync.Mutex
	dirs []string
	at   time.Time
}

// LoginPath is the PATH a terminal opened now has — the login shell's — for
// telling whether a command an agent runs by name is found there, which
// the app's own PATH can't: UserPath adds folders the shell may not have.
// nil when the shell doesn't say; asked again after a minute.
func LoginPath() []string {
	login.Lock()
	defer login.Unlock()
	if login.dirs != nil && time.Since(login.at) < time.Minute {
		return login.dirs
	}
	p, _ := shellEnv()
	if p == "" {
		return nil
	}
	login.dirs, login.at = filepath.SplitList(p), time.Now()
	return login.dirs
}

// UserBinDirs are the folders a user's command-line tools are installed in
// that exist here — npm's prefixes, each Node version of nvm, fnm and mise,
// bun, volta, pnpm, asdf, the standalone installers' ~/.local/bin,
// Homebrew — for finding one PATH doesn't reach.
func UserBinDirs() []string { return userDirs(true) }

func userDirs(everyNode bool) []string {
	home, _ := os.UserHomeDir()
	var known []string
	for _, d := range []string{".local/bin", ".npm-global/bin", ".npm/bin", ".bun/bin", ".volta/bin", ".asdf/shims", ".local/share/mise/shims", ".cargo/bin", ".deno/bin", "Library/pnpm"} {
		known = append(known, filepath.Join(home, d))
	}
	nodes := func(pattern string) {
		vs, _ := filepath.Glob(filepath.Join(home, pattern))
		if len(vs) == 0 {
			return
		}
		if !everyNode {
			known = append(known, vs[len(vs)-1])
			return
		}
		slices.Reverse(vs)
		known = append(known, vs...)
	}
	nodes(".nvm/versions/node/*/bin")
	if everyNode {
		known = append(known, filepath.Join(home, ".local/share/pnpm"), npmPrefix(home))
		nodes(".local/share/mise/installs/node/*/bin")
		nodes(".local/share/fnm/node-versions/*/installation/bin")
		nodes("Library/Application Support/fnm/node-versions/*/installation/bin")
	}
	known = append(known, "/opt/homebrew/bin", "/usr/local/bin")
	var have []string
	for _, d := range known {
		if st, err := os.Stat(d); d != "" && err == nil && st.IsDir() {
			have = append(have, d)
		}
	}
	return have
}

// npmPrefix is the bin folder of the npm prefix ~/.npmrc names, "" when it
// names none.
func npmPrefix(home string) string {
	b, err := os.ReadFile(filepath.Join(home, ".npmrc"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "prefix" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, v[2:])
		}
		if filepath.IsAbs(v) {
			return filepath.Join(v, "bin")
		}
	}
	return ""
}

// shellEnv asks the user's login shell for its PATH and the values it has
// for agentenv.Vars ("" for one it hasn't); "" and nil when it can't say
// within a few seconds.
func shellEnv() (string, map[string]string) {
	sh := os.Getenv("SHELL")
	if sh == "" || !filepath.IsAbs(sh) {
		sh = "/bin/zsh"
		if _, err := os.Stat(sh); err != nil {
			sh = "/bin/sh"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// interactive too, since many put PATH in .zshrc/.bashrc
	cmd := CommandContext(ctx, sh, "-ilc", shellProbe(sh))
	cmd.Stdin = nil
	out, _ := cmd.Output()
	return parseShellEnv(string(out), shellMark)
}

// shellMark tells shellEnv's answer apart from whatever the profile prints.
const shellMark = "__magpie_path__"

// shellProbe is the command the login shell sh runs to print its PATH and
// agentenv.Vars, between two marks. A NUL (which no variable can hold)
// parts the values: PATH first, then each of agentenv.Vars in order.
//
// A POSIX shell (zsh, bash, fish too) expands "$PATH" itself. nushell
// doesn't: a double-quoted string is literal there, so it printed "$PATH",
// "$CLAUDE_CONFIG_DIR", ... and magpie set each variable to its own name
// (#738: then Claude sign-in failed with "mkdir $CLAUDE_CONFIG_DIR:
// read-only file system", and the account lapsed minutes later, Claude
// Code's sign-in looked for under that folder). nushell has no POSIX
// quoting to ask with, and its PATH is a list, so it is asked to run
// /bin/sh with the same printf: nushell hands it the environment as a
// child sees it, PATH joined back into one string, and sh expands the
// references. The outer command is in nushell's single quotes, where a
// backslash is a backslash, and the printf format in sh's double quotes,
// where \0 reaches printf as it is.
func shellProbe(sh string) string {
	format, args := shellMark+"%s", ` "$PATH"`
	for _, v := range agentenv.Vars {
		format += `\0%s`
		args += ` "$` + v + `"`
	}
	if filepath.Base(sh) == "nu" {
		return `^/bin/sh -c 'printf "` + format + shellMark + `"` + args + `'`
	}
	return "printf '" + format + shellMark + "'" + args
}

// parseShellEnv reads shellEnv's answer out of what the shell printed. The
// variables are nil when their count isn't agentenv.Vars' (a printf that
// doesn't know \0); PATH alone is still taken then. A value that is the
// variable's own reference ("$CODEX_HOME") is a shell that printed the
// text instead of expanding it (nushell before shellProbe knew it; a shell
// with other quoting still), not a folder: it counts as unset, as does a
// PATH of "$PATH".
func parseShellEnv(s, mark string) (string, map[string]string) {
	i := strings.Index(s, mark)
	if i < 0 {
		return "", nil
	}
	s = s[i+len(mark):]
	j := strings.Index(s, mark)
	if j < 0 {
		return "", nil
	}
	f := strings.Split(s[:j], "\x00")
	if f[0] == "$PATH" {
		f[0] = ""
	}
	if len(f) != len(agentenv.Vars)+1 {
		if len(f) == 1 {
			return f[0], nil
		}
		return "", nil
	}
	vars := make(map[string]string, len(agentenv.Vars))
	for k, v := range agentenv.Vars {
		if f[k+1] == "$"+v {
			f[k+1] = ""
		}
		vars[v] = f[k+1]
	}
	return f[0], vars
}

// addPath adds the folders PATH lacks, after the ones it has.
func addPath(dirs []string) {
	cur := filepath.SplitList(os.Getenv("PATH"))
	seen := map[string]bool{}
	for _, d := range cur {
		seen[d] = true
	}
	for _, d := range dirs {
		if d != "" && filepath.IsAbs(d) && !seen[d] {
			seen[d] = true
			cur = append(cur, d)
		}
	}
	os.Setenv("PATH", strings.Join(cur, string(os.PathListSeparator)))
}
