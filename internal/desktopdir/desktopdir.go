// Package desktopdir finds Claude Desktop's folders: the one place the
// agent, the library and the session readers ask.
//
// On macOS they are ~/Library/Application Support/Claude and Claude-3p, on
// Linux $XDG_CONFIG_HOME/Claude and Claude-3p. On Windows Desktop's own
// data (its claude_desktop_config.json, Cowork's sessions and skills) is
// %APPDATA%\Claude, its 3p mode's is %LOCALAPPDATA%\Claude-3p, and magpie
// writes deploymentMode into %LOCALAPPDATA%\Claude as CC Switch does.
//
// Desktop installed from the setup.exe that installs an MSIX package (as on
// Windows 10 LTSC, Kilig on Discord) runs packaged, and Windows keeps what
// it writes under %APPDATA% and %LOCALAPPDATA% in the package's own
// folders instead:
//
//	%LOCALAPPDATA%\Packages\Claude_<publisher>\LocalCache\Roaming\Claude
//	%LOCALAPPDATA%\Packages\Claude_<publisher>\LocalCache\Local\Claude(-3p)
//
// The packaged app reads its own copy before the real folder's, so a file
// another program writes into %APPDATA%\Claude is not seen once the app has
// one there. Such an install's folders are the package's: those are found,
// read and written in place of the usual ones, unless Desktop runs in the
// usual ones (it has run there, and not later in the package's).
package desktopdir

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// Dirs are Claude Desktop's folders on one computer.
type Dirs struct {
	// Data is Desktop's userData when signed in with Anthropic: its
	// claude_desktop_config.json with its MCP servers, Cowork's sessions
	// and skills (%APPDATA%\Claude on Windows).
	Data string
	// Mode is the Claude folder magpie writes deploymentMode into: Data,
	// except on Windows (%LOCALAPPDATA%\Claude, as CC Switch writes it).
	Mode string
	// ThreeP is Claude-3p, Desktop's whole userData in its 3p mode.
	ThreeP string
	// Package is the MSIX package's LocalCache folder Data, Mode and ThreeP
	// are in, "" for Desktop not installed as a package.
	Package string
}

// All are Data, Mode and ThreeP, each once.
func (d Dirs) All() []string {
	var out []string
	for _, p := range []string{d.Data, d.Mode, d.ThreeP} {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// OS is the system whose folders Here, and the agent's Desktop, look for:
// runtime.GOOS, set by a test to try another system's.
var OS = runtime.GOOS

// Here are Desktop's folders on this computer.
func Here() Dirs {
	home, _ := os.UserHomeDir()
	return Find(OS, home, appdir.Getenv)
}

// Find are Desktop's folders on goos for home, with getenv reading the
// environment (a folder in it that isn't absolute is passed over).
func Find(goos, home string, getenv func(string) string) Dirs {
	env := func(k, def string) string {
		if v := getenv(k); v != "" && filepath.IsAbs(v) {
			return v
		}
		return def
	}
	switch goos {
	case "darwin":
		d := filepath.Join(home, "Library", "Application Support")
		return Dirs{Data: filepath.Join(d, "Claude"), Mode: filepath.Join(d, "Claude"), ThreeP: filepath.Join(d, "Claude-3p")}
	case "windows":
		local := env("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		roaming := env("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		classic := Dirs{Data: filepath.Join(roaming, "Claude"), Mode: windowsDir(local, false), ThreeP: windowsDir(local, true)}
		pkg := packageDir(local)
		if pkg == "" {
			return classic
		}
		packaged := Dirs{
			Data:    filepath.Join(pkg, "Roaming", "Claude"),
			Mode:    filepath.Join(pkg, "Local", "Claude"),
			ThreeP:  filepath.Join(pkg, "Local", "Claude-3p"),
			Package: pkg,
		}
		// the usual folders while Desktop runs there; the package's when
		// only it has run in its own, or ran there last (a Desktop moved
		// from the usual install onto the package leaves its old data)
		c, p := lastRun(classic.Data, classic.ThreeP), lastRun(packaged.Data, packaged.ThreeP)
		if !c.IsZero() && !p.After(c) {
			return classic
		}
		return packaged
	}
	d := env("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return Dirs{Data: filepath.Join(d, "Claude"), Mode: filepath.Join(d, "Claude"), ThreeP: filepath.Join(d, "Claude-3p")}
}

// lastRun is when Desktop last ran in any of dirs, its userData folders:
// the time Chromium's "Local State" there was written, zero for none. A
// folder holding only claude_desktop_config.json (another tool's MCP
// servers, or the library's) is not one Desktop has run in.
func lastRun(dirs ...string) time.Time {
	var t time.Time
	for _, d := range dirs {
		if fi, err := os.Stat(filepath.Join(d, "Local State")); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// packageDir is the LocalCache folder of Desktop's MSIX package under
// local (%LOCALAPPDATA%): Packages\Claude_<publisher id>, the one Desktop
// has run in first, else the first there is. The publisher id is not
// assumed: it is the one of whoever signed the package.
func packageDir(local string) string {
	found, _ := filepath.Glob(filepath.Join(local, "Packages", "Claude_*"))
	var dirs []string
	for _, p := range found {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			dirs = append(dirs, filepath.Join(p, "LocalCache"))
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if !lastRun(filepath.Join(d, "Roaming", "Claude"), filepath.Join(d, "Local", "Claude-3p")).IsZero() {
			return d
		}
	}
	return dirs[0]
}

// windowsDir is %LOCALAPPDATA%\Claude (or Claude-3p), else the first folder
// there named Claude… (with -3p in it or not), as CC Switch finds it.
func windowsDir(local string, threep bool) string {
	name := "Claude"
	if threep {
		name = "Claude-3p"
	}
	exact := filepath.Join(local, name)
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	ents, _ := os.ReadDir(local)
	var found []string
	for _, e := range ents {
		if n := e.Name(); e.IsDir() && strings.HasPrefix(n, "Claude") && strings.Contains(n, "-3p") == threep {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return exact
	}
	sort.Strings(found)
	return filepath.Join(local, found[0])
}
