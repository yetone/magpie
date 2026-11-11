package gateway

import (
	"context"
	"sync"
)

// entryKey carries a request's try the entry point Claude Code ran it by.
type entryKey struct{}

// entryNote is how a Claude Code run was made, noted by the subscription
// bridge as the run starts and read for the try's row on the Routing page.
type entryNote struct {
	mu       sync.Mutex
	set, tui bool
	why      string
}

// withEntry has the try's context carry a note of the entry point its run
// takes; the func returns it: "tui" or "print" (and claudePrint's code for
// why it is -p), "" for a try that ran no Claude Code.
func withEntry(ctx context.Context) (context.Context, func() (entry, why string)) {
	n := &entryNote{}
	return context.WithValue(ctx, entryKey{}, n), func() (string, string) {
		n.mu.Lock()
		defer n.mu.Unlock()
		switch {
		case !n.set:
			return "", ""
		case n.tui:
			return "tui", ""
		}
		return "print", n.why
	}
}

// noteEntry notes the run ctx belongs to is the interactive mode (tui) or
// -p, and for -p claudePrint's code. The last run of a try is the one told,
// as a run started again is.
func noteEntry(ctx context.Context, tui bool, why string) {
	if n, ok := ctx.Value(entryKey{}).(*entryNote); ok {
		n.mu.Lock()
		n.set, n.tui, n.why = true, tui, why
		n.mu.Unlock()
	}
}
