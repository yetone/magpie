package gui

import (
	"bytes"
	_ "embed"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// desktopIcon is the entry's Icon=magpie, put in the user's icon theme: the
// release binary run as downloaded has no package or install.sh to put it
// there, and the app grid drew a gear without it (#2626). It is the same
// picture install.sh downloads (go run build/icon/gen.go app 256).
//
//go:embed icon-256.png
var desktopIcon []byte

// dataHome is $XDG_DATA_HOME, or ~/.local/share without it.
func dataHome() string {
	if data := appdir.Getenv("XDG_DATA_HOME"); data != "" {
		return data
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// registerScheme makes magpie:// links open this executable: a desktop
// entry that claims the scheme, then the desktop's default for it. The
// entry also binds magpie's windows to it (StartupWMClass) and has its icon
// installed beside it. An entry that already runs this executable, claims
// it and names the window class is left alone.
func registerScheme() error {
	// The package supplies the host entry; this executable is inside the sandbox.
	if os.Getenv("FLATPAK_ID") != "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data := dataHome()
	iconErr := installIcon(data)
	dir := filepath.Join(data, "applications")
	path := filepath.Join(dir, "magpie.desktop")
	entry := `[Desktop Entry]
Type=Application
Name=magpie
Comment=Every agent's model. One place.
Exec=` + exe + ` %u
Icon=magpie
StartupWMClass=` + programName + `
Categories=Development;Utility;
MimeType=x-scheme-handler/magpie;
Terminal=false
`
	if old, err := os.ReadFile(path); err == nil && strings.Contains(string(old), "Exec="+exe+" %u") && strings.Contains(string(old), "x-scheme-handler/magpie") &&
		strings.Contains(string(old), "\nStartupWMClass="+programName+"\n") {
		return iconErr
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return err
	}
	_ = proc.Command("update-desktop-database", dir).Run()
	return errors.Join(iconErr, proc.Command("xdg-mime", "default", "magpie.desktop", "x-scheme-handler/magpie").Run())
}

// installIcon puts magpie's icon where the entry's Icon=magpie is looked
// up, the user's hicolor theme, unless the same picture is there already.
func installIcon(data string) error {
	dir := filepath.Join(data, "icons", "hicolor", "256x256", "apps")
	path := filepath.Join(dir, "magpie.png")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, desktopIcon) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, desktopIcon, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// keepWebData moves WebKitGTK's data (the pages' localStorage, which holds
// choices like favourite models and masked emails) to the folder it now
// uses. WebKitGTK names that folder after the program, which was the
// executable's name until magpie set programName: a release run as
// downloaded kept it in ~/.local/share/magpie-linux-amd64. It moves only
// WebKit's own folder (one holding localstorage), and only when magpie's
// isn't there yet.
func keepWebData() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := filepath.Base(exe)
	if old == programName || old == "." || old == string(filepath.Separator) {
		return
	}
	data := dataHome()
	from, to := filepath.Join(data, old), filepath.Join(data, programName)
	if fi, err := os.Stat(filepath.Join(from, "localstorage")); err != nil || !fi.IsDir() {
		return
	}
	// os.Rename refuses a directory that is there already, even an empty one
	if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrExist) {
		log.Println("webview data:", err)
	}
}
