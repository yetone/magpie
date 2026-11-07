package gateway

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// closingUpstream is a TLS vendor whose first closeFirst new connections are
// ended by the far side as soon as the handshake is done, before the
// request is read (a TUN proxy's fake IP doing so, dealyang_ on X), or
// once the whole request was read: reset (afterSentReset) or ended
// (afterSentEnd). The rest are answered 200 with the body's length.
type closingUpstream struct {
	addr     string
	accepts  atomic.Int32
	answered atomic.Int32
}

type closeWhen int

const (
	atHandshake closeWhen = iota
	afterSentReset
	afterSentEnd
)

func newClosingUpstream(t *testing.T, closeFirst int32, when closeWhen) *closingUpstream {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	cert := srv.TLS.Certificates
	srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &closingUpstream{addr: ln.Addr().String()}
	served := make(chan net.Conn)
	done := make(chan struct{})
	conf := &tls.Config{Certificates: cert, NextProtos: []string{"http/1.1"}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := u.accepts.Add(1)
			go func() {
				tc := tls.Server(c, conf)
				if tc.Handshake() != nil {
					c.Close()
					return
				}
				if n <= closeFirst {
					if when != atHandshake {
						// the whole request, then reset or ended
						if r, err := http.ReadRequest(bufio.NewReader(tc)); err == nil {
							io.Copy(io.Discard, r.Body)
						}
						if when == afterSentEnd {
							tc.Close()
							return
						}
						c.(*net.TCPConn).SetLinger(0)
						c.Close()
						return
					}
					// closed, as a proxy whose own connection
					// onward failed closes it: the reader sees
					// the end while the request is being written
					tc.Close()
					return
				}
				select {
				case served <- tc:
				case <-done:
					c.Close()
				}
			}()
		}
	}()
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		u.answered.Add(1)
		io.WriteString(w, http.StatusText(200)+" "+strconv.FormatInt(n, 10))
	})}
	go hs.Serve(&chanListener{c: served, done: done, addr: ln.Addr()})
	t.Cleanup(func() {
		ln.Close()
		close(done)
		hs.Close()
	})
	return u
}

type chanListener struct {
	c    chan net.Conn
	done chan struct{}
	addr net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.c:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

// closedUnsentClient is replayable over an HTTP/1.1-only vendor transport
// trusting any certificate, with short pauses between tries.
func closedUnsentClient(t *testing.T) *http.Client {
	t.Helper()
	was := unsentAgain
	unsentAgain = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { unsentAgain = was })
	tr := plainVendorTransport(time.Minute)
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	var h1 http.Protocols
	h1.SetHTTP1(true)
	tr.Protocols, tr.ForceAttemptHTTP2 = &h1, false
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: replayable{tr}}
}

// closedUnsentPost is a Codex-sized turn: a large body, as with an image
// in it, so the write is still under way when the close arrives.
func closedUnsentPost(t *testing.T, cl *http.Client, addr string) (*http.Response, error) {
	t.Helper()
	body := bytes.Repeat([]byte("x"), 8<<20)
	req, err := http.NewRequest(http.MethodPost, "https://"+addr+"/backend-api/codex/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 1500))
	return cl.Do(req)
}

// A new connection the network closes right after the TLS handshake,
// before a byte of the request went out, is not the vendor's answer: the
// request goes again on another new connection and is answered, whole.
func TestClosedBeforeSentIsSentAgain(t *testing.T) {
	u := newClosingUpstream(t, 2, atHandshake)
	res, err := closedUnsentPost(t, closedUnsentClient(t), u.addr)
	if err != nil {
		t.Fatalf("a request whose first two new connections were closed before it was sent failed: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != "OK "+strconv.Itoa(8<<20) {
		t.Errorf("answered %d %q, want 200 with the whole body", res.StatusCode, b)
	}
	if n := u.accepts.Load(); n != 3 {
		t.Errorf("%d connections, want 3: two closed, one answered", n)
	}
}

// goEnded is Go's HTTP/1.1 transport as replayable sees it when a new
// connection ends under a request: GotConn for a new connection, then,
// if wrote, WroteRequest without an error, then err. The first ends
// calls end so, the rest are answered 200.
type goEnded struct {
	ends  int32
	wrote bool
	err   error
	tries atomic.Int32
}

func (g *goEnded) RoundTrip(r *http.Request) (*http.Response, error) {
	trace := httptrace.ContextClientTrace(r.Context())
	if trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{})
	}
	if g.tries.Add(1) > g.ends {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	}
	if g.wrote && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	return nil, g.err
}

// On a busy machine the reader can see a proxy's close right after the
// handshake before Go's write goroutine has run: Go then drops the
// request unwritten (no WroteRequest) and says only "EOF", not a write
// error (#5519, red on CI's race run). Nothing went out, so the request
// goes again; so too for a reset seen first.
func TestEndedBeforeWriteBeganIsSentAgain(t *testing.T) {
	closedUnsentClient(t) // the short pauses
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	for _, end := range []error{io.EOF, reset} {
		g := &goEnded{ends: 2, err: end}
		res, err := closedUnsentPost(t, &http.Client{Transport: replayable{g}}, "127.0.0.1:1")
		if err != nil {
			t.Fatalf("a request whose first two new connections ended (%v) before its write began failed: %v", end, err)
		}
		res.Body.Close()
		if n := g.tries.Load(); n != 3 {
			t.Errorf("%v: %d tries, want 3: two ended, one answered", end, n)
		}
	}
}

// Once Go has written the whole request, the same end may follow a
// vendor that read it and acted on it: not sent again.
func TestEndedAfterWriteIsNotSentAgain(t *testing.T) {
	closedUnsentClient(t)
	g := &goEnded{ends: 2, wrote: true, err: io.EOF}
	_, err := closedUnsentPost(t, &http.Client{Transport: replayable{g}}, "127.0.0.1:1")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want the EOF as it was", err)
	}
	var cu errClosedUnsent
	if errors.As(err, &cu) {
		t.Errorf("err = %v: a request written whole was called unsent", err)
	}
	if n := g.tries.Load(); n != 1 {
		t.Errorf("%d tries, want 1", n)
	}
}

// Closed on every try, the request fails with why, in the words the
// routing reads to rest nobody for it.
func TestClosedBeforeSentEveryTimeSaysWhy(t *testing.T) {
	u := newClosingUpstream(t, 100, atHandshake)
	_, err := closedUnsentPost(t, closedUnsentClient(t), u.addr)
	var cu errClosedUnsent
	if !errors.As(err, &cu) {
		t.Fatalf("err = %v, want errClosedUnsent", err)
	}
	if cu.tries != 3 || !strings.Contains(err.Error(), closedUnsentWords) || !strings.Contains(err.Error(), "proxy or VPN") {
		t.Errorf("err = %v (tries %d), want 3 tries and why", err, cu.tries)
	}
	if n := u.accepts.Load(); n != 3 {
		t.Errorf("%d connections, want 3", n)
	}
	if got := failure(http.StatusBadGateway, []byte(`{"error":{"message":"Codex: `+err.Error()+`"}}`)); got != failProxy {
		t.Errorf("failure() = %q for %v, want %q: nothing reached the vendor, so nobody rests", got, err, failProxy)
	}
}

// A connection closed after the vendor read the whole request may have
// had it acted on: that is not sent again here.
// Reset or ended ("EOF"), the same.
func TestClosedAfterSentIsNotSentAgain(t *testing.T) {
	for _, when := range []closeWhen{afterSentReset, afterSentEnd} {
		u := newClosingUpstream(t, 100, when)
		_, err := closedUnsentPost(t, closedUnsentClient(t), u.addr)
		if err == nil {
			t.Fatalf("close %d: no error from a connection closed under the request", when)
		}
		var cu errClosedUnsent
		if errors.As(err, &cu) {
			t.Errorf("close %d: err = %v: a request whose bytes went out was called unsent", when, err)
		}
		if n := u.accepts.Load(); n != 1 {
			t.Errorf("close %d: %d connections for a request the vendor had read whole, want 1 (%v)", when, n, err)
		}
	}
}

// Only the words errClosedUnsent writes make a 502 nobody's fault; Go's
// own "use of closed network connection" without them, or a connection
// reset mid-reply, is read as before.
func TestClosedUnsentFailureScoped(t *testing.T) {
	sent := `Post "https://chatgpt.com/backend-api/codex/responses": write tcp 198.18.0.1:56329->198.18.0.17:443: use of closed network connection`
	if got := failure(http.StatusBadGateway, []byte(sent+" ("+closedUnsentWords+", 3 times)")); got != failProxy {
		t.Errorf("failure() = %q for errClosedUnsent's words, want %q", got, failProxy)
	}
	for _, body := range []string{sent, "read tcp 1.2.3.4:5->6.7.8.9:443: read: connection reset by peer"} {
		if got := failure(http.StatusBadGateway, []byte(body)); got == failProxy {
			t.Errorf("failure(502, %q) = %q: read as nobody's fault", body, got)
		}
	}
	if got := failure(http.StatusInternalServerError, []byte(closedUnsentWords)); got == failProxy {
		t.Errorf("a vendor's own 500 naming the words was read as nobody's fault")
	}
}
