package proc

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// #4872: with Settings' Detect agents in WSL off, nothing magpie makes
// starts wsl.exe, by any name or path: a request's stand-in asked it past
// agent's gates, and on a computer without WSL each ask put up a window
// offering to install it.
func TestWSLOffStartsNoWSL(t *testing.T) {
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	names := []string{"wsl.exe", "WSL.EXE", "wsl", `C:\Windows\System32\wsl.exe`, "/mnt/c/Windows/System32/wsl.exe"}
	// each is only started while it is refused: on, wsl.exe isn't run
	makers := map[string]func(name string) *exec.Cmd{
		"Command": func(n string) *exec.Cmd { return Command(n, "-l", "--running", "-q") },
		"CommandContext": func(n string) *exec.Cmd {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			return CommandContext(ctx, n, "-l", "-q")
		},
		"ProbeContext": func(n string) *exec.Cmd {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			return ProbeContext(ctx, n, "-l", "-q")
		},
	}

	if err := settings.Save(settings.Settings{NoWSLAgents: true}); err != nil {
		t.Fatal(err)
	}
	for how, mk := range makers {
		for _, n := range names {
			c := mk(n)
			if !errors.Is(c.Err, ErrWSLOff) {
				t.Errorf("detection off, %s(%q) wasn't refused: %v", how, n, c.Err)
				continue
			}
			if _, err := c.Output(); !errors.Is(err, ErrWSLOff) || c.Process != nil {
				t.Errorf("detection off, %s(%q) started: %v", how, n, err)
			}
		}
		// only wsl.exe itself
		for _, n := range []string{"wslconfig.exe", "notwsl.exe", `C:\wsl\git.exe`} {
			if c := mk(n); errors.Is(c.Err, ErrWSLOff) {
				t.Errorf("%s(%q) refused as wsl.exe", how, n)
			}
		}
	}

	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	for how, mk := range makers {
		if c := mk("wsl.exe"); errors.Is(c.Err, ErrWSLOff) {
			t.Errorf("detection on, %s(wsl.exe) refused", how)
		}
	}
}
