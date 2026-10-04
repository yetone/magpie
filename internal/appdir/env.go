package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/agentenv"
)

// homeVar is the variable Go's os.UserHomeDir reads the home folder from.
func homeVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// Home is the user's home folder, or an error when its variable is unset,
// empty or not an absolute path. os.UserHomeDir hands back "" there, which
// filepath.Join turns into a path under the folder magpie was started in,
// and on Android it falls back to the shared /sdcard.
func Home() (string, error) {
	v := homeVar()
	switch h := os.Getenv(v); {
	case h == "":
		return "", fmt.Errorf("%s is not set: magpie needs the home folder to find its own files and the agents'", v)
	case !filepath.IsAbs(h):
		return "", fmt.Errorf("%s is %q, not an absolute path", v, h)
	default:
		return h, nil
	}
}

// mustHome is Home for the paths CheckEnv has made safe; reached with an
// unusable home anyway (the environment changed after it, or a test made
// one), it stops rather than write under the working folder.
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
func pathVars() []string {
	vs := []string{
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
		"APPDATA", "LOCALAPPDATA",
		"MAGPIE_BIN_DIR", "OMARCHY_PATH", "OPENCODE_CONFIG",
	}
	for _, v := range agentenv.Vars {
		if !agentenv.NotPaths[v] {
			vs = append(vs, v)
		}
	}
	return vs
}

// CheckEnv readies the environment for building paths, before magpie reads
// or writes anything: an unusable home folder is an error, and a folder
// variable holding a relative path is dropped, as the XDG spec asks of a
// relative value, so that no path resolves against the folder magpie was
// started in. An empty one is dropped too, and a value starting with ~
// stays: the agents' variables take one, and the home it stands for is
// checked. It returns the relative ones it dropped.
func CheckEnv() ([]string, error) {
	if _, err := Home(); err != nil {
		return nil, err
	}
	var dropped []string
	for _, v := range pathVars() {
		x, ok := os.LookupEnv(v)
		if !ok || filepath.IsAbs(x) || strings.HasPrefix(x, "~") {
			continue
		}
		os.Unsetenv(v)
		if x != "" {
			dropped = append(dropped, v)
		}
	}
	return dropped, nil
}
