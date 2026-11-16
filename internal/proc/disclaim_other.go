//go:build !darwin || !cgo

package proc

import (
	"context"
	"os/exec"
)

// The context's error is returned whenever it ended, as on macOS: a
// killed command's Wait would otherwise say "signal: killed".
func combinedOutputOwn(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

// Responsible is the process macOS holds responsible for pid, 0 when that
// can't be told (here, always).
func Responsible(pid int) int { return 0 }
