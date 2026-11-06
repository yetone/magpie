package gateway

import (
	"context"
	"errors"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// sideTimedOut is why a request magpie asks of itself (an image's
// description, a search) ended when its own time limit did: not the agent
// leaving, which the usage log said it was ("the agent canceled the
// request", #2318).
type sideTimedOut struct {
	what  string
	after time.Duration
}

func (e sideTimedOut) Error() string {
	return "no answer in " + e.after.String()
}

// sideDeadline bounds a request magpie asks of itself through the gateway
// (s.serve). The vendor has d from when the request last went out to it —
// each try's own d, a try after a failure starting it again — and the whole
// of it, with the waits before a try (a lane's slot, room in the minute, a
// provider resting or paused between tries), most. A 2-minute limit on the
// whole left a reasoning model describing a dense screenshot only what the
// queue and the pauses hadn't spent, and the image's turn failed (#2318).
// The context's cause is a sideTimedOut once either limit ends it.
func sideDeadline(ctx context.Context, what string, d, most time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	whole := time.AfterFunc(most, func() { cancel(sideTimedOut{what, most}) })
	var mu sync.Mutex
	var try *time.Timer
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			defer mu.Unlock()
			if try != nil {
				try.Stop()
			}
			try = time.AfterFunc(d, func() { cancel(sideTimedOut{what, d}) })
		}})
	return ctx, func() {
		whole.Stop()
		mu.Lock()
		if try != nil {
			try.Stop()
		}
		mu.Unlock()
		cancel(context.Canceled)
	}
}

// sideTimeout is the limit that ended ctx, if one of sideDeadline's did.
func sideTimeout(ctx context.Context) (sideTimedOut, bool) {
	var t sideTimedOut
	ok := errors.As(context.Cause(ctx), &t)
	return t, ok
}

// canceled is what the usage log says of a request whose context ended
// before it was answered: the agent canceled it, or magpie's own limit for
// a request it asked of itself ran out — still no fault of the vendor's,
// told apart (#2318). The first is no failure either, as the usage log and
// the pages count it (usage.CanceledBy); the second is.
func canceled(ctx context.Context) (int, string) {
	if t, ok := sideTimeout(ctx); ok {
		return usage.StatusCanceled, "magpie stopped waiting: its " + t.what + " had " + t.Error()
	}
	return usage.StatusCanceled, usage.AgentCanceled
}
