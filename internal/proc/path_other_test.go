//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/testenv"
	"golang.org/x/sys/unix"
)

// A desktop app with launchd's PATH finds a claude installed under a custom
// npm prefix: from the folders such tools use, and from the login shell's
// PATH, whatever its profile prints around it.
func TestUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	npm := filepath.Join(home, ".npm-global", "bin")
	os.MkdirAll(npm, 0o755)
	testenv.Program(t, filepath.Join(npm, "claude"), "#!/bin/sh\n")
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\necho 'welcome back!'\nPATH=/from/profile:$PATH\neval \"$2\"\necho bye\n")
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")

	UserPath()
	p := os.Getenv("PATH")
	if !strings.HasPrefix(p, "/usr/bin:/bin:") || !strings.Contains(p, npm) {
		t.Fatalf("known folders not added after the old PATH: %q", p)
	}
	for end := time.Now().Add(5 * time.Second); !strings.Contains(os.Getenv("PATH"), "/from/profile"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the login shell's PATH never came: %q", os.Getenv("PATH"))
		}
	}
	if n := strings.Count(os.Getenv("PATH"), "/usr/bin:"); n != 1 {
		t.Fatalf("a folder PATH had was added again: %q", os.Getenv("PATH"))
	}
}

// A desktop app started from the Finder doesn't have the variables a shell
// profile exports either; the agents' folders among them come from the login
// shell by the time UserPath returns (atie on Discord: PI_CODING_AGENT_DIR in
// the shell, Pi's models.json still written to ~/.pi/agent). One magpie was
// started with is kept, and one the profile leaves empty isn't set.
func TestUserPathTakesAgentVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pi := filepath.Join(home, "pi agent") // a space survives
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\necho 'a profile that talks'\n"+
		"export PI_CODING_AGENT_DIR='"+pi+"'\nexport CODEX_HOME=/from/profile/codex\nexport GROK_HOME=\n"+
		"eval \"$2\"\necho bye\n")
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range []string{"PI_CODING_AGENT_DIR", "GROK_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	t.Setenv("CODEX_HOME", "/started/with")
	// the Mac checks a new script on its first run, which can take longer
	// than UserPath waits; a login shell is no new script
	exec.Command(sh, "-c", ":").Run()

	UserPath()
	if got := os.Getenv("PI_CODING_AGENT_DIR"); got != pi {
		t.Fatalf("PI_CODING_AGENT_DIR = %q, want the profile's %q", got, pi)
	}
	if got := os.Getenv("CODEX_HOME"); got != "/started/with" {
		t.Fatalf("CODEX_HOME = %q: the one magpie was started with was replaced", got)
	}
	for _, v := range []string{"GROK_HOME", "CLAUDE_CONFIG_DIR"} {
		if _, set := os.LookupEnv(v); set {
			t.Fatalf("%s was set though the shell has no value for it", v)
		}
	}
	if !strings.Contains(os.Getenv("PATH"), "/usr/bin") {
		t.Fatalf("PATH lost: %q", os.Getenv("PATH"))
	}
}

// liu_dyj on X: a NO_PROXY=127.0.0.1 in .zshrc keeps the Codex a terminal
// or the ChatGPT app runs off the proxy, while magpie, opened from the Dock
// without it, warned that Codex went through the proxy. The login shell's
// proxy variables are read (LoginEnv) but never set in magpie's own
// environment, whose requests take their proxy from netproxy.
func TestLoginEnvProxyVarsNotSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\necho 'a profile that talks'\n"+
		"export HTTP_PROXY=http://127.0.0.1:7890 no_proxy=localhost,127.0.0.1\neval \"$2\"\n")
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range ProxyVars {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	exec.Command(sh, "-c", ":").Run()
	login.Lock()
	dirs, vars, at := login.dirs, login.vars, login.at
	said := loginSaid.Load()
	login.dirs, login.vars, login.at = nil, nil, time.Time{}
	loginSaid.Store(nil)
	login.Unlock()
	t.Cleanup(func() {
		login.Lock()
		login.dirs, login.vars, login.at = dirs, vars, at
		loginSaid.Store(said)
		login.Unlock()
	})

	UserPath()
	for _, v := range ProxyVars {
		if _, set := os.LookupEnv(v); set {
			t.Fatalf("%s set in magpie's own environment", v)
		}
	}
	if got := LoginEnv("HTTP_PROXY"); got != "http://127.0.0.1:7890" {
		t.Fatalf("HTTP_PROXY = %q, want the profile's", got)
	}
	if got := LoginEnv("no_proxy"); got != "localhost,127.0.0.1" {
		t.Fatalf("no_proxy = %q, want the profile's", got)
	}
	if got := LoginEnv("ALL_PROXY"); got != "" {
		t.Fatalf("ALL_PROXY = %q, want none", got)
	}
	// netproxy reads them on every request, without waiting on the shell
	if got := LoginEnvSaid("HTTP_PROXY"); got != "http://127.0.0.1:7890" {
		t.Fatalf("LoginEnvSaid(HTTP_PROXY) = %q, want the profile's", got)
	}
	if got := LoginEnvSaid("no_proxy"); got != "localhost,127.0.0.1" {
		t.Fatalf("LoginEnvSaid(no_proxy) = %q, want the profile's", got)
	}
}

// ReneXiong (#2627): a variable the shell has but doesn't export reaches
// no program started from it, so it is not the agent's. The reporter's
// were fish universals from an old Clash setup; here a profile sets the
// same values without export, beside an exported NO_PROXY.
func TestLoginEnvUnexportedNotCounted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\n"+
		"http_proxy=http://127.0.0.1:7890 https_proxy=http://127.0.0.1:7890 all_proxy=socks5://127.0.0.1:7890\n"+
		"CLAUDE_CONFIG_DIR=/not/exported\n"+
		"export NO_PROXY=127.0.0.1\neval \"$2\"\n")
	t.Setenv("SHELL", sh)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range append([]string{"CLAUDE_CONFIG_DIR"}, ProxyVars...) {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	old := shellDirs
	shellDirs = func() []string { return nil }
	t.Cleanup(func() { shellDirs = old })

	p, vars := shellEnv()
	if p == "" {
		t.Fatal("no PATH from the shell")
	}
	for _, v := range []string{"http_proxy", "https_proxy", "all_proxy", "CLAUDE_CONFIG_DIR"} {
		if got := vars[v]; got != "" {
			t.Errorf("%s = %q, but the shell never exports it", v, got)
		}
	}
	if got := vars["NO_PROXY"]; got != "127.0.0.1" {
		t.Errorf("NO_PROXY = %q, want the exported one", got)
	}
}

// jw1dev on X: CLAUDE_CODE_USE_BEDROCK=1 sends Claude Code to Bedrock past
// magpie. One in the profile is read (LoginEnv), for magpie to say so, but
// never set in magpie's own environment, where every agent magpie starts
// would take it.
func TestLoginEnvClaudeProvidersNotSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\nexport CLAUDE_CODE_USE_BEDROCK=1 CLAUDE_CODE_USE_MANTLE=true\neval \"$2\"\n")
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range agentenv.ClaudeProviders {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	exec.Command(sh, "-c", ":").Run()
	login.Lock()
	dirs, vars, at := login.dirs, login.vars, login.at
	login.dirs, login.vars, login.at = nil, nil, time.Time{}
	login.Unlock()
	t.Cleanup(func() {
		login.Lock()
		login.dirs, login.vars, login.at = dirs, vars, at
		login.Unlock()
	})

	UserPath()
	for _, v := range agentenv.ClaudeProviders {
		if _, set := os.LookupEnv(v); set {
			t.Fatalf("%s set in magpie's own environment", v)
		}
	}
	if got := LoginEnv("CLAUDE_CODE_USE_BEDROCK"); got != "1" {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK = %q, want the profile's", got)
	}
	if got := LoginEnv("CLAUDE_CODE_USE_MANTLE"); got != "true" {
		t.Fatalf("CLAUDE_CODE_USE_MANTLE = %q, want the profile's", got)
	}
	if got := LoginEnv("CLAUDE_CODE_USE_VERTEX"); got != "" {
		t.Fatalf("CLAUDE_CODE_USE_VERTEX = %q, want none", got)
	}
}

// A login shell that doesn't expand "$CLAUDE_CONFIG_DIR" prints the
// reference itself. That is no folder: the variable counts as unset,
// rather than magpie setting CLAUDE_CONFIG_DIR to "$CLAUDE_CONFIG_DIR" and
// then looking for Claude Code's sign-in under it (#738).
func TestParseShellEnvUnexpanded(t *testing.T) {
	s := shellMark + "$PATH"
	for _, v := range shellVars {
		s += "\x00$" + v
	}
	p, vars := parseShellEnv("hello from the profile\n"+s+shellMark, shellMark)
	if p != "" {
		t.Fatalf("PATH = %q, want none", p)
	}
	if len(vars) != len(shellVars) {
		t.Fatalf("%d variables, want %d", len(vars), len(shellVars))
	}
	for v, got := range vars {
		if got != "" {
			t.Fatalf("%s = %q, want none", v, got)
		}
	}
}

// nushell is asked in its own way, and answers with its PATH and the
// variables a child of it gets, as a POSIX shell does; where nushell isn't
// installed, the command's shape is all that can be checked.
func TestShellEnvNushell(t *testing.T) {
	probe := shellProbe("/opt/homebrew/bin/nu")
	if !strings.HasPrefix(probe, "^/bin/sh -c 'printf \""+shellMark+"%s") || !strings.HasSuffix(probe, `"$`+shellVars[len(shellVars)-1]+`"'`) {
		t.Fatalf("nushell's probe: %q", probe)
	}
	if strings.Contains(probe, `'`+shellMark) || strings.Count(probe, "'") != 2 {
		t.Fatalf("nushell's probe must hold sh's command in one pair of single quotes: %q", probe)
	}
	nu, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("nushell isn't installed")
	}
	t.Setenv("CODEX_HOME", "/from/the/environment")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	out, err := exec.Command(nu, "-ilc", shellProbe(nu)).Output()
	if err != nil {
		t.Fatalf("nu: %v", err)
	}
	p, vars := parseShellEnv(string(out), shellMark)
	if p == "" || strings.Contains(p, "$PATH") {
		t.Fatalf("PATH from nushell: %q", p)
	}
	if got := vars["CODEX_HOME"]; got != "/from/the/environment" {
		t.Fatalf("CODEX_HOME = %q: nushell's environment didn't reach sh", got)
	}
	if got := vars["CLAUDE_CONFIG_DIR"]; got != "" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want none", got)
	}
}

// The login shell is asked outside magpie's process group and session (DD
// on Discord: magpie web → zsh: suspended (tty input)). An interactive zsh
// in magpie's session puts itself in the terminal's foreground, which left
// magpie's group in the background there: Ctrl-C no longer reached it, and
// anything in the group that touched the terminal had the kernel stop all
// of it. The fake shell answers with its own process group and session; a
// real zsh, where there is one, still gives its PATH when asked that way.
func TestAskShellOwnSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sh := filepath.Join(home, "sh")
	testenv.Program(t, sh, "#!/bin/sh\nprintf '"+shellMark+"%s %s"+shellMark+"' \"$(ps -o pgid= -p $$ | tr -d ' ')\" \"$(ps -o sess= -p $$ | tr -d ' ')\"\n")
	out, _ := askShell(sh)
	f := strings.Fields(out)
	if len(f) != 2 {
		t.Fatalf("the fake shell's answer: %q", out)
	}
	if mine := strconv.Itoa(syscall.Getpgrp()); f[0] == mine {
		t.Fatalf("the login shell ran in magpie's process group %s: a shell there takes the terminal from magpie", mine)
	}
	if sid, err := unix.Getsid(0); err == nil && f[1] == strconv.Itoa(sid) && f[1] != "0" {
		t.Fatalf("the login shell ran in magpie's session %s: it can take magpie's terminal", f[1])
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh isn't installed")
	}
	if p := ShellPath(zsh); len(p) == 0 {
		t.Fatalf("zsh asked in a session of its own gave no PATH")
	}
}

// fishProbeOutput is what fish 4.9.3 printed for shellProbe under a
// config.fish that echoes a line, puts /Users/john/.fishbin first on PATH
// and sets CODEX_HOME: PATH joined with ':' (a path variable), then each of
// agentenv.Vars. The captured tail after CODEX_HOME was all empty values;
// it is padded to shellVars' count (agentenv.Vars, ProxyVars, then
// agentenv.ClaudeProviders) as it is now.
func fishProbeOutput() string {
	s := "hello from config.fish\n" + shellMark +
		"/Users/john/.fishbin:/usr/local/bin:/System/Cryptexes/App/usr/bin:/usr/bin:/bin:/usr/sbin:/sbin:/var/run/com.apple.security.cryptexd/codex.system/bootstrap/usr/local/bin:/var/run/com.apple.security.cryptexd/codex.system/bootstrap/usr/bin:/var/run/com.apple.security.cryptexd/codex.system/bootstrap/usr/appleinternal/bin:/opt/pmk/env/global/bin" +
		"\x00\x00/Users/john/codex"
	return s + strings.Repeat("\x00", len(shellVars)-2) + shellMark
}

// fakeShells makes HOME a temp folder with a login shell (SHELL) whose
// PATH lacks fish's folder, and a fish that answers as the real one did
// (fishProbeOutput), where FindShell looks.
func fakeShells(t *testing.T) (home string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	login := filepath.Join(home, "zsh")
	testenv.Program(t, login, "#!/bin/sh\nPATH=/from/zsh:/usr/bin:/bin\nexport CLAUDE_CONFIG_DIR=/from/zsh/claude\neval \"$2\"\n")
	t.Setenv("SHELL", login)
	bin := filepath.Join(home, "shells")
	os.MkdirAll(bin, 0o755)
	out := filepath.Join(home, "fish.out")
	os.WriteFile(out, []byte(fishProbeOutput()), 0o644)
	testenv.Program(t, filepath.Join(bin, "fish"), "#!/bin/sh\ncat '"+out+"'\n")
	old := shellDirs
	shellDirs = func() []string { return []string{bin} }
	t.Cleanup(func() { shellDirs = old })
	return home
}

// A terminal that opens fish while the login shell is zsh (John on
// Discord: 二进制文件没有从用户环境变量找到，我终端环境使用的fish): fish is asked
// too, its PATH comes after zsh's and a variable zsh leaves unset is
// fish's, so an agent in a folder only config.fish adds is found.
func TestShellEnvAsksFishBesideLoginShell(t *testing.T) {
	home := fakeShells(t)
	os.MkdirAll(filepath.Join(home, ".config", "fish"), 0o755)
	p, vars := shellEnv()
	dirs := filepath.SplitList(p)
	if len(dirs) < 2 || dirs[0] != "/from/zsh" || !slices.Contains(dirs, "/Users/john/.fishbin") {
		t.Fatalf("PATH = %q, want the login shell's first and fish's folder after", p)
	}
	if n := len(slices.DeleteFunc(slices.Clone(dirs), func(d string) bool { return d != "/usr/bin" })); n != 1 {
		t.Fatalf("a folder both shells have is there twice: %q", p)
	}
	if got := vars["CODEX_HOME"]; got != "/Users/john/codex" {
		t.Fatalf("CODEX_HOME = %q, want fish's", got)
	}
	if got := vars["CLAUDE_CONFIG_DIR"]; got != "/from/zsh/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the login shell's", got)
	}
}

// fish installed but never set up (no ~/.config/fish) isn't the terminal's
// shell, and isn't asked.
func TestShellEnvLeavesUnusedFish(t *testing.T) {
	fakeShells(t)
	if p, _ := shellEnv(); strings.Contains(p, "fishbin") {
		t.Fatalf("fish was asked with no config of its own: %q", p)
	}
}

// The login shell being fish itself, it is asked once, and what it printed
// is read: PATH whole, CODEX_HOME, and nothing for the variables it hasn't.
func TestShellEnvFishLogin(t *testing.T) {
	home := fakeShells(t)
	os.MkdirAll(filepath.Join(home, ".config", "fish"), 0o755)
	fish := filepath.Join(home, "shells", "fish")
	t.Setenv("SHELL", fish)
	if got := termShells(fish); len(got) != 0 {
		t.Fatalf("fish asked again beside itself: %q", got)
	}
	p, vars := shellEnv()
	if !strings.HasPrefix(p, "/Users/john/.fishbin:/usr/local/bin:") || strings.Contains(p, "/from/zsh") {
		t.Fatalf("PATH = %q", p)
	}
	if vars["CODEX_HOME"] != "/Users/john/codex" || vars["CLAUDE_CONFIG_DIR"] != "" {
		t.Fatalf("variables: %v", vars)
	}
}

// The real fish, where it is installed: a folder fish_add_path adds in
// config.fish and a variable it sets reach magpie with zsh (here a plain
// sh) as the login shell, and ShellPath(fish) has the folder.
func TestShellEnvRealFish(t *testing.T) {
	fish := FindShell("fish")
	if fish == "" {
		t.Skip("fish isn't installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	bin := filepath.Join(home, "fish bin") // a space survives
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(filepath.Join(home, ".config", "fish"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "fish", "config.fish"),
		[]byte("echo hi from fish\nfish_add_path -g '"+bin+"'\nset -gx CODEX_HOME "+home+"/codex\n"), 0o644)
	// the reporter's universal variables (#2627), never exported: no
	// program fish starts has them
	os.WriteFile(filepath.Join(home, ".config", "fish", "fish_variables"),
		[]byte("# This file contains fish universal variable definitions.\n# VERSION: 3.0\n"+
			"SETUVAR all_proxy:socks5\\x3a//127\\x2e0\\x2e0\\x2e1\\x3a7890\n"+
			"SETUVAR http_proxy:http\\x3a//127\\x2e0\\x2e0\\x2e1\\x3a7890\n"+
			"SETUVAR https_proxy:http\\x3a//127\\x2e0\\x2e0\\x2e1\\x3a7890\n"), 0o644)
	login := filepath.Join(home, "zsh")
	testenv.Program(t, login, "#!/bin/sh\neval \"$2\"\n")
	t.Setenv("SHELL", login)
	old := shellDirs
	shellDirs = func() []string { return []string{filepath.Dir(fish)} }
	t.Cleanup(func() { shellDirs = old })

	p, vars := shellEnv()
	if !slices.Contains(filepath.SplitList(p), bin) {
		t.Fatalf("PATH = %q, want fish's %q in it", p, bin)
	}
	if got := vars["CODEX_HOME"]; got != home+"/codex" {
		t.Fatalf("CODEX_HOME = %q, want fish's", got)
	}
	if got := vars["CLAUDE_CONFIG_DIR"]; got != "" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want none", got)
	}
	for _, v := range []string{"http_proxy", "https_proxy", "all_proxy"} {
		if got := vars[v]; got != "" {
			t.Errorf("%s = %q, from a universal fish never exports", v, got)
		}
	}
	if !slices.Contains(ShellPath(fish), bin) {
		t.Fatalf("ShellPath(fish) lacks %q", bin)
	}
}

// A profile that execs something else in an interactive shell (tmux's or
// zellij's auto-start in config.fish) or is too slow to come up
// interactively never prints the probe; the shell is asked again as a
// login shell only, and MAGPIE_SHELL_PROBE=1 tells the profile who asks
// (John on Discord, fish set with chsh).
func TestAskShellProfileExecsOrHangs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := shellAsk
	shellAsk = time.Second
	t.Cleanup(func() { shellAsk = old })
	for name, interactive := range map[string]string{
		"exec": "exec /bin/cat",
		"slow": "sleep 4",
	} {
		sh := filepath.Join(home, name)
		testenv.Program(t, sh, "#!/bin/sh\ncase \"$1\" in -i*) "+interactive+";; esac\n"+
			"PATH=/from/login:$PATH\n[ \"$MAGPIE_SHELL_PROBE\" = 1 ] && PATH=/told:$PATH\neval \"$2\"\n")
		p, vars := askShell(sh)
		dirs := filepath.SplitList(p)
		if !slices.Contains(dirs, "/from/login") || vars == nil {
			t.Fatalf("%s: PATH = %q, vars %v: the login shell wasn't asked again", name, p, vars != nil)
		}
		if !slices.Contains(dirs, "/told") {
			t.Fatalf("%s: MAGPIE_SHELL_PROBE didn't reach the profile: %q", name, p)
		}
	}
}

// The real fish, where it is installed, with a config.fish that starts
// tmux in an interactive shell, and one that's slow to come up in one:
// its PATH still comes.
func TestAskShellRealFishAutostart(t *testing.T) {
	fish := FindShell("fish")
	if fish == "" {
		t.Skip("fish isn't installed")
	}
	old := shellAsk
	shellAsk = 2 * time.Second
	t.Cleanup(func() { shellAsk = old })
	for name, block := range map[string]string{
		"tmux": "if status is-interactive; and not set -q TMUX\n  exec /bin/cat\nend\n",
		"slow": "if status is-interactive\n  sleep 5\nend\n",
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		bin := filepath.Join(home, "fb")
		os.MkdirAll(bin, 0o755)
		os.MkdirAll(filepath.Join(home, ".config", "fish"), 0o755)
		os.WriteFile(filepath.Join(home, ".config", "fish", "config.fish"),
			[]byte("set -g fish_greeting 'Welcome to fish'\nfish_add_path -g "+bin+"\n"+block), 0o644)
		if p, _ := askShell(fish); !slices.Contains(filepath.SplitList(p), bin) {
			t.Fatalf("%s: PATH = %q, want %q in it", name, p, bin)
		}
	}
}
