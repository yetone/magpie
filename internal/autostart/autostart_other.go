//go:build !darwin && !windows && !android

package autostart

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

func record() string {
	dir := appdir.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "magpie.desktop")
}

// the two keys a desktop entry needs before the desktop starts it at login:
// no Type and it is not an application entry, no Exec and it names nothing
// to run. A .desktop a write left short of either reads as off, not as on.
var (
	entryType = regexp.MustCompile(`(?m)^Type=Application$`)
	// the Exec key with something to run after it: an empty one names
	// nothing, and the desktop starts no entry that names nothing
	entryExec = regexp.MustCompile(`(?m)^Exec=\s*\S`)
)

func enabled() bool {
	b, err := os.ReadFile(record())
	return err == nil && entryType.Match(b) && entryExec.Match(b)
}

// an XDG autostart entry, which the desktop starts at login
func enable(exe string) error {
	p := record()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// the Exec key's quoting: in double quotes, \ " ` $ escaped
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(exe)
	body := "[Desktop Entry]\nType=Application\nName=magpie\nComment=one place to pick every agent's model\n" +
		"Exec=\"" + q + "\" " + Arg + "\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	return edit.WriteAtomic(p, []byte(body))
}

func disable() error {
	if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// refresh: nothing an older magpie wrote here needs writing again
func refresh() error { return nil }
