package netproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// PingAgain is how long a connection still silent after its first health
// ping waits for the next (CalmHTTP2): gRPC's servers, Google's among
// them, count a ping that comes sooner after the last, with nothing sent
// between, as a strike, and end the connection at the third.
var PingAgain = 5 * time.Minute

// CalmHTTP2 has t, a transport to the vendors, speak HTTP/2 with a health
// check of magpie's own instead of Go's (HTTP2Health). Go's pings a
// connection each time it has read no frame for PingAfter, and the ping's
// own answer is a frame, so a vendor silent for minutes before a turn's
// first byte is pinged every 10s for all of them. Google's front end
// counts the pings it gets while it has sent nothing and ends the
// connection: GOAWAY ENHANCE_YOUR_CALM "too_many_pings" after about four
// minutes of gemini thinking through Antigravity, and the turn was lost
// and sent again (zdevoz on Discord). gRPC's servers, Google's among them,
// take a ping as abuse when it comes within 5 minutes of the last one
// with no HEADERS or DATA sent between (keepalive.md:
// GRPC_ARG_HTTP2_MIN_RECV_PING_INTERVAL_WITHOUT_DATA_MS).
//
// Here the first ping of a silence still goes out PingAfter into it, so a
// connection whose path died before or as a request went out is found as
// soon as before (#2843, #4936), and dropped when no answer comes within
// PingWait. A silence starts at the vendor's last HEADERS or DATA, or at a
// request handed to the connection. A ping answered shows the path alive,
// and the next waits PingAgain: a 4-minute wait for a first byte has one
// ping, not twenty-four. A path that dies later in a long wait is found at
// the next ping, or by the request's own wait for its answer.
//
// t's settings are read now: CalmHTTP2 is called once t is set up, and a
// clone of t is calmed again with its own connections (Dispatch).
func CalmHTTP2(t *http.Transport) error {
	if _, ok := calmed.Load(t); ok {
		return nil
	}
	p := &calmPool{conns: map[string][]*calmConn{}}
	// set before anything clones t: a clone (or a first request) has
	// net/http settle t's HTTP/2 once, and it takes the "https" protocol's
	// one field as the HTTP/2 transport whose idle connections it closes
	// with its own (CloseIdleConnections)
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	} else {
		t.TLSClientConfig = t.TLSClientConfig.Clone()
	}
	t.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{
		"h2": func(authority string, c *tls.Conn) http.RoundTripper {
			if err := p.add(authority, c); err != nil {
				go c.Close()
				return erringRoundTripper{err}
			}
			return p.t2
		},
	}
	t.RegisterProtocol("https", calmAlt{Pool: p})

	// x/net's HTTP/2 reads its settings (ResponseHeaderTimeout,
	// IdleConnTimeout, DisableCompression…) from the transport it was
	// configured on; this copy is that and never dials
	conf := t.Clone()
	conf.TLSNextProto = nil
	if t.HTTP2 != nil {
		h := *t.HTTP2
		h.SendPingTimeout, h.PingTimeout = 0, 0
		conf.HTTP2 = &h
	}
	t2, err := http2.ConfigureTransports(conf)
	if err != nil {
		return err
	}
	t2.ReadIdleTimeout, t2.PingTimeout = 0, 0
	t2.ConnPool = p
	p.t2 = t2
	calmed.Store(t, p)
	return nil
}

// calmed is every transport CalmHTTP2 set up, so a clone is set up anew.
var calmed sync.Map // *http.Transport -> *calmPool

func isCalm(t *http.Transport) bool {
	_, ok := calmed.Load(t)
	return ok
}

// calmAlt is a calmed transport's https protocol: a request goes on a
// kept HTTP/2 connection, or back to net/http to dial one.
type calmAlt struct{ Pool *calmPool }

func (a calmAlt) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := a.Pool.t2.RoundTrip(req)
	if noCachedConn(err) {
		return nil, http.ErrSkipAltProtocol
	}
	return res, err
}

func noCachedConn(err error) bool {
	var n interface{ IsHTTP2NoCachedConnError() }
	return err != nil && errors.As(err, &n)
}

type erringRoundTripper struct{ err error }

func (e erringRoundTripper) RoundTripErr() error                             { return e.err }
func (e erringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

// calmPool keeps a calmed transport's HTTP/2 connections, by host:port.
// It never dials: net/http does, through the transport's proxy, and hands
// the connection over (TLSNextProto).
type calmPool struct {
	t2    *http2.Transport
	mu    sync.Mutex
	conns map[string][]*calmConn
}

type calmConn struct {
	cc *http2.ClientConn
	w  *watchedConn
}

func (p *calmPool) add(authority string, c *tls.Conn) error {
	// net/http hands a connection over for a request
	now := time.Now()
	w := &watchedConn{Conn: c, read: now, ask: now}
	cc, err := p.t2.NewClientConn(w)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.conns[authority] = append(p.conns[authority], &calmConn{cc: cc, w: w})
	p.mu.Unlock()
	go watch(cc, w, PingAfter, PingWait, PingAgain)
	return nil
}

func (p *calmPool) GetClientConn(req *http.Request, addr string) (*http2.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.conns[addr][:0]
	var got *calmConn
	for _, c := range p.conns[addr] {
		if c.cc.State().Closed {
			continue
		}
		kept = append(kept, c)
		if got == nil && c.cc.ReserveNewRequest() {
			got = c
		}
	}
	p.conns[addr] = kept
	if got == nil {
		return nil, http2.ErrNoCachedConn
	}
	got.w.asked()
	return got.cc, nil
}

func (p *calmPool) MarkDead(cc *http2.ClientConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for addr, cs := range p.conns {
		for i, c := range cs {
			if c.cc == cc {
				p.conns[addr] = append(cs[:i:i], cs[i+1:]...)
				return
			}
		}
	}
}

// CloseIdleConnections shuts every connection no request is on; one a
// request takes meanwhile is let finish it.
func (p *calmPool) CloseIdleConnections() {
	p.mu.Lock()
	var idle []*http2.ClientConn
	for addr, cs := range p.conns {
		kept := cs[:0]
		for _, c := range cs {
			s := c.cc.State()
			if s.StreamsActive == 0 && s.StreamsReserved == 0 && s.StreamsPending == 0 {
				idle = append(idle, c.cc)
				continue
			}
			kept = append(kept, c)
		}
		p.conns[addr] = kept
	}
	p.mu.Unlock()
	for _, cc := range idle {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if cc.Shutdown(ctx) != nil {
				cc.Close()
			}
		}()
	}
}

// errLostPing is a connection's end when its health ping went unanswered,
// in Go's own words for it (closeForLostPing), which lostPing reads.
var errLostPing = errors.New("http2: client connection lost")

// watch is a calmed connection's health check (CalmHTTP2), until the
// connection closes.
func watch(cc *http2.ClientConn, w *watchedConn, after, wait, again time.Duration) {
	tick := time.NewTicker(max(after/10, time.Millisecond))
	defer tick.Stop()
	var pinged time.Time
	for range tick.C {
		if cc.State().Closed || w.isLost() {
			return
		}
		read, fresh := w.times()
		now := time.Now()
		var due time.Time
		if pinged.Before(fresh) {
			// the first ping of a silence
			due = later(read, fresh).Add(after)
		} else {
			due = pinged.Add(again)
		}
		if now.Before(due) {
			continue
		}
		pinged = now
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		err := cc.Ping(ctx)
		cancel()
		if err != nil {
			if !cc.State().Closed {
				w.lose()
			}
			return
		}
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// watchedConn is a calmed HTTP/2 connection's TLS connection, which notes
// when a frame came, and when the vendor last sent HEADERS or DATA.
type watchedConn struct {
	*tls.Conn
	mu    sync.Mutex
	read  time.Time // the last bytes read
	spoke time.Time // the last HEADERS, CONTINUATION or DATA frame read
	ask   time.Time // the last request handed the connection
	scan  frameScan
	lost  bool
}

func (w *watchedConn) Read(b []byte) (int, error) {
	n, err := w.Conn.Read(b)
	w.mu.Lock()
	defer w.mu.Unlock()
	if n > 0 {
		now := time.Now()
		w.read = now
		if w.scan.spoke(b[:n]) {
			w.spoke = now
		}
	}
	if err != nil && w.lost {
		err = errLostPing
	}
	return n, err
}

func (w *watchedConn) asked() {
	w.mu.Lock()
	w.ask = time.Now()
	w.mu.Unlock()
}

// times is when bytes last came, and when the silence began: the
// vendor's last HEADERS or DATA, or the last request, whichever is later.
func (w *watchedConn) times() (read, fresh time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.read, later(w.spoke, w.ask)
}

func (w *watchedConn) lose() {
	w.mu.Lock()
	w.lost = true
	w.mu.Unlock()
	w.Conn.NetConn().Close()
}

func (w *watchedConn) isLost() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lost
}

// frameScan follows HTTP/2 frames through the bytes read, by their
// 9-byte headers (RFC 9113 4.1).
type frameScan struct {
	head [9]byte
	have int // header bytes held
	left int // payload bytes still to come
}

// spoke says a HEADERS (1), CONTINUATION (9) or DATA (0) frame began in b.
func (s *frameScan) spoke(b []byte) bool {
	saw := false
	for len(b) > 0 {
		if s.left > 0 {
			n := min(s.left, len(b))
			s.left -= n
			b = b[n:]
			continue
		}
		n := copy(s.head[s.have:], b)
		s.have += n
		b = b[n:]
		if s.have < len(s.head) {
			break
		}
		s.have = 0
		s.left = int(s.head[0])<<16 | int(s.head[1])<<8 | int(s.head[2])
		switch s.head[3] {
		case 0, 1, 9:
			saw = true
		}
	}
	return saw
}

var _ net.Conn = (*watchedConn)(nil)
