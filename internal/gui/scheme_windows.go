package gui

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// registerScheme makes magpie:// links open this executable. There is no
// installer to do it, so every start points the per-user handler here.
func registerScheme() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\magpie`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", "URL:magpie")
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
