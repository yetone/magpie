package library

// Variables in a command server's command and args (math_milian on
// Discord): $HOME/bin/x in the library is one value on a Mac (/Users) and
// on Linux (/home). The library keeps $NAME, ${NAME} and a leading ~ as
// the user wrote them; each agent is given them as it can read them, and
// magpie puts them in itself where it starts the server.
//
// An agent that expands ${NAME} in a command and its args as it starts it
// (cmdRefs) is given the reference in its own syntax, $NAME written
// ${NAME}, so it reads the variable from its own environment. Every other
// agent starts the command as written, with no shell, so magpie puts the
// values in as it writes the entry: HOME is the agent's home, the rest
// what magpie's environment or a terminal has. A ~ is put in for every
// agent: none of them reads one. A variable nothing has is left as
// written.
//
// A home is named the same way on every system (0xBCD18E on X: one
// command on computers whose users have different names): HOME and
// USERPROFILE are both the home, as $NAME, ${NAME} or Windows' %NAME%,
// and so is a leading ~ or ~\. An agent that reads references is given
// the home's own variable where it runs, ${USERPROFILE} on Windows (where
// an agent seldom has HOME) and ${HOME} elsewhere (where none has
// USERPROFILE). Any other %NAME% is a variable on Windows only; elsewhere
// it is left as written, as cmd.exe's syntax means nothing there. In a
// command's environment a leading ~ and %USERPROFILE% or %HOME% are put
// in as the home for every agent; $NAME and ${NAME} there are envref.go's.

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/wslrun"
)

// cmdVar is a variable in a command or an arg: ${NAME}, $NAME, or
// %NAME% as cmd.exe writes one (ProgramFiles(x86) has parentheses).
var cmdVar = lazyre.New(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)|%([A-Za-z_][A-Za-z0-9_()]*)%`)

// cmdOS is the system magpie and the agents it writes for (WSL's aside)
// run on; a var for tests.
var cmdOS = runtime.GOOS

// varOf is the name a cmdVar match names, and whether it is %NAME%.
func varOf(m []string) (string, bool) {
	if m[3] != "" {
		return m[3], true
	}
	return m[1] + m[2], false
}

// homeVar says whether a variable is the home: HOME or USERPROFILE, in
// either case as %NAME%, which Windows reads without one.
func homeVar(n string, pct bool) bool {
	if pct {
		return strings.EqualFold(n, "HOME") || strings.EqualFold(n, "USERPROFILE")
	}
	return n == "HOME" || n == "USERPROFILE"
}

// hasCmdVars says whether the command, an arg or a value of its
// environment has a variable or a leading ~ to put in.
func hasCmdVars(s *Server) bool {
	for _, v := range append([]string{s.Command}, s.Args...) {
		if cmdVar.MatchString(v) || tildeRest(v) != "" || v == "~" {
			return true
		}
	}
	return hasHomeEnv(s.Env)
}

// envHome is a home in a value of a command's environment: %USERPROFILE%
// or %HOME%, as cmd.exe writes it.
var envHome = lazyre.New(`(?i)%(?:USERPROFILE|HOME)%`)

// hasHomeEnv says whether a value of env names the home as homeEnv puts
// it in.
func hasHomeEnv(env map[string]string) bool {
	for _, v := range env {
		if v == "~" || tildeRest(v) != "" || envHome.MatchString(v) {
			return true
		}
	}
	return false
}

// homeEnv is env with a leading ~ and each %USERPROFILE% or %HOME% in its
// values put in as home; env itself when it has none, or no home is known.
func homeEnv(env map[string]string, home string) map[string]string {
	if home == "" || !hasHomeEnv(env) {
		return env
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = envHome.ReplaceAllLiteralString(homeOf(home, v), home)
	}
	return out
}

// tildeRest is what follows a leading ~/ (or ~\), with its slash; "" for
// a value with none.
func tildeRest(v string) string {
	if len(v) > 1 && v[0] == '~' && (v[1] == '/' || v[1] == '\\') {
		return v[1:]
	}
	return ""
}

// cmdRefs is how the agent reads a reference in a command and its args,
// as its source or docs say (refsOf has where): refNone for one that
// starts them as written.
func (f *mcpFile) cmdRefs() refSyntax {
	if f.Literal || f.NoCmdRefs {
		return refNone
	}
	switch f.Format {
	case fmtClaude, fmtGrok, fmtGemini, fmtOmp:
		// Claude Code, CodeBuddy and WorkBuddy; Grok Build; Gemini CLI;
		// omp, Qoder and AtomCode. Droid (fmtClaude too) reads one in env
		// and headers only: NoCmdRefs.
		return refDollar
	case fmtOpenCode:
		return refOpenCode
	case fmtCursor:
		return refCursor
	}
	// Codex (codex-rs spawns command and args as written), Kiro (only
	// the variables its setting approves), Crush, Copilot, Goose, Pi,
	// Hermes, dsh, Command Code, LiveAgent and those that read none
	return refNone
}

// withCmdVars is s as the agent is given it: its command and args with
// the references in the agent's syntax, or the values put in. s itself is
// never changed.
func (f *mcpFile) withCmdVars(s *Server) *Server {
	if s.Remote() || !hasCmdVars(s) {
		return s
	}
	h := home()
	if f.WSL {
		h = f.Home
	}
	look := lookCmdVar
	if f.WSL {
		// the distro's variables aren't magpie's: only its HOME is known
		look = func(string) (string, bool) { return "", false }
	}
	win := cmdOS == "windows" && !f.WSL
	// the home as the agent reads it where it runs
	hv := "HOME"
	if win {
		hv = "USERPROFILE"
	}
	syntax := f.cmdRefs()
	put := func(v string) string {
		if syntax != refNone {
			return toAgent(syntax, homeOf(h, cmdVar.ReplaceAllStringFunc(v, func(r string) string {
				n, pct := varOf(cmdVar.FindStringSubmatch(r))
				switch {
				case homeVar(n, pct):
					return "${" + hv + "}"
				case pct && !win:
					return r
				}
				return "${" + n + "}"
			})))
		}
		return expandCmd(v, h, win, look)
	}
	c := *s
	c.Env = homeEnv(s.Env, h)
	c.Command = put(s.Command)
	if s.Args != nil {
		c.Args = make([]string, len(s.Args))
		for i, a := range s.Args {
			c.Args[i] = put(a)
		}
	}
	return &c
}

// homeOf is v with a leading ~ as home; v as it is without a home.
func homeOf(home, v string) string {
	if home == "" {
		return v
	}
	if v == "~" {
		return home
	}
	if r := tildeRest(v); r != "" {
		return strings.TrimRight(home, `/\`) + r
	}
	return v
}

// expandCmd is v with a leading ~ and each variable put in: HOME and
// USERPROFILE as home, any other as look has it, a %NAME% only on Windows
// (win); one it hasn't is left as written.
func expandCmd(v, home string, win bool, look func(string) (string, bool)) string {
	v = homeOf(home, v)
	return cmdVar.ReplaceAllStringFunc(v, func(r string) string {
		n, pct := varOf(cmdVar.FindStringSubmatch(r))
		if homeVar(n, pct) && home != "" {
			return home
		}
		if pct && !win {
			return r
		}
		if x, ok := look(n); ok {
			return x
		}
		return r
	})
}

// lookCmdVar is a variable as magpie's environment has it, else as a
// terminal's does (loginVars).
func lookCmdVar(n string) (string, bool) {
	if x, ok := os.LookupEnv(n); ok {
		return x, true
	}
	x, ok := loginVars([]string{n})[n]
	return x, ok
}

// launchCmd is s's command and args as magpie starts it on this machine,
// or in distro (whose HOME is distroHome's), and its environment's home
// (homeEnv): each ~ and variable put in.
func launchCmd(s *Server, distro string) (string, []string, map[string]string) {
	if !hasCmdVars(s) {
		return s.Command, s.Args, s.Env
	}
	h, look, win := home(), lookCmdVar, cmdOS == "windows"
	if distro != "" {
		h, look, win = distroHome(distro), func(string) (string, bool) { return "", false }, false
	}
	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = expandCmd(a, h, win, look)
	}
	return expandCmd(s.Command, h, win, look), args, homeEnv(s.Env, h)
}

var homes struct {
	sync.Mutex
	of map[string]string
}

// distroHome is $HOME in a WSL distro, as a login shell there has it; ""
// when it couldn't be asked. A var for tests.
var distroHome = func(distro string) string {
	homes.Lock()
	h, ok := homes.of[distro]
	homes.Unlock()
	if ok {
		return h
	}
	out, err := wslrun.Run(10*time.Second, "-d", distro, "-e", "sh", "-lc", `printf %s "$HOME"`)
	if err != nil {
		return ""
	}
	h = strings.TrimSpace(string(out))
	if !strings.HasPrefix(h, "/") {
		return ""
	}
	homes.Lock()
	if homes.of == nil {
		homes.of = map[string]string{}
	}
	homes.of[distro] = h
	homes.Unlock()
	return h
}
