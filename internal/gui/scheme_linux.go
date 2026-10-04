package gui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// registerScheme makes magpie:// links open this executable: a desktop
// entry that claims the scheme, then the desktop's default for it. An
// entry that already runs this executable and claims it is left alone.
func registerScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data := appdir.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(data, "applications")
	path := filepath.Join(dir, "magpie.desktop")
	entry := `[Desktop Entry]
Type=Application
Name=magpie
Comment=Every agent's model. One place.
Exec=` + exe + ` %u
Icon=magpie
Categories=Development;Utility;
MimeType=x-scheme-handler/magpie;
Terminal=false
`
	if old, err := os.ReadFile(path); err == nil && strings.Contains(string(old), "Exec="+exe+" %u") && strings.Contains(string(old), "x-scheme-handler/magpie") {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return err
	}
	_ = proc.Command("update-desktop-database", dir).Run()
	return proc.Command("xdg-mime", "default", "magpie.desktop", "x-scheme-handler/magpie").Run()
}
