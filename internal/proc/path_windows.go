package proc

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/yetone/magpie/internal/appdir"
)

// UserPath has nothing to do on Windows: an app started from the Start menu
// gets the user's PATH from the registry, as a terminal does.
func UserPath() {}

// UserBinDirs are the folders a user's command-line tools are installed in
// that exist here — npm's global one, bun's, volta's, pnpm's, the
// standalone installers' — for finding one PATH doesn't reach.
func UserBinDirs() []string {
	home, _ := os.UserHomeDir()
	known := []string{
		filepath.Join(appdir.Getenv("APPDATA"), "npm"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(appdir.Getenv("LOCALAPPDATA"), "Volta", "bin"),
		filepath.Join(appdir.Getenv("LOCALAPPDATA"), "pnpm"),
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
