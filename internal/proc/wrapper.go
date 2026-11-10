package proc

// A tool's command can be a script of the user's that runs it through a
// version manager: `exec mise x "claude" -- "claude" "$@"` in
// ~/.local/bin/claude. Inside `mise x` the command is looked up on PATH
// again, and with ~/.local/bin before mise's own folders it is that same
// script: it runs itself until killed (deluxe on Discord, Omarchy Linux: a
// magpie started from the desktop, without the shell's mise environment,
// never showed the Claude sign-in link). The program such a script stands
// for is asked of mise, `mise which <name>`, and run directly.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Unwrap is the program path stands for when it is a small script that
// runs name through mise (`mise x … -- name`, `mise exec`, rtx's old
// name): what `mise which name` says. path itself otherwise, and when mise
// isn't here or doesn't say within a few seconds.
func Unwrap(name, path string) string {
	if runtime.GOOS == "windows" || path == "" {
		return path
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > wrapperMax {
		return path
	}
	key := unwrapKey{path, name, st.ModTime(), st.Size()}
	unwrapped.Lock()
	if e, ok := unwrapped.m[key]; ok && (e.real != "" || time.Since(e.at) < time.Minute) {
		unwrapped.Unlock()
		if e.real == "" {
			return path
		}
		return e.real
	}
	unwrapped.Unlock()
	real := ""
	if b, err := os.ReadFile(path); err == nil {
		if w, ok := parseMiseWrapper(b, name); ok {
			real = miseWhich(w, name, path)
		}
	}
	unwrapped.Lock()
	unwrapped.m[key] = unwrapEntry{real: real, at: time.Now()}
	unwrapped.Unlock()
	if real == "" {
		return path
	}
	return real
}

// wrapperMax is the largest file read as a possible wrapper script; a
// program is bigger.
const wrapperMax = 8 << 10

type unwrapKey struct {
	path, name string
	mod        time.Time
	size       int64
}

type unwrapEntry struct {
	real string // "" when it isn't a wrapper or mise didn't say
	at   time.Time
}

// unwrapped remembers what each file was found to stand for, until it
// changes; a failed ask is made again after a minute.
var unwrapped = struct {
	sync.Mutex
	m map[unwrapKey]unwrapEntry
}{m: map[unwrapKey]unwrapEntry{}}

// miseWrapper is what a wrapper script's mise line says.
type miseWrapper struct {
	mise string // the manager as the script names it: mise, /usr/bin/mise
	tool string // the tool it asks for (`mise x claude@latest -- …`), "" when none
}

// parseMiseWrapper reads b as a shell script whose line runs name through
// mise: `[exec] mise x|exec [options] [tool…] -- name args…`. A script
// running anything else after `--` (a path, "$bin") isn't one.
func parseMiseWrapper(b []byte, name string) (miseWrapper, bool) {
	if !bytes.HasPrefix(b, []byte("#!")) || bytes.IndexByte(b, 0) >= 0 {
		return miseWrapper{}, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := shellWords(line)
		if len(f) > 0 && f[0] == "exec" {
			f = f[1:]
		}
		if len(f) < 4 {
			continue
		}
		if m := filepath.Base(f[0]); m != "mise" && m != "rtx" {
			continue
		}
		if f[1] != "x" && f[1] != "exec" {
			continue
		}
		var tool string
		for i := 2; i < len(f); i++ {
			if f[i] != "--" {
				if !strings.HasPrefix(f[i], "-") {
					tool = f[i]
				}
				continue
			}
			if i+1 < len(f) && f[i+1] == name {
				return miseWrapper{mise: f[0], tool: tool}, true
			}
			break
		}
	}
	return miseWrapper{}, false
}

// shellWords splits a script's line into its words, as sh would without
// expanding anything: quotes taken off, a comment dropped. A word holding
// an expansion keeps its `$`, so it never equals a plain name.
func shellWords(line string) []string {
	var words []string
	var w strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				w.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '#' && !in:
			i = len(line)
		case c == ' ' || c == '\t' || c == '\r' || c == ';':
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

// miseWait is how long mise is given to say where the program is.
const miseWait = 5 * time.Second

// miseAsk is the context an ask of mise runs under, ended when mise is
// given up on: miseWait from now. A var so a test ends it when its fake
// mise has started rather than at a time a loaded machine can pass before
// the fake runs its first line (#1838).
var miseAsk = func() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), miseWait)
}

// miseWhich is the program `mise which name` names, run with magpie's
// environment (the shell's PATH UserPath took) from the home folder, whose
// mise config a terminal reads: with the wrapper's tool when plain
// `mise which` doesn't know it. "" when mise isn't here, fails, or names
// the wrapper itself or another wrapper.
func miseWhich(w miseWrapper, name, wrapper string) string {
	mise := findMise(w.mise)
	if mise == "" {
		return ""
	}
	asks := [][]string{{"which", name}}
	if w.tool != "" && w.tool != name {
		asks = append(asks, []string{"which", "--tool", w.tool, name})
	}
	home, _ := os.UserHomeDir()
	for _, args := range asks {
		ctx, cancel := miseAsk()
		cmd := ProbeContext(ctx, mise, args...)
		cmd.Dir = home
		out, err := cmd.Output()
		cancel()
		if err != nil {
			continue
		}
		p := strings.TrimSpace(string(out))
		if i := strings.IndexByte(p, '\n'); i >= 0 {
			p = strings.TrimSpace(p[:i])
		}
		if usableProgram(p, wrapper, name) {
			return p
		}
	}
	return ""
}

// usableProgram is whether p, as mise named it, is a program to run in the
// wrapper's place: a file that isn't the wrapper, nor a wrapper itself.
func usableProgram(p, wrapper, name string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return false
	}
	if ws, err := os.Stat(wrapper); err == nil && os.SameFile(st, ws) {
		return false
	}
	if st.Size() <= wrapperMax {
		if b, err := os.ReadFile(p); err == nil {
			if _, ok := parseMiseWrapper(b, name); ok {
				return false
			}
		}
	}
	return true
}

// findMise is the mise a wrapper names: its own path, else mise (or rtx)
// on PATH or in the folders it is installed in.
func findMise(named string) string {
	if filepath.IsAbs(named) {
		if st, err := os.Stat(named); err == nil && !st.IsDir() {
			return named
		}
	}
	for _, n := range []string{filepath.Base(named), "mise"} {
		if p, err := Look(n); err == nil {
			return p
		}
		if p := toolIn(n, runtime.GOOS, UserBinDirs()); p != "" {
			return p
		}
	}
	return ""
}
