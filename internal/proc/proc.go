// Package proc makes the commands magpie runs: agents' CLIs, the probes
// that ask them what they have, the system's own tools. Every one is made
// here rather than with os/exec, so that on Windows none of them opens a
// window.
//
// The desktop app on Windows is linked as a GUI program and has no console.
// A console program it starts (devin, claude, codex, cursor-agent, grok, the
// probes behind them) is given a new console by Windows, and with it a
// window, one for every run; so a child of a magpie with no console is
// started with CREATE_NO_WINDOW, which gives it a console without one. A
// magpie run in a terminal, or the desktop app started from one with a
// command, lets its children share that console as before, so Ctrl+C there
// reaches them too. Nothing magpie starts reads or writes the terminal
// itself: its output is piped back, which a hidden console doesn't change.
package proc

import (
	"context"
	"os/exec"
	"time"
)

// Command is exec.Command, with no window of its own on Windows.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	batch(cmd)
	hide(cmd)
	return cmd
}

// CommandContext is exec.CommandContext, with no window of its own on
// Windows.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// the context ending kills the command, but not what it started: a CLI
	// that is a script running node leaves node holding the output, and
	// Output waited for it however long it took (#123) — its pipes are
	// closed a moment after instead
	cmd.WaitDelay = waitDelay
	batch(cmd)
	hide(cmd)
	return cmd
}

var waitDelay = 2 * time.Second
