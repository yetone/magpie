package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/agentenv"
)

// homeVar is the variable Go's os.UserHomeDir reads the home folder from.
func homeVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// rooted says whether p names the same place whatever the working folder
// is: absolute, or on Windows rooted on the current drive, as \Users\x or
// /x is. A Git Bash path such as /c/x is one too, though Go takes it as
// \c\x on the current drive, not C:\x: it kept that meaning before, and it
// still doesn't depend on the working folder.
func rooted(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return runtime.GOOS == "windows" && (strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "/"))
}

// Home is the user's home folder, or an error when its variable is unset,
// empty or relative. os.UserHomeDir hands back "" there, which
// filepath.Join turns into a path under the folder magpie was started in,
// and on Android it falls back to the shared /sdcard.
func Home() (string, error) {
	v := homeVar()
	switch h := os.Getenv(v); {
	case h == "":
		return "", fmt.Errorf("%s is not set: magpie needs the home folder to find its own files and the agents'", v)
	case !rooted(h):
		return "", fmt.Errorf("%s is %q, not an absolute path", v, h)
	default:
		return h, nil
	}
}

// mustHome is Home for paths built once main has checked it; reached with
// an unusable home anyway (a test made one), it stops rather than write
// under the working folder.
func mustHome() string {
	h, err := Home()
	if err != nil {
		panic("appdir: " + err.Error())
	}
	return h
}

// pathVars are the variables folders and files are built from: the XDG
// folders, Windows' application data folders, magpie's own and every
// agent's.
var pathVars = sync.OnceValue(func() map[string]bool {
	vs := map[string]bool{}
	for _, v := range []string{
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
		"APPDATA", "LOCALAPPDATA",
		"MAGPIE_BIN_DIR", "OMARCHY_PATH", "OPENCODE_CONFIG",
	} {
		vs[v] = true
	}
	for _, v := range agentenv.Vars {
		if !agentenv.NotPaths[v] {
			vs[v] = true
		}
	}
	return vs
})

// usable accepts rooted paths, and a home-relative path only for variables
// whose readers expand it. Most agent paths, like the XDG and Windows
// folders, are used as given and would put a literal ~ under the working folder.
func usable(name, v string) bool {
	if rooted(v) {
		return true
	}
	switch name {
	case "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "PRIME_AGENT_CODING_AGENT_DIR", "HANA_HOME", "T3CODE_HOME":
		return v == "~" || strings.HasPrefix(v, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(v, `~\`))
	}
	return false
}

// LookupEnv is os.LookupEnv for what magpie itself reads: a folder variable
// (pathVars) holding a relative path reads as unset, as the XDG spec asks of
// one, so no file of magpie's or an agent's is written under the working
// folder. The environment is left as it is: the programs magpie starts get
// the variable as the user set it. The variables a login shell lends the
// desktop app (proc.UserPath) are read through here too.
func LookupEnv(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	if ok && v != "" && pathVars()[name] && !usable(name, v) {
		return "", false
	}
	return v, ok
}

// Getenv is LookupEnv's value, "" for a variable unset or ignored.
func Getenv(name string) string {
	v, _ := LookupEnv(name)
	return v
}

// CheckEnv is what main checks before anything is read or written: an
// unusable home folder is the error, and ignored names the folder
// variables holding a relative path, which LookupEnv passes over.
func CheckEnv() (ignored []string, err error) {
	for v := range pathVars() {
		if x := os.Getenv(v); x != "" && !usable(v, x) {
			ignored = append(ignored, v)
		}
	}
	_, err = Home()
	return ignored, err
}
