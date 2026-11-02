package gateway

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// upstreamTLS is what the gateway's transports dial TLS with: nil, the
// system's roots, but for a test trusting its own server.
var upstreamTLS *tls.Config

// upstreamTransport is the gateway's transport to the vendors: a provider
// with a proxy of its own is sent through a transport kept for that proxy
// (#237), and the ChatGPT backend over HTTP/1.1 (netproxy.HTTP1), a
// streamed turn to it over its WebSocket (codexSocket). Every
// other vendor is spoken HTTP/2 to wherever it offers it. A large body
// whose connection was dropped for a lost ping goes again through a
// transport that does not ping (#3909), and so do the large bodies after
// it for a while (slowFor). A request that
// finds its kept-alive connection dropped is sent again on a new one
// (replayable). A vendor is given upstreamHeaderWait to begin answering,
// but for a drawing, which an images API answers only once it has drawn:
// it goes through a transport of its own that waits as long as Settings'
// Image generation timeout gives it (drawWithin), up to 60 minutes.
func upstreamTransport() http.RoundTripper {
	return drawsApart{
		any:     codexSocket{replayable{netproxy.Dispatch(vendorTransport(upstreamHeaderWait))}},
		draws:   replayable{netproxy.Dispatch(vendorTransport(0))},
		uploads: codexSocket{replayable{netproxy.Dispatch(uploadTransport(upstreamHeaderWait))}},
		slow:    new(atomic.Int64),
	}
}

// upstreamHeaderWait is how long a vendor is given to begin answering a
// request that isn't a drawing.
var upstreamHeaderWait = 10 * time.Minute

// uploadBytes is the body size from which a request goes to a vendor over
// a connection the read-idle ping does not watch: on an uplink under about
// 3KB/s a body this big is still being delivered when the ping is due
// (netproxy.PingAfter), and the ping's answer can only come back after the
// last byte, behind the body it was queued behind (netproxy.PingWait after
// it). #3909 sent a 175k-token turn, ~680KB, on a 2-10KB/s uplink.
var uploadBytes int64 = 64 << 10

// slowFor is how long, after a large body's connection was dropped for
// a lost ping, the large bodies after it skip the ping and its lost try.
var slowFor = 30 * time.Minute

// uploadStall is how long a request's write may put no bytes on its
// connection before that connection is given up on: what the read-idle
// ping cannot tell apart from a body still being delivered.
var uploadStall = time.Minute

// vendorTransport is a transport to the vendors that gives one wait to
// begin answering, none for 0: the request's context then bounds it.
func vendorTransport(wait time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 netproxy.Func,
		ResponseHeaderTimeout: wait,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       upstreamTLS,
		// a silent HTTP/2 connection is pinged and dropped (#2843)
		HTTP2: netproxy.HTTP2Health(),
	}
}

// uploadTransport is vendorTransport for a request whose body may still be
// under way when the connection has had no frame for a while: an HTTP/2
// ping is written behind that body's own DATA, so its answer comes only
// after the last byte a slow uplink is still delivering, and the health
// check closed a connection that was working (#3909). No ping here; a
// write that moves no bytes ends the connection (WriteByteTimeout), and a
// vendor that never begins answering is ended by the response header wait.
func uploadTransport(wait time.Duration) *http.Transport {
	t := vendorTransport(wait)
	t.HTTP2 = &http.HTTP2Config{WriteByteTimeout: uploadStall}
	return t
}

// uploading says r's body can outlast the vendor's silence: one of a
// length nobody stated (written as it is read), or one over uploadBytes.
func uploading(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody &&
		(r.ContentLength < 0 || r.ContentLength >= uploadBytes)
}

// drawsApart sends a drawing's request (drawWaitOf) through draws, every
// other through any, but for a large body (uploading) on an uplink found
// slow: through uploads.
//
// The ping stays on a large body by default, as a silent connection
// (#2843) takes a body that fits its send buffer and answers nothing for
// the 10-minute header wait. When the ping is lost under a large body, the
// body may still have been on its way (#3909), so it is sent once more
// through uploads, on a new connection, and the large bodies until
// slowFor has passed go there first. A connection that really was dead
// costs nothing more: the new one is dialled either way.
type drawsApart struct {
	any, draws, uploads http.RoundTripper
	// slow is when the uplink was last found slow, in Unix nanoseconds.
	slow *atomic.Int64
}

func (t drawsApart) RoundTrip(r *http.Request) (*http.Response, error) {
	if _, ok := drawWaitOf(r.Context()); ok {
		return t.draws.RoundTrip(r)
	}
	if !uploading(r) || t.slow == nil {
		return t.any.RoundTrip(r)
	}
	if time.Since(time.Unix(0, t.slow.Load())) < slowFor {
		return t.uploads.RoundTrip(r)
	}
	res, err := t.any.RoundTrip(r)
	if err == nil || !lostPing(err) || r.GetBody == nil || r.Context().Err() != nil {
		return res, err
	}
	body, gerr := r.GetBody()
	if gerr != nil {
		return nil, err
	}
	t.slow.Store(time.Now().UnixNano())
	again := r.Clone(r.Context())
	again.Body = body
	return t.uploads.RoundTrip(again)
}

// lostPing says err is Go's HTTP/2 transport closing a connection whose
// health ping went unanswered (closeForLostPing).
func lostPing(err error) bool {
	return strings.Contains(err.Error(), "http2: client connection lost")
}

// replayable lets Go's transport send a request again, at once and on a new
// connection, when the kept-alive connection it went out on turns out to
// be dropped: written into, and closed before a byte of a reply ("EOF",
// "write: broken pipe"). Go does so only for a request it may replay — a
// GET, or one marked idempotent — and only on a reused connection, so a
// POST to a vendor failed as a 502 and was asked again after a pause, each
// pause on a connection a proxy or the vendor's front had dropped while
// idle (#2136: Pi's Codex turn through Clash, "write: broken pipe" then
// "EOF", 14.5s before the third try). The mark is an X-Idempotency-Key with
// no value, which Go reads and never sends: the vendor sees the request as
// before. magpie already asked the same request again after these errors,
// so nothing is sent that wasn't before, only sooner.
type replayable struct{ http.RoundTripper }

// RoundTrip sends req, marked so. A new connection closed by the far side
// as soon as it opened — right after the TLS handshake, while the request
// was being written — Go gives up on at once, as it isn't a kept-alive
// one: "write tcp …: use of closed network connection". The vendor never
// had the whole request, so it is sent again on another new connection,
// after a moment, up to unsentAgain times (dealyang_ on X: a Codex turn through a TUN proxy's
// fake IP, 198.18.0.17, failed with it on every reconnect). Still closed,
// the error says why (errClosedUnsent), and the routing rests nobody for
// it (failProxy).
func (t replayable) RoundTrip(req *http.Request) (*http.Response, error) {
	_, marked := req.Header["Idempotency-Key"]
	if _, ok := req.Header["X-Idempotency-Key"]; ok {
		marked = true
	}
	r := req
	if !marked && req.Method != http.MethodGet && req.Method != http.MethodHead &&
		(req.Body == nil || req.Body == http.NoBody || req.GetBody != nil) {
		// a copy, as a RoundTripper leaves its caller's request as it was
		r = req.Clone(req.Context())
		r.Header["X-Idempotency-Key"] = nil
	}
	for n := 0; ; n++ {
		var fresh, h2 atomic.Bool
		trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
			fresh.Store(!i.Reused)
			if tc, ok := i.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
				h2.Store(tc.ConnectionState().NegotiatedProtocol == "h2")
			}
		}}
		try := r.WithContext(httptrace.WithClientTrace(r.Context(), trace))
		res, err := t.RoundTripper.RoundTrip(try)
		if err == nil || !fresh.Load() || h2.Load() || !closedBeforeSent(err) || req.Context().Err() != nil {
			return res, err
		}
		if n >= len(unsentAgain) || (r.Body != nil && r.Body != http.NoBody && r.GetBody == nil) {
			return nil, errClosedUnsent{err: err, tries: n + 1}
		}
		select {
		case <-req.Context().Done():
			return nil, err
		case <-time.After(unsentAgain[n]):
		}
		again := r.Clone(r.Context())
		if r.GetBody != nil {
			body, gerr := r.GetBody()
			if gerr != nil {
				return nil, err
			}
			again.Body = body
		}
		r = again
	}
}

// unsentAgain is the pause before each new try of a request whose new
// connection was closed before the whole of it was sent.
var unsentAgain = []time.Duration{250 * time.Millisecond, time.Second}

// closedBeforeSent says err is Go's HTTP/1.1 transport failing to write
// the request: the far side had closed the connection, so the write found
// it closed by the reader ("use of closed network connection") or broken
// ("broken pipe", "connection reset by peer"). The request never went out
// whole, so the vendor had no request to act on; a reply the vendor sent
// before closing is a reply, and a connection ending after the whole
// request went out is a read error, never sent again here. (Go's
// "nothing written" doesn't hold over TLS: a server had read 1186 bytes
// of a request Go called unwritten.) A TLS connection the reader closed
// first fails the write with a bare net.ErrClosed; the reader's own end is
// "EOF" or a read error. A close the reader saw before the write began is
// Go's errServerClosedIdle (seen on Windows).
func closedBeforeSent(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "write" || errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "http: server closed idle connection")
}

// errClosedUnsent is a request's error once every new connection it was
// tried on was closed before the whole of it was sent: what went wrong and
// where to look, read by failure as nobody's fault (closedUnsentWords).
type errClosedUnsent struct {
	err   error
	tries int
}

func (e errClosedUnsent) Error() string {
	return fmt.Sprintf("%v (%s, %d times: the connection was closed by the network or a proxy before the whole request was sent, so the vendor had nothing to act on; check the proxy or VPN this computer goes through)", e.err, closedUnsentWords, e.tries)
}

func (e errClosedUnsent) Unwrap() error { return e.err }

// closedUnsentWords mark errClosedUnsent in a failure's message.
const closedUnsentWords = "closed before sent"
