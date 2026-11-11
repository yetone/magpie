package gateway

import (
	"context"
	"testing"
)

// A try's context carries how its Claude Code run was made: the
// interactive mode, or -p and why, the last run's when it was started
// again, and nothing for a try that ran none.
func TestEntryNoted(t *testing.T) {
	ctx, entry := withEntry(context.Background())
	if e, w := entry(); e != "" || w != "" {
		t.Fatalf("before any run: %q %q", e, w)
	}
	noteEntry(ctx, false, "third-party")
	if e, w := entry(); e != "print" || w != "third-party" {
		t.Fatalf("-p for a refused prompt: %q %q", e, w)
	}
	noteEntry(ctx, true, "")
	if e, w := entry(); e != "tui" || w != "" {
		t.Fatalf("started again in the interactive mode: %q %q", e, w)
	}
	// a context that carries no note is told nothing, and doesn't panic
	noteEntry(context.Background(), true, "")
}
