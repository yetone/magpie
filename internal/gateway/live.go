package gateway

import (
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// liveRow counts a reply as the gateway writes it to the agent, for the
// Routing page to show while it is out (#4951, Canda15): how many bytes
// the agent has had, when the model last wrote content, and what that
// content is worth in output tokens. A reply mid-reasoning or a long one
// otherwise looks stuck. It counts and drops: no byte is kept or changed.
//
// captureResponseWriter.Write, which every byte to the agent passes,
// calls observe; it may be called from a held stream's keepalive and the
// try's goroutine in turn, so everything is under mu, which nothing that
// blocks is called under.
type liveRow struct {
	mu sync.Mutex
	// bytes is every byte written to the agent, SSE framing and the
	// gateway's own keepalive comments included
	bytes int64
	// contentAt is the epoch ms of the last write that was not only SSE
	// comments: a keepalive never moves it, so a stuck reply shows still
	contentAt int64
	// provider and model are the try's that answers (routed), whose
	// learned rate estimates the tokens
	provider, model string
	counter         liveContentCounter
	ended           bool
	f               *inflight
	// tr: f's trace, nil when its routes are in none
	tr *trace
}

// liveBegin starts counting f's reply, from is the agent's protocol, which
// decides what its content is. A request not counted in flight (the
// gateway's own) has no route to tell, and is not counted.
func liveBegin(f *inflight, model string, from provider.Protocol) *liveRow {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	tr := f.tr
	f.mu.Unlock()
	l := &liveRow{f: f, tr: tr, model: model, counter: liveContentCounter{proto: from}}
	f.live.Store(l)
	return l
}

// liveOfInflight is f's counter, nil when there is none.
func liveOfInflight(f *inflight) *liveRow {
	if f == nil {
		return nil
	}
	return f.live.Load()
}

// observe counts one write to the agent and tells its route.
func (l *liveRow) observe(p []byte) {
	if l == nil || len(p) == 0 {
		return
	}
	l.mu.Lock()
	l.bytes += int64(len(p))
	if !commentOnly(p) {
		l.contentAt = time.Now().UnixMilli()
	}
	l.counter.add(p)
	bytes, contentAt, content := l.bytes, l.contentAt, l.counter.total
	provider, model := l.provider, l.model
	l.mu.Unlock()
	l.tell(bytes, contentAt, provider, model, content)
}

// tell writes the running totals on the request's route, the first one
// begun for it (noteRoute); before it is begun a later write tells it,
// the numbers being totals.
func (l *liveRow) tell(bytes, contentAt int64, provider, model string, content int64) {
	if l.tr == nil {
		return
	}
	rt := l.f.route.Load()
	if rt == nil {
		return
	}
	l.tr.liveBytes(rt, bytes, contentAt, liveRates.estimate(provider, model, content))
}

// commentOnly reports whether p holds SSE comment lines and blank lines
// only, at least one comment: the gateway's keepalive. It allocates
// nothing, as it runs on every write.
func commentOnly(p []byte) bool {
	saw, start := false, true
	for _, c := range p {
		switch {
		case c == '\n':
			start = true
		case c == '\r' && start:
		case start:
			if c != ':' {
				return false
			}
			saw, start = true, false
		}
	}
	return saw
}

// totals are the row's bytes, content bytes and last content time.
func (l *liveRow) totals() (bytes, content, contentAt int64) {
	if l == nil {
		return 0, 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bytes, l.counter.total, l.contentAt
}

// routed names the provider and model of the try about to answer: after a
// fallback it is another than the first.
func (l *liveRow) routed(provider, model string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.provider = provider
	if model != "" {
		l.model = model
	}
	l.mu.Unlock()
}

// finish ends the count once: the last event still in the buffer is
// counted, and a streamed reply that did not fail and whose vendor said
// its output tokens is learned from. An agent that went away after the
// last content still makes a sample, the usage in hand saying the vendor
// finished the reply.
func (l *liveRow) finish(failed bool, output int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		return
	}
	l.ended = true
	l.counter.flush()
	sse, content := l.counter.sse, l.counter.total
	bytes, contentAt, provider, model := l.bytes, l.contentAt, l.provider, l.model
	l.mu.Unlock()
	if sse && !failed && output > 0 {
		liveRates.learn(provider, model, content, output)
	}
	l.tell(bytes, contentAt, provider, model, content)
}
