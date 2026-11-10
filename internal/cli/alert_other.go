//go:build !windows

package cli

import (
	"os/exec"
	"runtime"
	"strconv"

	"github.com/yetone/magpie/internal/proc"
)

// alert says text in a box of the system's: an alert on a Mac, zenity's
// where it is installed; nothing more elsewhere (stderr has it).
func alert(title, text string) {
	switch {
	case runtime.GOOS == "darwin":
		proc.Command("/usr/bin/osascript", "-e", "display alert "+strconv.Quote(title)+" message "+strconv.Quote(text)+" as critical").Run()
	default:
		if z, err := exec.LookPath("zenity"); err == nil {
			proc.Command(z, "--error", "--title="+title, "--text="+text).Run()
		}
	}
}
