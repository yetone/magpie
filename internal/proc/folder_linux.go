package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// OpenFolder shows path in the desktop's file manager and returns once the
// opener is started, not when the file manager is done. Wails' own way runs
// the opener to its end under a ten-second context: a file manager that
// isn't running yet stays in the foreground of what xdg-open starts, so the
// click hung for ten seconds and then killed the window that had just opened
// (pkh1585 on Discord). The opener gets a session of its own and no stdio,
// so it outlives the request and the click.
func OpenFolder(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		path = filepath.Dir(path)
	}
	var last error
	for _, c := range [][]string{{"xdg-open"}, {"gio", "open"}, {"exo-open"}} {
		bin, err := exec.LookPath(c[0])
		if err != nil {
			last = err
			continue
		}
		cmd := Command(bin, append(c[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setsid = true
		if err := cmd.Start(); err != nil {
			last = err
			continue
		}
		go cmd.Wait() //nolint:errcheck
		return nil
	}
	if last == nil {
		last = errors.New("no opener")
	}
	return last
}
