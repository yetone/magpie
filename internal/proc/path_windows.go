package proc

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/yetone/magpie/internal/appdir"
)

// UserPath gives magpie, and what it starts, the Node a terminal has from
// fnm (#2685). An app started from the Start menu gets the user's PATH from
// the registry, as a terminal does, but fnm puts its Node on PATH from the
// shell's profile (`fnm env | Invoke-Expression`) and never in the
// registry: an MCP server run with npx, even by npx.cmd's full path, died
// with "'node' is not recognized" once the package it fetched ran node.
// The folder fnm's default alias names is added after the ones PATH has.
func UserPath() {
	home, _ := os.UserHomeDir()
	addPath(fnmWinPath(home, appdir.Getenv("APPDATA"), os.Getenv("FNM_DIR")))
}

// UserBinDirs are the folders a user's command-line tools are installed in
// that exist here — npm's global one, bun's, volta's, pnpm's, mise's
// shims, the standalone installers', each Node version of fnm's, its
// default first — for finding one PATH doesn't reach.
func UserBinDirs() []string {
	home, _ := os.UserHomeDir()
	// mise's data folder: MISE_DATA_DIR, else XDG_DATA_HOME's mise (which
	// mise follows on Windows too), else LOCALAPPDATA's
	mise := filepath.Join(appdir.Getenv("LOCALAPPDATA"), "mise")
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		mise = filepath.Join(d, "mise")
	}
	if d := os.Getenv("MISE_DATA_DIR"); filepath.IsAbs(d) {
		mise = d
	}
	known := []string{
		filepath.Join(appdir.Getenv("APPDATA"), "npm"),
		filepath.Join(home, ".local", "bin"),
		bunBin(),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(appdir.Getenv("LOCALAPPDATA"), "Volta", "bin"),
		filepath.Join(appdir.Getenv("LOCALAPPDATA"), "pnpm"),
		filepath.Join(mise, "shims"),
	}
	for _, r := range fnmWinRoots(home, appdir.Getenv("APPDATA"), os.Getenv("FNM_DIR")) {
		known = append(known, fnmWinBins(r)...)
	}
	var have []string
	for _, d := range known {
		if st, err := os.Stat(d); filepath.IsAbs(d) && err == nil && st.IsDir() {
			have = append(have, d)
		}
	}
	return have
}

// LoginPath is the PATH the registry has now, the machine's then the
// user's: what a terminal opened now gets, and what a magpie started
// before a CLI was installed doesn't.
func LoginPath() []string {
	var dirs []string
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
		{registry.CURRENT_USER, `Environment`},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if v, _, err := key.GetStringValue("Path"); err == nil {
			// %SystemRoot%\system32 as Windows expands it: os.ExpandEnv of
			// $SystemRoot$\system32 made C:\Windows$\system32
			if x, err := registry.ExpandString(v); err == nil {
				v = x
			}
			for _, d := range strings.Split(v, ";") {
				if d = strings.TrimSpace(d); d != "" {
					dirs = append(dirs, d)
				}
			}
		}
		key.Close()
	}
	return dirs
}

// LoginEnv is "" on Windows: an app started from the Start menu gets the
// user's variables from the registry, as a terminal does, so magpie's own
// environment has them.
func LoginEnv(string) string { return "" }

// LoginEnvKnown is "" on Windows too (LoginEnv).
func LoginEnvKnown(string) string { return "" }

// ShellPath is LoginPath on Windows: PowerShell and cmd both take the
// registry's PATH.
func ShellPath(string) []string { return LoginPath() }

// LoginVars are the values the registry has now for names, the user's
// over the machine's: what a terminal opened now gets, and what a magpie
// started before the variable was set (setx, the Environment Variables
// dialog) doesn't. A ${NAME} in an MCP server's headers or environment is
// read so where magpie's own environment hasn't it. The values are only
// returned, never set in magpie's environment or logged.
func LoginVars(names []string) map[string]string {
	out := map[string]string{}
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		for _, n := range names {
			if _, have := out[n]; have || n == "" {
				continue
			}
			v, typ, err := key.GetStringValue(n)
			if err != nil {
				continue
			}
			if typ == registry.EXPAND_SZ {
				if x, err := registry.ExpandString(v); err == nil {
					v = x
				}
			}
			out[n] = v
		}
		key.Close()
	}
	return out
}
