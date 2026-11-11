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
// exist; the login shell's own PATH is asked for after. Of nvm's Nodes only
// the one a terminal gets is added: its default alias's, not the folder
// whose name sorts last (nvmBins).
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
		agents := make(map[string]string, len(agentenv.Vars))
		for _, v := range agentenv.Vars {
			agents[v] = vars[v]
		}
		setUnset(agents)
		if p != "" {
			addPath(filepath.SplitList(p))
			login.Lock()
			login.dirs, login.vars, login.at = filepath.SplitList(p), vars, time.Now()
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
	vars map[string]string
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
	p, vars := shellEnv()
	if p == "" {
		return nil
	}
	login.dirs, login.vars, login.at = filepath.SplitList(p), vars, time.Now()
	return login.dirs
}

// ProxyVars are the proxy variables the login shell is asked for beside
// agentenv.Vars. An agent run in a terminal has the ones its profile sets,
// and so does one the ChatGPT app starts (it takes the login shell's
// environment as it opens), while magpie opened from the Dock doesn't: a
// NO_PROXY=127.0.0.1 in .zshrc keeps Codex's requests for magpie off a
// proxy that magpie, reading its own environment, thought they went
// through (liu_dyj on X). They are only read (LoginEnv), never set in
// magpie's environment: magpie's own requests take their proxy from
// netproxy.
var ProxyVars = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// RegistryVars are the npm registry variables the login shell is asked
// for beside them: a registry set in .zshrc is the user's for bun's
// installs of plugins and magpie's asks of npm, which magpie opened from
// the Dock didn't see (timwangdev on X). Only read (LoginEnvKnown).
var RegistryVars = []string{"BUN_CONFIG_REGISTRY", "NPM_CONFIG_REGISTRY", "npm_config_registry"}

// shellVars are the variables shellProbe asks for, in its order. Claude
// Code's provider switches (agentenv.ClaudeProviders) are only read, as the
// proxy variables are.
var shellVars = slices.Concat(agentenv.Vars, ProxyVars, agentenv.ClaudeProviders, RegistryVars)

// LoginEnv is the value a terminal opened now has for k, one of
// agentenv.Vars, ProxyVars or agentenv.ClaudeProviders: the login shell's, asked as LoginPath asks
// and with it; "" when the shell has none for it or doesn't say.
func LoginEnv(k string) string {
	LoginPath()
	login.Lock()
	defer login.Unlock()
	return login.vars[k]
}

// LoginEnvKnown is what the login shell last said of k (UserPath or
// LoginPath asked it), "" when it hasn't been asked or had none; it never
// asks the shell itself, so a path every page takes doesn't wait on one.
func LoginEnvKnown(k string) string {
	login.Lock()
	defer login.Unlock()
	return login.vars[k]
}

// UserBinDirs are the folders a user's command-line tools are installed in
// that exist here — npm's prefixes, each Node version of nvm, fnm and mise,
// bun, volta, pnpm, asdf, the standalone installers' ~/.local/bin,
// Homebrew — for finding one PATH doesn't reach. A manager's Node versions
// come in the order a terminal would reach them: its default first, then
// the highest version (v22 before v9).
func UserBinDirs() []string { return userDirs(true) }

func userDirs(everyNode bool) []string {
	home, _ := os.UserHomeDir()
	var known []string
	if d := bunBin(); d != "" {
		known = append(known, d)
	}
	for _, d := range []string{".local/bin", ".npm-global/bin", ".npm/bin", ".bun/bin", ".volta/bin", ".asdf/shims", ".local/share/mise/shims", ".cargo/bin", ".deno/bin", "Library/pnpm"} {
		known = append(known, filepath.Join(home, d))
	}
	// nvm's Node a terminal gets: its default, or with none usable the
	// highest installed; none when the default is the system's Node
	if nvm := nvmDir(home); nvm != "" {
		bins, def, system := nvmBins(nvm)
		switch {
		case everyNode:
			known = append(known, bins...)
		case def != "":
			known = append(known, def)
		case !system && len(bins) > 0:
			known = append(known, bins[0])
		}
	}
	if everyNode {
		known = append(known, filepath.Join(home, ".local/share/pnpm"), npmPrefix(home))
		known = append(known, nodeBins(filepath.Join(home, ".local/share/mise/installs/node"))...)
		fnm := []string{filepath.Join(home, ".local/share/fnm"), filepath.Join(home, "Library/Application Support/fnm")}
		if d := os.Getenv("FNM_DIR"); filepath.IsAbs(d) {
			fnm = append([]string{d}, fnm...)
		}
		for _, d := range slices.Compact(fnm) {
			known = append(known, fnmBins(d)...)
		}
	}
	known = append(known, SystemDirs("/opt/homebrew/bin", "/usr/local/bin")...)
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
// for agentenv.Vars and ProxyVars ("" for one it hasn't); "" and nil when it can't say
// within a few seconds.
//
// fish and nushell are asked too, when they are installed and set up here
// but aren't the login shell: they aren't POSIX shells, so many keep zsh or
// bash as the login shell and have the terminal open fish instead (its
// profile setting, or exec fish from .zshrc). An agent installed in a folder
// only config.fish adds wasn't found (John on Discord: 二进制文件没有从用户环境变量
// 找到，我终端环境使用的fish). Their PATH comes after the login shell's, and
// a variable the login shell leaves empty is taken from them.
func shellEnv() (string, map[string]string) {
	sh := loginShell()
	asks := append([]string{sh}, termShells(sh)...)
	paths := make([]string, len(asks))
	vars := make([]map[string]string, len(asks))
	var wg sync.WaitGroup
	for i, s := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], vars[i] = askShell(s)
		}()
	}
	wg.Wait()
	return mergeShellEnv(paths, vars)
}

// loginShell is the user's login shell: $SHELL, else zsh, else sh.
func loginShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" || !filepath.IsAbs(sh) {
		sh = "/bin/zsh"
		if _, err := os.Stat(sh); err != nil {
			sh = "/bin/sh"
		}
	}
	return sh
}

// termShells are the shells other than the login shell sh that a terminal
// here may open instead of it: fish and nushell, when installed and their
// config folder is there (each makes it on its first run).
func termShells(sh string) []string {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(cfg) && home != "" {
		cfg = filepath.Join(home, ".config")
	}
	confs := map[string][]string{
		"fish": {filepath.Join(cfg, "fish")},
		"nu":   {filepath.Join(cfg, "nushell"), filepath.Join(home, "Library", "Application Support", "nushell")},
	}
	var out []string
	for _, n := range []string{"fish", "nu"} {
		if filepath.Base(sh) == n || cfg == "" {
			continue
		}
		set := slices.ContainsFunc(confs[n], func(d string) bool {
			st, err := os.Stat(d)
			return err == nil && st.IsDir()
		})
		if p := FindShell(n); set && p != "" {
			out = append(out, p)
		}
	}
	return out
}

// shellDirs are the folders FindShell looks in, before /etc/shells and
// PATH: the system's, Homebrew's, MacPorts', Nix's, cargo's.
var shellDirs = func() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/bin", "/usr/bin", "/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin",
		"/home/linuxbrew/.linuxbrew/bin", "/run/current-system/sw/bin", "/nix/var/nix/profiles/default/bin"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".nix-profile", "bin"), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"))
	}
	return dirs
}

// FindShell is the program of the shell name (zsh, bash, fish, nu)
// installed here, "" when there is none. A desktop app's PATH is launchd's,
// so the folders shells are installed in are looked in first, then the
// shells /etc/shells lists, then PATH.
func FindShell(name string) string {
	isProg := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}
	for _, d := range shellDirs() {
		if p := filepath.Join(d, name); isProg(p) {
			return p
		}
	}
	if b, err := os.ReadFile("/etc/shells"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if filepath.IsAbs(l) && filepath.Base(l) == name && isProg(l) {
				return l
			}
		}
	}
	p, _ := Look(name)
	return p
}

// mergeShellEnv puts shellEnv's answers together, the login shell's first:
// the PATH folders the first lacks after its own, and each variable from
// the first that has it.
func mergeShellEnv(paths []string, vars []map[string]string) (string, map[string]string) {
	var dirs []string
	for _, p := range paths {
		for _, d := range filepath.SplitList(p) {
			if d != "" && !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	var merged map[string]string
	for _, vs := range vars {
		if vs == nil {
			continue
		}
		if merged == nil {
			merged = make(map[string]string, len(vs))
		}
		for k, v := range vs {
			if merged[k] == "" {
				merged[k] = v
			}
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator)), merged
}

// askShell runs sh as a terminal opens it and reads what shellEnv says.
//
// The shell is a probe (ProbeContext): it leads a session of its own, with
// no controlling terminal. An interactive zsh in magpie's session took the
// terminal it was started from — it puts itself in the foreground, as a
// shell does for its jobs — and while it ran, magpie's own process group
// was in the background there: Ctrl-C no longer reached `magpie web`, and
// anything in that group touching the terminal (the bash asked beside it,
// whose profile runs in magpie's group, an agent's CLI) had the kernel stop
// the whole group, magpie with it (DD on Discord: magpie web → zsh:
// suspended (tty input)). A timeout also killed only the shell, leaving
// what its profile started holding the terminal; a probe's whole group goes.
//
// It is asked as an interactive login shell first, since many put PATH in
// .zshrc/.bashrc or a config.fish block under `status is-interactive`. A
// profile that hands an interactive shell over to something else — `exec
// tmux` or zellij's auto-start, common in config.fish — or takes longer
// than shellAsk to come up never runs the probe, and magpie got no PATH at
// all (John on Discord, fish set with chsh: an agent fish's PATH has
// wasn't found). Without an answer it is asked again as a login shell
// only, which still reads config.fish, .zprofile and .bash_profile.
// MAGPIE_SHELL_PROBE=1 is in its environment both times, for a profile to
// tell magpie's question from a terminal.
func askShell(sh string) (string, map[string]string) {
	for _, flags := range []string{"-ilc", "-lc"} {
		if p, vars := askShellAs(sh, flags); p != "" || vars != nil {
			return p, vars
		}
	}
	return "", nil
}

// shellAsk is how long one question to a shell may take.
var shellAsk = 5 * time.Second

func askShellAs(sh, flags string) (string, map[string]string) {
	return parseShellEnv(askShellRaw(sh, flags, shellProbe(sh)), shellMark)
}

// askShellRaw is what sh run with flags and the command probe prints, as
// askShell runs it: a probe of its own, MAGPIE_SHELL_PROBE=1, shellAsk
// at most.
func askShellRaw(sh, flags, probe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), shellAsk)
	defer cancel()
	cmd := ProbeContext(ctx, sh, flags, probe)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "MAGPIE_SHELL_PROBE=1")
	out, _ := cmd.Output()
	return string(out)
}

// ShellPath is the PATH the shell sh (zsh, bash, fish) has in a terminal
// opened now, whether or not it is the login shell; nil when it doesn't
// say within a few seconds.
func ShellPath(sh string) []string {
	p, _ := askShell(sh)
	if p == "" {
		return nil
	}
	return filepath.SplitList(p)
}

// shellMark tells shellEnv's answer apart from whatever the profile prints.
const shellMark = "__magpie_path__"

// shellProbe is the command the login shell sh runs to print its PATH and
// agentenv.Vars and ProxyVars (shellVars), between two marks. A NUL (which
// no variable can hold) parts the values: PATH first, then each of
// shellVars in order.
//
// Every shell is asked to run /bin/sh with the printf, and sh expands the
// references, so a value is what the shell exports: what an agent started
// from it gets. A shell's own expansion saw unexported variables too: fish's
// universal http_proxy, set with `set -U` and never exported, reached no
// program, yet magpie took it for Codex's proxy and showed the "proxied"
// drift (#2627); an unexported zsh or bash variable is the same.
//
// nushell needs it most: a double-quoted string is literal there, so it
// printed "$PATH", "$CLAUDE_CONFIG_DIR", ... and magpie set each variable
// to its own name (#738: then Claude sign-in failed with "mkdir
// $CLAUDE_CONFIG_DIR: read-only file system"). Its ^ runs /bin/sh as an
// external command rather than a nushell one. nushell, as fish (PATH is a
// path variable there), hands a child PATH joined with ':'. The outer
// command is in single quotes, where a backslash is a backslash in every
// one of these shells, and the printf format in sh's double quotes, where
// \0 reaches printf as it is.
func shellProbe(sh string) string {
	format, args := shellMark+"%s", ` "$PATH"`
	for _, v := range shellVars {
		format += `\0%s`
		args += ` "$` + v + `"`
	}
	cmd := `/bin/sh -c 'printf "` + format + shellMark + `"` + args + `'`
	if filepath.Base(sh) == "nu" {
		return "^" + cmd
	}
	return cmd
}

// parseShellEnv reads shellEnv's answer out of what the shell printed. The
// variables are nil when their count isn't shellVars' (a printf that
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
	if len(f) != len(shellVars)+1 {
		if len(f) == 1 {
			return f[0], nil
		}
		return "", nil
	}
	vars := make(map[string]string, len(shellVars))
	for k, v := range shellVars {
		if f[k+1] == "$"+v {
			f[k+1] = ""
		}
		vars[v] = f[k+1]
	}
	return f[0], vars
}

// varName is a variable's name a shell can be asked for: letters, digits
// and _, not starting with a digit, so it goes into shellNamedProbe as it is.
func varName(n string) bool {
	if n == "" || n[0] >= '0' && n[0] <= '9' {
		return false
	}
	for _, c := range n {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

var named struct {
	sync.Mutex
	// who is the SHELL and HOME the answers are for
	who  string
	vals map[string]namedVal
}

type namedVal struct {
	v   string
	set bool
	at  time.Time
}

// LoginVars are the values a terminal opened now has for names, the ones
// set there (empty ones too): what the login shell exports, or fish's or
// nushell's when a terminal opens those (shellEnv's shells, the login
// shell's answer first). A ${NAME} in an MCP server's headers or
// environment is read so where magpie's own environment hasn't it: a
// magpie opened from the Finder or the Dock has none of the profile's
// variables, so a server whose token is in .zshrc showed no tools (Cyrus
// on Discord). Each answer, unset too, is kept a minute; the values are
// only returned, never set in magpie's environment or logged. A name that
// isn't a variable's is never asked.
func LoginVars(names []string) map[string]string {
	who := os.Getenv("SHELL") + "\x00" + os.Getenv("HOME")
	out := map[string]string{}
	var ask []string
	named.Lock()
	if named.who != who {
		named.who, named.vals = who, nil
	}
	for _, n := range names {
		if !varName(n) || slices.Contains(ask, n) {
			continue
		}
		if a, ok := named.vals[n]; ok && time.Since(a.at) < time.Minute {
			if a.set {
				out[n] = a.v
			}
			continue
		}
		ask = append(ask, n)
	}
	named.Unlock()
	if len(ask) == 0 {
		return out
	}
	got := shellNamed(ask)
	now := time.Now()
	named.Lock()
	if named.who == who {
		if named.vals == nil {
			named.vals = map[string]namedVal{}
		}
		for _, n := range ask {
			v, set := got[n]
			named.vals[n] = namedVal{v: v, set: set, at: now}
		}
	}
	named.Unlock()
	for n, v := range got {
		out[n] = v
	}
	return out
}

// shellNamed asks shellEnv's shells for names, as shellEnv asks them for
// PATH: the set ones, each from the first shell that has it set.
func shellNamed(names []string) map[string]string {
	sh := loginShell()
	asks := append([]string{sh}, termShells(sh)...)
	answers := make([]map[string]string, len(asks))
	var wg sync.WaitGroup
	for i, s := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = askNamed(s, names)
		}()
	}
	wg.Wait()
	out := map[string]string{}
	for _, a := range answers {
		for k, v := range a {
			if _, have := out[k]; !have {
				out[k] = v
			}
		}
	}
	return out
}

// askNamed asks the shell sh for names as askShell asks it for PATH; nil
// when it doesn't answer.
func askNamed(sh string, names []string) map[string]string {
	for _, flags := range []string{"-ilc", "-lc"} {
		if vars := parseNamed(askShellRaw(sh, flags, shellNamedProbe(sh, names)), names); vars != nil {
			return vars
		}
	}
	return nil
}

// shellNamedProbe is the command sh runs to print, for each of names,
// whether it is exported ("1" or "") and its value, NUL-parted between
// two marks, as shellProbe prints PATH: /bin/sh expands them.
func shellNamedProbe(sh string, names []string) string {
	var format, args string
	for i, n := range names {
		if i > 0 {
			format += `\0`
		}
		format += `%s\0%s`
		args += ` "${` + n + `+1}" "$` + n + `"`
	}
	cmd := `/bin/sh -c 'printf "` + shellMark + format + shellMark + `"` + args + `'`
	if filepath.Base(sh) == "nu" {
		return "^" + cmd
	}
	return cmd
}

// parseNamed reads shellNamedProbe's answer: the set names' values; nil
// when there is no answer, or one of another length.
func parseNamed(s string, names []string) map[string]string {
	i := strings.Index(s, shellMark)
	if i < 0 {
		return nil
	}
	s = s[i+len(shellMark):]
	j := strings.Index(s, shellMark)
	if j < 0 {
		return nil
	}
	f := strings.Split(s[:j], "\x00")
	if len(f) != 2*len(names) {
		return nil
	}
	out := map[string]string{}
	for k, n := range names {
		if f[2*k] == "1" {
			out[n] = f[2*k+1]
		}
	}
	return out
}
