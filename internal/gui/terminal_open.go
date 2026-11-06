package gui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/proc"
)

// Opening a session or an agent in a terminal off a Mac (lileo0668 on
// Discord, #4474). The lines are the ones the Sessions page copies:
// PowerShell's on Windows, a POSIX shell's on Linux.

// terminalHere: this system has a way to open a terminal on a line.
func terminalHere() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows" || runtime.GOOS == "linux"
}

// look finds a terminal's program: a var so a test says what is installed.
var look = proc.Look

// winTerminals are Windows' choices: Windows Terminal, PowerShell 7 and
// Windows PowerShell, which a console of its own opens in whatever host
// Windows is set to (Windows Terminal on Windows 11), the system default.
var winTerminals = []terminalApp{
	{ID: "wt", Name: "Windows Terminal", Path: "wt.exe"},
	{ID: "pwsh", Name: "PowerShell", Path: "pwsh.exe"},
	{ID: "powershell", Name: "Windows PowerShell", Path: "powershell.exe"},
}

// linuxTerminals are the Linux terminals offered by name, each with the
// words before the command it runs (sh and the script).
var linuxTerminals = []struct {
	terminalApp
	pre []string
}{
	{terminalApp{ID: "gnome-terminal", Name: "GNOME Terminal"}, []string{"--"}},
	{terminalApp{ID: "konsole", Name: "Konsole"}, []string{"-e"}},
	{terminalApp{ID: "xfce4-terminal", Name: "Xfce Terminal"}, []string{"-x"}},
	{terminalApp{ID: "kitty", Name: "kitty"}, nil},
	{terminalApp{ID: "alacritty", Name: "Alacritty"}, []string{"-e"}},
	{terminalApp{ID: "ghostty", Name: "Ghostty"}, []string{"-e"}},
	{terminalApp{ID: "foot", Name: "foot"}, nil},
	{terminalApp{ID: "wezterm", Name: "WezTerm"}, []string{"start", "--"}},
	{terminalApp{ID: "terminator", Name: "Terminator"}, []string{"-x"}},
	{terminalApp{ID: "xterm", Name: "XTerm"}, []string{"-e"}},
	// the system's picks: xdg-terminal-exec (Omarchy, the XDG spec's) and
	// Debian's alternative, which takes -e by its policy
	{terminalApp{ID: "xdg-terminal-exec", Name: "xdg-terminal-exec"}, nil},
	{terminalApp{ID: "x-terminal-emulator", Name: "x-terminal-emulator"}, []string{"-e"}},
}

// discoverOwnTerminals is discoverTerminals off a Mac: the terminals
// installed, and the one the system default opens.
func discoverOwnTerminals(goos string) (terminalDiscovery, error) {
	var found terminalDiscovery
	switch goos {
	case "windows":
		for _, app := range winTerminals {
			if p, err := look(app.Path); err == nil {
				found.Apps = append(found.Apps, terminalApp{ID: app.ID, Name: app.Name, Path: p})
			}
		}
		found.Default = "powershell"
	case "linux":
		for _, t := range linuxTerminals {
			if p, err := look(t.ID); err == nil {
				found.Apps = append(found.Apps, terminalApp{ID: t.ID, Name: t.Name, Path: p})
			}
		}
		if bin, _ := linuxDefault(); bin != "" {
			id := linuxTerminalID(bin)
			found.Default = id
			if !hasApp(found, id) {
				found.Apps = append(found.Apps, terminalApp{ID: id, Name: id, Path: bin})
			}
		}
	default:
		return found, errors.New("session terminals are only available in the desktop app")
	}
	if len(found.Apps) == 0 {
		return found, errors.New("no terminal found")
	}
	return found, nil
}

func hasApp(found terminalDiscovery, id string) bool {
	for _, app := range found.Apps {
		if app.ID == id {
			return true
		}
	}
	return false
}

// linuxDefault is the terminal the system default opens: the user's
// $TERMINAL, else xdg-terminal-exec, else x-terminal-emulator, else the
// first of linuxTerminals installed; and the words before the command.
func linuxDefault() (string, []string) {
	if f := strings.Fields(os.Getenv("TERMINAL")); len(f) > 0 {
		if p, err := look(f[0]); err == nil {
			return p, linuxPre(linuxTerminalID(p))
		}
	}
	for _, id := range []string{"xdg-terminal-exec", "x-terminal-emulator"} {
		if p, err := look(id); err == nil {
			return p, linuxPre(id)
		}
	}
	for _, t := range linuxTerminals {
		if p, err := look(t.ID); err == nil {
			return p, t.pre
		}
	}
	return "", nil
}

// linuxTerminalID is a terminal's program named as linuxTerminals name it:
// x-terminal-emulator's own name stays (its -e is the policy's), a link
// otherwise is the terminal it leads to, a .wrapper (Debian's) without it.
func linuxTerminalID(path string) string {
	base := filepath.Base(path)
	if base == "x-terminal-emulator" || base == "xdg-terminal-exec" {
		return base
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		base = filepath.Base(real)
	}
	return strings.TrimSuffix(base, ".wrapper")
}

// linuxPre is the words a terminal takes before the command to run: -e,
// the xterm way, for one not listed.
func linuxPre(id string) []string {
	for _, t := range linuxTerminals {
		if t.ID == id {
			return t.pre
		}
	}
	return []string{"-e"}
}

// linuxTerminalArgv is the terminal's argv running script in sh, for the
// choice made in Settings ("" and "system" the system default).
func linuxTerminalArgv(choice, script string) ([]string, error) {
	var bin string
	var pre []string
	if choice == "" || choice == "system" {
		if bin, pre = linuxDefault(); bin == "" {
			return nil, errors.New("no terminal found: install one, or set $TERMINAL")
		}
	} else {
		p, err := look(choice)
		if err != nil {
			return nil, fmt.Errorf("the selected terminal %q is no longer available", choice)
		}
		bin, pre = p, linuxPre(choice)
	}
	return append(append([]string{bin}, pre...), "sh", script), nil
}

// winTerminalArgv is the argv opening a PowerShell on line, left open
// after it. The line goes as -EncodedCommand: no script file, so no
// execution policy, no encoding of a file's to guess and no quoting
// between here and PowerShell. A shell goes through cmd's start, which
// gives it a console of its own: magpie's children have none
// (CREATE_NO_WINDOW), and a shell started from here would write to
// handles magpie passed it, not a window. Windows Terminal opens its own.
func winTerminalArgv(line, choice string) ([]string, error) {
	shell := func(ids ...string) (string, error) {
		for _, id := range ids {
			for _, app := range winTerminals {
				if app.ID == id {
					if p, err := look(app.Path); err == nil {
						return p, nil
					}
				}
			}
		}
		return "", fmt.Errorf("the selected terminal %q is no longer available", choice)
	}
	ps := []string{"-NoExit", "-NoLogo", "-EncodedCommand", psEncode(line)}
	started := func(id string) ([]string, error) {
		p, err := shell(id)
		if err != nil {
			return nil, err
		}
		// start's title, empty: Go writes "" as "", and a quoted path
		// after it is the program, not the title
		return append([]string{"cmd.exe", "/c", "start", "", p}, ps...), nil
	}
	switch choice {
	case "", "system", "powershell":
		return started("powershell")
	case "pwsh":
		return started("pwsh")
	case "wt":
		wt, err := shell("wt")
		if err != nil {
			return nil, err
		}
		p, err := shell("pwsh", "powershell")
		if err != nil {
			return nil, err
		}
		// wt reads ; as the end of its own command: the line is encoded
		return append([]string{wt, "new-tab", "--", p}, ps...), nil
	}
	return nil, fmt.Errorf("the selected terminal %q is no longer available", choice)
}

// psEncode is a line as PowerShell's -EncodedCommand takes it: UTF-16LE,
// in base64.
func psEncode(line string) string {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune(line)) {
		b.WriteByte(byte(u))
		b.WriteByte(byte(u >> 8))
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func openWinTerminal(line, choice string) error {
	argv, err := winTerminalArgv(line, choice)
	if err != nil {
		return err
	}
	return startTerminal(proc.Command(argv[0], argv[1:]...))
}

// openLinuxTerminal runs line from a script of its own in sh, as a Mac's
// .command file runs it, then the user's shell, so the window stays.
func openLinuxTerminal(line, choice string) error {
	f, err := os.CreateTemp("", "magpie-resume-*.sh")
	if err != nil {
		return err
	}
	_, err = f.WriteString("#!/bin/sh\n" + line + "\nexec \"${SHELL:-/bin/sh}\"\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	var argv []string
	if err == nil {
		argv, err = linuxTerminalArgv(choice, f.Name())
	}
	if err == nil {
		err = startTerminal(proc.Command(argv[0], argv[1:]...))
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	// sh holds it open; the name goes
	time.AfterFunc(time.Minute, func() { os.Remove(f.Name()) })
	return nil
}

// startTerminal starts a terminal and lets it run: one that quits at once
// failing (no display, a bad option) is told of, with what it said.
func startTerminal(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if msg := strings.TrimSpace(out.String()); msg != "" {
				return fmt.Errorf("%s: %w: %s", filepath.Base(cmd.Path), err, msg)
			}
			return fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err)
		}
	case <-time.After(terminalFails):
	}
	return nil
}

// terminalFails is how long a terminal that fails at once is waited for.
const terminalFails = time.Second
