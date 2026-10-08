//go:build !windows

package proc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// a CLI that is a script starting another program: its timeout ends the
// call, though what it started still holds the output
func TestCommandContextTimeoutWithChild(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "cli")
	testenv.Program(t, sh, "#!/bin/sh\nsleep 30\n")
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	CommandContext(ctx, sh).Output()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the call took %v past its timeout", d)
	}
}
