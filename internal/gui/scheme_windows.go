package gui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// registerScheme makes magpie:// links open this executable. There is no
// installer to do it, so every start points the per-user handler here.
func registerScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return errors.Join(setScheme("magpie", exe), registerCCSwitch(exe))
}

// registerCCSwitch lets magpie open ccswitch:// links, the one-click import
// links relays make for CC Switch, but only while no other program has
// them: CC Switch installed here, per user or for the machine, keeps its
// links. The merged view (HKEY_CLASSES_ROOT) is asked, so both count; a
// handler whose program is gone is replaced.
func registerCCSwitch(exe string) error {
	if run := schemeCommand(registry.CLASSES_ROOT, "ccswitch"); run != "" && !strings.EqualFold(filepath.Clean(run), filepath.Clean(exe)) {
		if _, err := os.Stat(run); err == nil {
			return nil
		}
	}
	return setScheme("ccswitch", exe)
}

// schemeCommand is the program a scheme's open command runs, "" when none.
func schemeCommand(root registry.Key, scheme string) string {
	k, err := registry.OpenKey(root, scheme+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("")
	run, _, _ := strings.Cut(strings.TrimPrefix(v, `"`), `"`)
	return run
}

// setScheme points the per-user handler of scheme at exe.
func setScheme(scheme, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+scheme, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", "URL:"+scheme)
	_ = k.SetStringValue("URL Protocol", "")
	icon, _, err := registry.CreateKey(k, `DefaultIcon`, registry.SET_VALUE)
	if err == nil {
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

// keepWebData: WebView2's data folder isn't named after the program.
func keepWebData() {}
