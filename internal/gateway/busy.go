package gateway

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Busy is what a restart of the magpie serving the gateway would cut
// short (#577): the agents' requests in flight — a reply streaming, an
// image being made — and the turns of Claude Code run through the
// subscription bridge waiting on their caller's tool results, whose
// Claude Code lives only in this process. A request in flight is one
// that does work (not a GET: the model lists, the info another magpie
// asks for every 15 s); the bridge's own helper calls are not counted
// apart, a turn waiting on them being in Tools. Last is when the last
// counted request ended, so a caller can tell a tool chain's gap — the
// agent running a tool between two requests — from the end of its turn.
//
// Bridge and BridgeLast are the same for the bridge's turns alone, which
// a restart that hands the gateway over (handover.go) still waits for:
// their Claude Code lives in this process, and only here can the turn go
// on. Every other request finishes in the magpie that hands over while
// the new one takes the next.
type Busy struct {
	Requests   int       `json:"requests"`
	Tools      int       `json:"tools"`
	Last       time.Time `json:"-"`
	Bridge     int       `json:"-"`
	BridgeLast time.Time `json:"-"`
}

// Any is whether there is anything in flight.
func (b Busy) Any() bool { return b.Requests > 0 || b.Tools > 0 }

// Busy says what is in flight at the gateway now.
func (s *Server) Busy() Busy {
	b := Busy{Requests: int(s.inFlight.Load()), Tools: s.subscription.parked(), Bridge: int(s.bridgeInFlight.Load())}
	if n := s.lastDone.Load(); n != 0 {
		b.Last = time.Unix(0, n)
	}
	if n := s.bridgeDone.Load(); n != 0 {
		b.BridgeLast = time.Unix(0, n)
	}
	return b
}

// CodexBusy is how many of Codex's requests are in flight and when its
// last ended: a turn of the Codex app's that a restart of its app-server
// would cut short (provider.KeepCodexAppOnItsProvider). Other agents'
// requests don't hold it.
func (s *Server) CodexBusy() (int, time.Time) {
	n := 0
	s.active.mu.Lock()
	for _, f := range s.active.m {
		if f.agent == "codex" {
			n++
		}
	}
	s.active.mu.Unlock()
	var last time.Time
	if d := s.codexDone.Load(); d != 0 {
		last = time.Unix(0, d)
	}
	return n, last
}

// bridgeTurn counts a turn served through the subscription bridge for
// Busy.Bridge until done is called.
func (s *Server) bridgeTurn() (done func()) {
	s.bridgeInFlight.Add(1)
	return func() {
		s.bridgeDone.Store(time.Now().UnixNano())
		s.bridgeInFlight.Add(-1)
	}
}

// counted is h with its requests counted for Busy.
func (s *Server) counted(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || strings.HasPrefix(r.URL.Path, "/_magpie/") || strings.HasPrefix(r.URL.Path, "/mcp/") {
			h.ServeHTTP(w, r)
			return
		}
		s.inFlight.Add(1)
		r, done := s.active.add(r)
		shut := func() {}
		if f := inflightOf(r.Context()); f != nil {
			f.mu.Lock()
			f.tr = &s.trace
			f.curves = &s.curves
			f.mu.Unlock()
			// the session's curve counts it in flight, its tool gaps
			// being the time between
			shut = s.curves.open(f.agent, f.session)
		}
		w, r, unwatch := s.watchUnrouted(w, r, inflightOf(r.Context()))
		defer func() {
			// a panic is net/http's to recover, as before; the routes it
			// left in flight end first (#2386)
			if p := recover(); p != nil {
				s.endBroken(inflightOf(r.Context()))
				defer panic(p)
			}
			unwatch()
			shut()
			done()
			s.lastDone.Store(time.Now().UnixNano())
			if f := inflightOf(r.Context()); f != nil && f.agent == "codex" {
				s.codexDone.Store(time.Now().UnixNano())
			}
			if s.inFlight.Add(-1) == 0 {
				s.armRelease(s.idle.restFor())
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// busyCounters are the Server's counts for Busy.
type busyCounters struct {
	inFlight atomic.Int64
	lastDone atomic.Int64 // unix ns
	active   inflights    // what each is, for /v1/magpie/concurrency
	// the subscription bridge's turns among them, and when the last ended
	bridgeInFlight atomic.Int64
	bridgeDone     atomic.Int64 // unix ns
	// when Codex's last request ended (CodexBusy)
	codexDone atomic.Int64 // unix ns
	// handing back what requests left once it rests (idle_memory.go)
	idle idleMemory
	// each session's output speed, for the Sessions page (session_speed.go)
	curves curveMeter
}

// parked is how many runs wait on their caller's tool results.
func (b *subscriptionBridge) parked() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	runs := make([]*subscriptionRun, 0, len(b.runs))
	for _, run := range b.runs {
		runs = append(runs, run)
	}
	b.mu.Unlock()
	n := 0
	for _, run := range runs {
		run.mu.Lock()
		if !run.closed && !run.parkedAt.IsZero() {
			n++
		}
		run.mu.Unlock()
	}
	return n
}
