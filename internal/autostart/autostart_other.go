//go:build !darwin && !windows && !android

package autostart

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

func record() string {
	dir := appdir.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "magpie.desktop")
}

func enabled() bool {
	_, err := os.Stat(record())
	return err == nil
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
	return os.WriteFile(p, []byte(body), 0o644)
}

func disable() error {
	if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// refresh: nothing an older magpie wrote here needs writing again
func refresh() error { return nil }
