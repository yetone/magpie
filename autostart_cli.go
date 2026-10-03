package main

import (
	"fmt"
	"runtime"

	"github.com/yetone/magpie/internal/autostart"
)

// magpie autostart [on|off]: whether magpie opens at login, in the tray.
func autostartCmd(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: magpie autostart [on|off]")
	}
	if len(args) == 1 {
		switch args[0] {
		case "on", "off":
			if err := autostart.Set(args[0] == "on"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("usage: magpie autostart [on|off]")
		}
	}
	if autostart.Enabled() {
		if runtime.GOOS == "android" {
			fmt.Println(green.Render("✓"), "magpie starts the gateway at boot (requires Termux:Boot)")
		} else {
			fmt.Println(green.Render("✓"), "magpie opens at login, in the tray")
		}
	} else {
		fmt.Println("magpie doesn't open at login")
	}
	return nil
}
