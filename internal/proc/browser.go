package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// OpenBrowser opens url in the user's default browser. The error is the
// launcher's failing to start, for the caller to show with the link to open
// by hand: a sign-in that waits on a page that never opened only times out
// (#2043).
func OpenBrowser(url string) error {
	name, args := browserCommand(runtime.GOOS, os.Getenv("SystemRoot"), url)
	return startBrowser(Command(name, args...))
}

// startBrowser starts cmd and reaps it once it ends; tests stand in for it.
var startBrowser = func(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	ClearProcessEfficiencyMode(cmd.Process.Pid)
	go cmd.Wait() //nolint:errcheck
	return nil
}

// browserCommand is the program and arguments that open url on goos. On
// Windows rundll32 is named by its path in the system folder: by its bare
// name Go looks it up with PATHEXT's extensions, and a PATHEXT without
// .EXE (one WSL passed on as ".CPL", #2043) finds none, though it is there.
func browserCommand(goos, systemRoot, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		exe := "rundll32"
		if systemRoot != "" {
			exe = filepath.Join(systemRoot, "System32", "rundll32.exe")
		}
		return exe, []string{"url.dll,FileProtocolHandler", url}
	case "android":
		return "termux-open-url", []string{url}
	default:
		return "xdg-open", []string{url}
	}
}
