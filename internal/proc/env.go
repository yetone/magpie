package proc

import (
	"context"
	"errors"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// End ends process pid as its own clean exit would look to its parent:
// SIGTERM where there are signals, on Windows TerminateProcess with exit
// code 0. The Codex app starts its app-server again after an exit 0 and
// shows an error for any other (its AppServerConnection: code === 0 is
// classifiedAsExpected). On Windows what pid started is ended with it,
// as a program ending on SIGTERM ends its own children (endTree).
// ErrDenied when this user may not; nil when pid is gone already.
func End(pid int) error { return endTree(pid) }

// ErrNoEnv is a system that doesn't tell another process's environment
// (Windows here).
var ErrNoEnv = errors.New("this system doesn't tell another process's environment")

// EnvOf is the value of each of names that process pid's environment has
// (a name it lacks is left out): on macOS as `ps -E` prints it, on Linux
// from /proc/<pid>/environ. ErrNoEnv elsewhere; an error too for another
// user's process, whose environment ps leaves out.
func EnvOf(ctx context.Context, pid int, names ...string) (map[string]string, error) {
	return envOf(ctx, pid, names)
}

// psEnvNameRe is where the next variable starts in `ps -E` output.
var psEnvNameRe = lazyre.New(` [A-Za-z_][A-Za-z0-9_]*=`)

// parsePSEnv reads names out of the line `ps -E -ww -o command=` prints:
// the command line, then each variable as NAME=value, space-separated and
// unquoted. A value runs to the next " NAME=", so one holding such text
// reads short; the last time a name shows is its own (the command line
// comes first).
func parsePSEnv(line string, names []string) map[string]string {
	out := map[string]string{}
	line = strings.TrimRight(line, "\r\n")
	for _, n := range names {
		i := strings.LastIndex(line, " "+n+"=")
		if i < 0 {
			continue
		}
		v := line[i+len(n)+2:]
		if j := psEnvNameRe.FindStringIndex(v); j != nil {
			v = v[:j[0]]
		}
		out[n] = v
	}
	return out
}

// parseEnviron reads names out of /proc/<pid>/environ's NUL-separated
// NAME=value list.
func parseEnviron(b []byte, names []string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(string(b), "\x00") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, n := range names {
			if k == n {
				out[n] = v
			}
		}
	}
	return out
}
