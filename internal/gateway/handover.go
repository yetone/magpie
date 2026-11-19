package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// A restart to update hands the gateway over rather than ending it
// (internal/handoff): the magpie updating gives the new version the
// socket it listens on, the new one serves on it at once, and the old
// stops accepting and finishes the requests it has (handedOffDrain at
// most) before it quits. No connection is refused on the way: the socket
// is never closed, only accepted from by the other process.

// handedOffDrain is how long a magpie that handed the gateway over waits
// for its requests in flight to finish: the longest stream an agent has
// been seen to keep, as `make dev`'s handover waits.
var handedOffDrain = 15 * time.Minute

// HandedOffDrain is handedOffDrain: the app that handed the gateway over
// quits by then and a little after, whatever holds it up (gui.quitBy).
func HandedOffDrain() time.Duration { return handedOffDrain }

// shortDrain is how long the gateway's end waits for its requests when
// nothing serves the port after it: a quit, or a restart that waits.
var shortDrain = 2 * time.Second

// inherited is the listener the magpie before this one handed over, taken
// by the first Listen of its address; ack is called once this gateway
// serves on it, which tells the one before to stop accepting.
var inherited struct {
	sync.Mutex
	ln  net.Listener
	ack func()
}

// Inherit has this process's gateway serve on ln, the socket the magpie
// it replaces listened on, rather than listening itself; ack is called
// once it serves.
func Inherit(ln net.Listener, ack func()) {
	inherited.Lock()
	defer inherited.Unlock()
	if inherited.ln != nil {
		inherited.ln.Close()
	}
	inherited.ln, inherited.ack = ln, ack
}

// Inherited is whether a listener handed over is waiting to be served:
// the gateway's port is then held by the magpie that handed it over, and
// is this process's to serve all the same.
func Inherited() bool {
	inherited.Lock()
	defer inherited.Unlock()
	return inherited.ln != nil
}

// takeInherited is the listener handed over when it listens on addr, and
// the ack to call once it serves. One on another address (the settings
// changed in between) is closed: the port is then listened on as usual.
func takeInherited(addr string) (net.Listener, func()) {
	inherited.Lock()
	defer inherited.Unlock()
	ln, ack := inherited.ln, inherited.ack
	if ln == nil {
		return nil, nil
	}
	inherited.ln, inherited.ack = nil, nil
	if !sameAddr(ln.Addr(), addr) {
		log.Printf("gateway: the socket handed over listens on %s, not %s; listening anew", ln.Addr(), addr)
		ln.Close()
		return nil, nil
	}
	return ln, ack
}

// pendingAck is the ack of the socket handed over that Listen returned,
// for ListenAndServe to call once it serves on it.
var pendingAck struct {
	sync.Mutex
	ack func()
}

func setAck(ack func()) {
	pendingAck.Lock()
	pendingAck.ack = ack
	pendingAck.Unlock()
}

// takeAck is the ack waiting, nil when the listener wasn't handed over.
func takeAck() func() {
	pendingAck.Lock()
	defer pendingAck.Unlock()
	ack := pendingAck.ack
	pendingAck.ack = nil
	return ack
}

// sameAddr is whether a listens where addr names: the same port, on the
// same IP or both on every interface.
func sameAddr(a net.Addr, addr string) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port != strconv.Itoa(ta.Port) {
		return false
	}
	if host == "" {
		return ta.IP == nil || ta.IP.IsUnspecified()
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		// a name (localhost): any of its addresses
		var err error
		if ips, err = net.LookupIP(host); err != nil {
			return false
		}
	}
	for _, ip := range ips {
		if ip.IsUnspecified() {
			if ta.IP == nil || ta.IP.IsUnspecified() {
				return true
			}
		} else if ip.Equal(ta.IP) {
			return true
		}
	}
	return false
}

// ListenerFile is a copy of the socket the gateway listens on, to hand to
// the magpie that takes it over; the caller closes it once sent.
func (s *Server) ListenerFile() (*os.File, error) {
	s.lnMu.Lock()
	ln := s.ln
	s.lnMu.Unlock()
	if t, ok := ln.(*tlsListener); ok {
		ln = t.Listener
	}
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, errors.New("the gateway isn't listening on a TCP socket")
	}
	return tl.File()
}

// HandOff has the gateway stop accepting at once: the magpie it was
// handed to takes every connection from now on (one this one took as its
// end began was closed unanswered, #3568). Once its ctx ends, it waits
// for every request in flight to finish (handedOffDrain at most) rather
// than 2s.
func (s *Server) HandOff() {
	s.handedOff.Store(true)
	s.stopAccepting()
}

// stopAccepting has the gateway take no connection from here on, for the
// magpie serving the port after it: the copy of the socket handed to that
// one keeps the socket open, so a connection waits there rather than
// being refused. (`make dev`'s takes them on a socket of its own.)
func (s *Server) stopAccepting() {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	s.notAccepting.Store(true)
	if s.ln != nil {
		s.ln.Close()
	}
}

// closesWhenHandedOff has each reply the gateway gives once it takes no
// connection (stopAccepting) close its connection, so the agent's next
// request dials the magpie serving the port after this one rather than
// waiting on a connection this one's end closes (unreadConns).
func (s *Server) closesWhenHandedOff(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.notAccepting.Load() {
			w.Header().Set("Connection", "close")
		}
		h.ServeHTTP(w, r)
	})
}

// handingOver is whether another magpie serves the port once this
// gateway ends: it was handed off, or `make dev` started one beside it.
func (s *Server) handingOver() bool { return Handover || s.handedOff.Load() }

// drainFor is how long the gateway's end waits for the requests in flight.
func (s *Server) drainFor() time.Duration {
	if s.handingOver() {
		return handedOffDrain
	}
	if g := time.Duration(s.grace.Load()); g > 0 {
		return g // Drain's
	}
	return shortDrain
}

// Drain readies the gateway's end for a stop that should cut nothing: a
// SIGTERM to `magpie serve`, which is how Kubernetes, Docker and a service
// manager end it (a rolling update's old pod). GET / answers 503 from now
// on, so a readiness probe (`magpie healthcheck`) takes this magpie out of
// its Service, and once ctx ends the requests in flight, a stream an agent
// is reading included, are given grace to finish rather than shortDrain's
// 2s. The listener closes as ctx ends, as before: new connections go to
// the next magpie, or are refused.
func (s *Server) Drain(grace time.Duration) {
	s.grace.Store(int64(grace))
	s.draining.Store(true)
}

// Draining: Drain was called.
func (s *Server) Draining() bool { return s.draining.Load() }

// Grace is the grace Drain gave, shortDrain before it was called.
func (s *Server) Grace() time.Duration {
	if g := time.Duration(s.grace.Load()); g > 0 {
		return g
	}
	return shortDrain
}

// unreadWait is how long a handover's end waits for a connection taken to
// send its first request: as for net/http's Shutdown, one that sent
// nothing for 5s is idle.
var unreadWait = 5 * time.Second

// unreadConns are the connections the gateway took whose next request it
// hasn't read yet: new ones, and kept-alive ones idle between requests or
// still answering one (net/http notes one idle after its reply is out, so
// an agent can have read the reply and sent its next request first).
// Shutdown closes such a connection unanswered, and a request the agent
// sent on it just then is cut ("connection reset", #3451), so a
// handover's end waits for them first: each one's next request is
// answered here, its reply closing it (closesWhenHandedOff), and the
// agent's next one dials the magpie after this one.
type unreadConns struct {
	mu    sync.Mutex
	conns map[net.Conn]bool
	kept  map[net.Conn]bool // answering a request, or idle after one
	// stopped: the server takes none from here on, its Serve returned
	stopped bool
}

// note is the server's ConnState: a connection is unread from StateNew
// to its next state, and kept from StateActive until it is closed or
// hijacked.
func (u *unreadConns) note(c net.Conn, state http.ConnState) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.conns, c)
	delete(u.kept, c)
	switch state {
	case http.StateNew:
		if u.conns == nil {
			u.conns = map[net.Conn]bool{}
		}
		u.conns[c] = true
	case http.StateActive, http.StateIdle:
		if u.kept == nil {
			u.kept = map[net.Conn]bool{}
		}
		u.kept[c] = true
	}
}

// stop is called once the server's Serve has returned. net/http notes a
// connection (StateNew) on the goroutine that took it, before Serve can
// return, so every one it took is noted by then: one noted after Shutdown
// began would have its request closed unanswered too.
func (u *unreadConns) stop() {
	u.mu.Lock()
	u.stopped = true
	u.mu.Unlock()
}

// wait returns once the server takes no more connections and none it took
// is unread, after unreadWait at most, or when ctx ends. A kept-alive
// connection its agent leaves idle that long is closed by Shutdown, as
// its idle timeout would.
func (u *unreadConns) wait(ctx context.Context) {
	for deadline := time.Now().Add(unreadWait); ; time.Sleep(10 * time.Millisecond) {
		u.mu.Lock()
		done := u.stopped && len(u.conns) == 0 && len(u.kept) == 0
		u.mu.Unlock()
		if done || time.Now().After(deadline) || ctx.Err() != nil {
			return
		}
	}
}

// ServeCallbacks answers, on ln, the tool calls of the Claude Code runs
// this process started (claudeMCPPath): their MCP helper calls the
// gateway's port, which the magpie this one handed it to now serves, and
// that one passes on the calls of runs it doesn't have (ForwardCallbacks).
// It also lets the successor wait until Claude sessions have been saved.
// It returns once ln is closed.
func (s *Server) ServeCallbacks(ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_magpie/claude-ready", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-s.sessionsSaved:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("POST "+claudeMCPPath+"{token}", s.subscription.mcpCall)
	err := (&http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}).Serve(ln)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// predecessorCallbacks is the unix socket of the magpie this one took the
// gateway over from, which answers its own runs' tool calls there; ""
// when there is none.
var predecessorCallbacks struct {
	sync.Mutex
	sock  string
	ready bool
}

// ForwardCallbacks has a tool call for a Claude Code run this process
// doesn't know passed on to sock, where the magpie it took the gateway
// over from answers those of its own runs (ServeCallbacks): a turn under
// way there when the gateway changed hands still gets its tool results.
// "" stops it.
func ForwardCallbacks(sock string) {
	predecessorCallbacks.Lock()
	predecessorCallbacks.ready = false
	predecessorCallbacks.sock = sock
	predecessorCallbacks.Unlock()
}

// forwardCallback passes the tool call r, for a run this process doesn't
// have, on to the magpie before this one; false when there is none to.
func forwardCallback(w http.ResponseWriter, r *http.Request) bool {
	predecessorCallbacks.Lock()
	sock := predecessorCallbacks.sock
	predecessorCallbacks.Unlock()
	if sock == "" {
		return false
	}
	if _, err := os.Stat(sock); err != nil {
		// that magpie has finished and gone
		ForwardCallbacks("")
		return false
	}
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: "predecessor"})
			pr.Out.URL.Path, pr.Out.URL.RawPath = r.URL.Path, r.URL.RawPath
		},
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if !errors.Is(err, io.EOF) {
				log.Printf("claude: a tool call for the magpie before this one didn't reach it: %v", err)
			}
			http.Error(w, "unknown or expired Claude run", http.StatusNotFound)
		},
	}
	p.ServeHTTP(w, r)
	return true
}

// forwarding is whether tool calls this process doesn't know go on to the
// magpie it took the gateway over from.
func forwarding() bool {
	predecessorCallbacks.Lock()
	sock := predecessorCallbacks.sock
	predecessorCallbacks.Unlock()
	return sock != ""
}

// Addr is where the gateway listens, "" before it does.
func (s *Server) Addr() string {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// A successor accepts requests before its predecessor finishes draining.
// Only Claude turns wait: resuming before the shelf is written replays history.
// The predecessor saves its shelf once every request it holds has ended, of
// any provider, up to handedOffDrain; a Claude turn waits for that at most
// predecessorWait, then goes on as it did before the wait (a replay, for a
// conversation the shelf would have resumed), and so does every turn after
// it: errPredecessorSlow. A shelf written later is still read, at the next
// turn no session here is found for (loadShelf).
func awaitPredecessorSessions(ctx context.Context) error {
	predecessorCallbacks.Lock()
	sock, ready := predecessorCallbacks.sock, predecessorCallbacks.ready
	predecessorCallbacks.Unlock()
	if sock == "" || ready {
		return nil
	}
	asker := ctx
	ctx, cancel := context.WithTimeout(ctx, predecessorWait)
	defer cancel()
	markReady := func() {
		predecessorCallbacks.Lock()
		if predecessorCallbacks.sock == sock {
			predecessorCallbacks.ready = true
		}
		predecessorCallbacks.Unlock()
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://predecessor/_magpie/claude-ready", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		if asker.Err() == nil && ctx.Err() != nil {
			markReady()
			return errPredecessorSlow
		}
		// The callback listener goes away only after the old gateway drains.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return err
		}
	} else {
		resp.Body.Close()
		// Older versions have no readiness endpoint. Preserve their handover.
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("predecessor returned %s", resp.Status)
		}
	}
	markReady()
	return nil
}

// predecessorWait is how long a Claude turn waits for the predecessor's
// saved sessions; a var so tests can shorten it. Well inside Claude Code's
// own ten minutes for a reply.
var predecessorWait = 90 * time.Second

// errPredecessorSlow: the predecessor had not saved its sessions within
// predecessorWait, and no turn waits for it any more.
var errPredecessorSlow = errors.New("the previous gateway is still draining")
