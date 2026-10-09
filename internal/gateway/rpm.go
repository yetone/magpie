package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's MaxRPM (coeo91 on Discord: OpenRouter's free models take 20
// requests a minute, which a limit at once of 1 doesn't keep to) is how
// many requests each of its keys or accounts — a candidate's who(), as for
// MaxConcurrency — sends the vendor in any 60 seconds. Every request that
// goes out counts: each try of the forward loop (a retry, the next key or
// account, a fallback), each request a try makes on the way (a second ask
// after the vendor refused a beta or a shape), and the gateway's own asks
// of the provider (count_tokens, System One's decisions, embeddings,
// drawing, video and search). One more waits until the oldest of the
// minute is a minute old: in the order they came, and never longer than
// rpmWait (2 minutes, or the provider's QueueWait when that is shorter).
// One that would wait longer is turned away at once with a 429 whose
// Retry-After is when it could go; one whose agent goes away while it
// waits gives its place back and is never sent.
//
// A try waits for room in the minute after it has its slot at once
// (concurrency.go), right before it is sent, so the time the minute counts
// is when it really goes. Like a full lane, a key or account with no room
// in the minute gives way to another of its provider's, or of its routing
// group, that has room now (laneMate).
//
// The count is kept as each request's time to go, past and to come: room
// is there when fewer than the limit fall in the last minute, and else the
// next goes a minute after the limit-th from the end. Times only grow, so
// the list stays in order; one taken back (its agent gone) only leaves
// more room. Nothing waits while holding rpms.mu.

// rpmWindow is the rolling window MaxRPM counts over; tests shorten it.
var rpmWindow = time.Minute

// rpmLongest is the longest a request waits for room in the minute when
// the provider's QueueWait isn't shorter; tests shorten it.
var rpmLongest = 2 * time.Minute

// rpms are the times of the requests of each key or account with a MaxRPM.
type rpms struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

// errRPM turns a request away that would wait longer than it may for room
// in the minute: after is how long until it could go.
type errRPM struct {
	limit int
	after time.Duration
	wait  time.Duration
}

func (e *errRPM) Error() string {
	return fmt.Sprintf("%d requests a minute is its limit, and the next could go in %.0fs, longer than a request waits (%.0fs)", e.limit, math.Ceil(e.after.Seconds()), e.wait.Seconds())
}

// retryAfter is e's Retry-After, in whole seconds, at least 1.
func (e *errRPM) retryAfter() string {
	return fmt.Sprint(max(int(math.Ceil(e.after.Seconds())), 1))
}

// rpmWait is how long a request to p waits for room in the minute at most.
func rpmWait(p provider.Provider) time.Duration {
	if w := time.Duration(p.QueueWait) * time.Second; w > 0 && w < rpmLongest {
		return w
	}
	return rpmLongest
}

// prune drops who's times a minute old or older, at now.
func (l *rpms) prune(who string, now time.Time) []time.Time {
	ts := l.m[who]
	i := 0
	for i < len(ts) && !ts[i].After(now.Add(-rpmWindow)) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.m, who)
		return nil
	}
	l.m[who] = ts
	return ts
}

// reserve takes who's next time to go, at most longest from now (0: no
// bound): now when the minute has room, else a minute after the limit-th
// from the end.
func (l *rpms) reserve(who string, limit int, longest time.Duration, now time.Time) (time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string][]time.Time{}
	}
	ts := l.prune(who, now)
	at := now
	if len(ts) >= limit {
		at = ts[len(ts)-limit].Add(rpmWindow)
	}
	if d := at.Sub(now); longest > 0 && d > longest {
		return time.Time{}, &errRPM{limit: limit, after: d, wait: longest}
	}
	l.m[who] = append(ts, at)
	return at, nil
}

// giveBack takes back a time to go that wasn't used, its agent gone.
func (l *rpms) giveBack(who string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := l.m[who]
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Equal(at) {
			l.m[who] = append(ts[:i:i], ts[i+1:]...)
			break
		}
	}
	if len(l.m[who]) == 0 {
		delete(l.m, who)
	}
}

// wait waits for room in who's minute and counts the request in it. It
// answers how long it waited, and an *errRPM, with nothing counted, when
// the wait would be longer than longest, or ctx's error when ctx ended
// first, its time given back. A limit of 0 counts nothing.
func (l *rpms) wait(ctx context.Context, who string, limit int, longest time.Duration) (time.Duration, error) {
	if limit <= 0 {
		return 0, nil
	}
	now := time.Now()
	at, err := l.reserve(who, limit, longest, now)
	if err != nil {
		return 0, err
	}
	d := at.Sub(now)
	if d <= 0 {
		return 0, nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return d, nil
	case <-ctx.Done():
		l.giveBack(who, at)
		return time.Since(now), ctx.Err()
	}
}

// free says whether a request for who would go at once: no limit, or room
// in its minute.
func (l *rpms) free(who string, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		return true
	}
	return len(l.prune(who, time.Now())) < limit
}

// used is how many requests of who's are counted in its minute now, to go
// or gone.
func (l *rpms) used(who string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		return 0
	}
	return len(l.prune(who, time.Now()))
}

// rpmTicket, on a request's context, counts each request made with it
// against who's MaxRPM as it goes out (rpmTransport), waiting at most
// longest (0: no bound). paid is a place the forward loop took already,
// which the first request uses; try marks the forward loop's ticket for
// provider id's try.
type rpmTicket struct {
	l       *rpms
	id      string
	try     bool
	who     string
	limit   int
	longest time.Duration
	paid    atomic.Bool
}

type rpmKey struct{}

// meterWho is p's key or account as its limits count it: candidate.who for a
// provider not planned as a candidate.
func meterWho(p provider.Provider) string {
	switch {
	case p.Account != nil && p.Account.User != "":
		return candidate{p: p}.restKey()
	case p.Account == nil && p.Key != "":
		return candidate{p: p}.who()
	}
	return p.ID
}

// metered is ctx with requests made with it counted against p's MaxRPM
// for who, "" for p's own key or account. One counted for who already is
// kept, and so is the forward loop's for a try of p: a try's own requests
// are counted against the key or account the try is of.
func (s *Server) metered(ctx context.Context, p provider.Provider, who string) context.Context {
	limit := p.RPMLimit()
	if limit <= 0 {
		return ctx
	}
	if who == "" {
		who = meterWho(p)
	}
	if t, ok := ctx.Value(rpmKey{}).(*rpmTicket); ok && (t.who == who || t.try && t.id == p.ID) {
		return ctx
	}
	return context.WithValue(ctx, rpmKey{}, &rpmTicket{l: &s.rpms, who: who, limit: limit, longest: rpmWait(p)})
}

// paidFor is ctx whose first request to who is counted already: the
// forward loop waited for it. The ones the try makes after it (a second
// ask after the vendor refused a beta or a shape) wait as long as it
// takes: the try is under way, and turned away it would rest its key or
// account for nothing. That is never long, the minute's places to come
// being bounded by rpmWait.
func (s *Server) paidFor(ctx context.Context, p provider.Provider, who string) context.Context {
	t := &rpmTicket{l: &s.rpms, id: p.ID, try: true, who: who, limit: p.RPMLimit()}
	t.paid.Store(true)
	return context.WithValue(ctx, rpmKey{}, t)
}

// rpmTransport counts each request whose context has a ticket against its
// MaxRPM before it goes out, waiting for room in the minute.
type rpmTransport struct{ next http.RoundTripper }

func (t rpmTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if tk, ok := r.Context().Value(rpmKey{}).(*rpmTicket); ok && !tk.paid.CompareAndSwap(true, false) {
		if _, err := tk.l.wait(r.Context(), tk.who, tk.limit, tk.longest); err != nil {
			if r.Body != nil {
				r.Body.Close()
			}
			var e *errRPM
			if errors.As(err, &e) {
				return nil, fmt.Errorf("%s: %w", tk.who, err)
			}
			return nil, err
		}
	}
	return t.next.RoundTrip(r)
}
