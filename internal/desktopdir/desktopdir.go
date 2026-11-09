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
// (AnthropicPBC.Claude_<publisher> for the Store's package.)
//
// The packaged app reads its own copy before the real folder's, so a file
// another program writes into %APPDATA%\Claude is not seen once the app has
// one there. Such an install's folders are the package's: those are found,
// read and written in place of the usual ones, unless Desktop runs in the
// usual ones (it has run there, and not later in the package's).
//
// From 2.31226 on the package no longer keeps %LOCALAPPDATA% to itself:
// Desktop's 3p mode reads the real %LOCALAPPDATA%\Claude-3p (its Ou() is
// path.join(LOCALAPPDATA, "Claude-3p"), the same as before, now outside
// the package's LocalCache), and a gateway written only into the
// package's Claude-3p is never seen (bg5eau on X). Which build is
// installed can't be told from its folders, so Sets gives both: what
// Desktop must find goes into each.
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
	return Sets(goos, home, getenv)[0]
}

// Sets are every set of Desktop's folders on goos for home: Find's first,
// then, for an MSIX install, the other of the package's and the usual
// ones, which one build or another of the packaged app reads.
func Sets(goos, home string, getenv func(string) string) []Dirs {
	env := func(k, def string) string {
		if v := getenv(k); v != "" && filepath.IsAbs(v) {
			return v
		}
		return def
	}
	switch goos {
	case "darwin":
		d := filepath.Join(home, "Library", "Application Support")
		return []Dirs{{Data: filepath.Join(d, "Claude"), Mode: filepath.Join(d, "Claude"), ThreeP: filepath.Join(d, "Claude-3p")}}
	case "windows":
		local := env("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		roaming := env("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		classic := Dirs{Data: filepath.Join(roaming, "Claude"), Mode: windowsDir(local, false), ThreeP: windowsDir(local, true)}
		pkg := packageDir(local)
		if pkg == "" {
			return []Dirs{classic}
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
			return []Dirs{classic, packaged}
		}
		return []Dirs{packaged, classic}
	}
	d := env("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return []Dirs{{Data: filepath.Join(d, "Claude"), Mode: filepath.Join(d, "Claude"), ThreeP: filepath.Join(d, "Claude-3p")}}
}

// IsolatedName is the folder of the Claude Desktop magpie can start on
// its own (#5671): Desktop run with --user-data-dir set to it keeps all its
// data there, its 3p mode's too, beside the user's own Claude and Claude-3p.
const IsolatedName = "Claude-Magpie-3p"

// Isolated is that folder on goos for home, "" where Desktop can't run in
// it: only macOS has it. Desktop's 3p folder there is its userData with
// "-3p" added unless it ends so already (GY in 2.7032's and 4.4212's
// index.pre.js), so --user-data-dir=…/Claude-Magpie-3p is that folder
// itself. On Windows the 3p folder is %LOCALAPPDATA%\Claude-3p whatever
// the userData, and Desktop sets its userData to it in 3p mode, so a
// second one would share the first's; Linux has no Desktop of
// Anthropic's to try it on.
func Isolated(goos, home string) string {
	if goos != "darwin" || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", IsolatedName)
}

// isolatedHere is Isolated on this computer when it is there.
func isolatedHere(goos, home string) string {
	d := Isolated(goos, home)
	if fi, err := os.Stat(d); d == "" || err != nil || !fi.IsDir() {
		return ""
	}
	return d
}

// Installed says whether Desktop's program is installed on goos, whatever
// it has written: on Windows, Squirrel's setup puts it in
// %LOCALAPPDATA%\AnthropicClaude\claude.exe, and the MSIX's has its
// package folder (Sets' Package). Installed and not yet opened, Desktop has
// none of its folders (#1482); it still reads at its first start what
// magpie writes into them. False elsewhere, where the folders tell.
func Installed(goos, home string, getenv func(string) string) bool {
	if goos != "windows" {
		return false
	}
	local := getenv("LOCALAPPDATA")
	if local == "" || !filepath.IsAbs(local) {
		local = filepath.Join(home, "AppData", "Local")
	}
	if fi, err := os.Stat(filepath.Join(local, "AnthropicClaude", "claude.exe")); err == nil && fi.Mode().IsRegular() {
		return true
	}
	return packageDir(local) != ""
}

// Everywhere are the folders of every set on this computer (Sets), each
// once, and Isolated when it is there: where the readers look for
// Desktop's sessions and skills.
func Everywhere() []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, d := range Sets(OS, home, appdir.Getenv) {
		for _, p := range d.All() {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if iso := isolatedHere(OS, home); iso != "" && !slices.Contains(out, iso) {
		out = append(out, iso)
	}
	return out
}

// LastRun is when Desktop last ran in any of dirs (lastRun).
func LastRun(dirs ...string) time.Time { return lastRun(dirs...) }

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
// local (%LOCALAPPDATA%): Packages\Claude_<publisher id>, or the Store's
// Packages\AnthropicPBC.Claude_<publisher id> (Desktop 2.31226 knows both
// families, Claude_pzs8sxrjxfjjc and AnthropicPBC.Claude_fnn82j28hfe8t),
// the one Desktop has run in first, else the first there is. The publisher
// id is not assumed: it is the one of whoever signed the package.
func packageDir(local string) string {
	var found []string
	for _, family := range []string{"Claude_*", "AnthropicPBC.Claude_*"} {
		m, _ := filepath.Glob(filepath.Join(local, "Packages", family))
		found = append(found, m...)
	}
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

// ClaudeCode are the versions of Claude Code that Desktop keeps for its
// Code tab, in any set's userData (Data, or ThreeP in its 3p mode, or
// Isolated):
// <userData>/claude-code/<version>/ holding claude.exe on Windows, claude
// elsewhere, or claude.app on a Mac (Desktop 2.7032's CCD storageDir and
// getBinaryPathForTarget). That copy reads ~/.claude as Claude Code does.
// Empty when Desktop hasn't fetched it, or isn't here.
func ClaudeCode(goos, home string, getenv func(string) string) []string {
	names := []string{"claude", "claude.app"}
	if goos == "windows" {
		names = []string{"claude.exe"}
	}
	var out []string
	sets := Sets(goos, home, getenv)
	if iso := isolatedHere(goos, home); iso != "" {
		sets = append(sets, Dirs{Data: iso, ThreeP: iso})
	}
	for _, d := range sets {
		for _, data := range []string{d.Data, d.ThreeP} {
			ents, _ := os.ReadDir(filepath.Join(data, "claude-code"))
			for _, e := range ents {
				if !e.IsDir() || slices.Contains(out, e.Name()) {
					continue
				}
				for _, n := range names {
					if _, err := os.Stat(filepath.Join(data, "claude-code", e.Name(), n)); err == nil {
						out = append(out, e.Name())
						break
					}
				}
			}
		}
	}
	return out
}
