//go:build !windows

package proc

import (
	"context"
	"os/exec"
)

// listCommand is ps, in its POSIX form, which macOS, Linux and the BSDs
// all take: every process, its id and its whole command line.
func listCommand(ctx context.Context, _ string) *exec.Cmd {
	return CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "args=")
}
