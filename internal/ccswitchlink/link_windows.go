package ccswitchlink

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// schemeKey is the per-user handler of ccswitch://, which both CC Switch's
// MSI and an older magpie wrote; tests point it elsewhere.
var schemeKey = `Software\Classes\ccswitch`

// executable is this program's path; tests stand in for it.
var executable = os.Executable

// start starts CC Switch on the link; tests stand in for it.
var start = func(exe, link string) error { return proc.StartReaped(proc.Command(exe, link)) }

// forward runs CC Switch with the link as its argument, as the handler its
// MSI writes does ("…cc-switch.exe" "%1"). Then a handler that runs this
// magpie is pointed at CC Switch, as its MSI writes it.
func forward(link string) error {
	self, _ := executable()
	cmd := handler()
	exe := ccSwitch(cmd, self, appdir.Getenv("LOCALAPPDATA"))
	if exe == "" {
		return ErrNotInstalled
	}
	if err := start(exe, link); err != nil {
		return err
	}
	if self != "" && samePath(cmd, self) {
		return handBack(exe)
	}
	return nil
}

// ccSwitch is CC Switch's executable: the scheme's handler when it runs a
// program other than this one that is there, or where CC Switch's MSI
// installs it. "" when neither is.
func ccSwitch(cmd, self, localAppData string) string {
	if cmd != "" && !samePath(cmd, self) && exists(cmd) {
		return cmd
	}
	if localAppData != "" {
		if p := filepath.Join(localAppData, "Programs", "CC Switch", "cc-switch.exe"); exists(p) {
			return p
		}
	}
	return ""
}

// handler is the program the per-user ccswitch:// handler runs, "" when
// there is none.
func handler() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, schemeKey+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("")
	return commandExe(v)
}

// commandExe is the program of a shell\open\command value: the quoted
// first word, or the first word.
func commandExe(v string) string {
	v = strings.TrimSpace(v)
	if rest, ok := strings.CutPrefix(v, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(v, " ")
	return exe
}

// handBack writes the handler as CC Switch's MSI does (its
// wix/per-user-main.wxs).
func handBack(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, schemeKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", "URL:com.ccswitch.desktop protocol")
	_ = k.SetStringValue("URL Protocol", "")
	if icon, _, err := registry.CreateKey(k, `DefaultIcon`, registry.SET_VALUE); err == nil {
		_ = icon.SetStringValue("", `"`+exe+`",0`)
		icon.Close()
	}
	cmd, _, err := registry.CreateKey(k, `shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", `"`+exe+`" "%1"`)
}

func samePath(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func exists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
