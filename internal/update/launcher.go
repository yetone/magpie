package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A launcher is a command of its own a terminal runs, which runs magpie
// with arguments: claude-magpie runs `magpie claude run` (baranwang on X:
// `claude` on the user's own Claude account, `claude-magpie` through
// magpie). It goes where the terminal finds the magpie command, put there
// first as Settings › Command line puts it (AddCLI): beside its link in
// ~/.local/bin and the like, a shell script naming this program by the
// path that stays put (Program); on Windows a .cmd in the app's folder,
// which runs the magpie there as a terminal's `magpie` would (magpie.com
// before magpie.exe, PATHEXT's order), so the terminal waits for it.

// launcherMark is in every launcher magpie writes, so one of the user's
// own by the same name is never taken for magpie's.
const launcherMark = "written by magpie"

// Launchers are the launchers magpie puts beside its command, each name
// with the arguments it runs magpie with.
var Launchers = map[string][]string{"claude-magpie": {"claude", "run"}}

// LauncherFile is the file name of the launcher name on goos.
func LauncherFile(goos, name string) string {
	if goos == "windows" {
		return name + ".cmd"
	}
	return name
}

// launcherText is the launcher on goos that runs prog (on Windows, the
// magpie command beside it) with args and every argument it is given.
func launcherText(goos, prog string, args ...string) string {
	if goos == "windows" {
		var b strings.Builder
		b.WriteString("@echo off\r\nrem " + launcherMark + ": runs magpie " + strings.Join(args, " ") + "\r\n\"%~dp0magpie\"")
		for _, a := range args {
			b.WriteString(" " + strings.ReplaceAll(a, "%", "%%"))
		}
		b.WriteString(" %*\r\n")
		return b.String()
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# " + launcherMark + ": runs magpie " + strings.Join(args, " ") + "\nexec " + q(prog))
	for _, a := range args {
		b.WriteString(" " + q(a))
	}
	b.WriteString(" \"$@\"\n")
	return b.String()
}

// Launcher is where the launcher name is, "" when magpie hasn't put one
// on the PATH: in the folder the magpie command is found in.
func Launcher(name string) string {
	v := ReadCLI()
	if !v.Ours || v.Command == "" {
		return ""
	}
	p := filepath.Join(filepath.Dir(v.Command), LauncherFile(runtime.GOOS, name))
	if !ownLauncher(p) {
		return ""
	}
	return p
}

// ownLauncher says the file at p is a launcher magpie wrote.
func ownLauncher(p string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(string(b), launcherMark)
}

// AddLauncher writes the launcher name, which runs magpie with args,
// beside the magpie command a terminal runs, putting that there first
// when it isn't (AddCLI). A file of the user's own by that name is left
// as it is. It answers the launcher's path.
func AddLauncher(name string, args ...string) (string, error) {
	v := ReadCLI()
	if !v.Ours {
		var err error
		if v, err = addCLI(""); err != nil {
			return "", fmt.Errorf("the magpie command isn't on your PATH, and putting it there failed: %w", err)
		}
	}
	if !v.Ours || v.Command == "" {
		return "", errors.New("the magpie command isn't on your PATH: add it in Settings › Command line, then run this again")
	}
	p := filepath.Join(filepath.Dir(v.Command), LauncherFile(runtime.GOOS, name))
	return p, addLauncherBeside(v, name, args...)
}

// addLauncherBeside writes the launcher name beside v's magpie command.
func addLauncherBeside(v *CLI, name string, args ...string) error {
	prog, err := cliExe()
	if err != nil {
		return err
	}
	p := filepath.Join(filepath.Dir(v.Command), LauncherFile(runtime.GOOS, name))
	return writeLauncher(p, launcherText(runtime.GOOS, prog, args...))
}

// writeLauncher writes text to p, unless a file there is the user's own.
func writeLauncher(p, text string) error {
	if b, err := os.ReadFile(p); err == nil {
		if string(b) == text {
			return nil
		}
		if !strings.Contains(string(b), launcherMark) {
			return fmt.Errorf("%s is there already, and isn't magpie's: nothing was written", p)
		}
	}
	tmp := p + ".magpie-new"
	if err := os.WriteFile(tmp, []byte(text), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveLauncher takes the launcher name out, where magpie wrote it.
func RemoveLauncher(name string) (string, error) {
	p := Launcher(name)
	if p == "" {
		return "", nil
	}
	return p, os.Remove(p)
}
