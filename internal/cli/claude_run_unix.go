//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

// claudeRunOS starts claude and waits for it, to see what its session
// left in the user's settings.json (agent.ClaudeLaunchAfter). It runs in
// magpie's process group, so the terminal's Ctrl+C and Ctrl+\ reach it
// as they would reach claude started alone, and magpie only waits: a
// stop (Ctrl+Z) stops magpie beside it, so the shell has the terminal
// back, and fg continues both. A SIGTERM or SIGHUP sent to magpie alone
// is passed on. Its exit code is magpie's.
func claudeRunOS(bin string, args, env []string) error {
	cmd := proc.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// caught, not ignored: an ignored signal stays ignored in claude
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for s := range sigs {
			if s == syscall.SIGTERM || s == syscall.SIGHUP {
				_ = cmd.Process.Signal(s)
			}
		}
	}()
	code, err := proc.WaitFollowingStops(cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code: code}
	}
	return nil
}
