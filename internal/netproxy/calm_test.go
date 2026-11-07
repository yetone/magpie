package netproxy

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hushPipe passes bytes between a client and to until hushed: then it
// goes on taking what either side writes and passes none of it, as a
// tunnel whose far side died does.
type hushPipe struct {
	ln     net.Listener
	hushed atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
	dials  int
}

func newHushPipe(t *testing.T, to string) *hushPipe {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &hushPipe{ln: ln}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", to)
			if err != nil {
				a.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, a, b)
			p.dials++
			p.mu.Unlock()
			pump := func(dst, src net.Conn) {
				defer wg.Done()
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if n > 0 && !p.hushed.Load() {
						if _, err := dst.Write(buf[:n]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}
			wg.Add(2)
			go pump(a, b)
			go pump(b, a)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
		wg.Wait()
	})
	return p
}

func (p *hushPipe) Addr() string { return p.ln.Addr().String() }

// slowH2 is an HTTP/2 vendor that answers after wait, and counts the
// connections it has open.
func slowH2(t *testing.T, wait time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var open atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("X-Conn", r.RemoteAddr)
		io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &open
}

func calmTransport(t *testing.T) *http.Transport {
	t.Helper()
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	if err := CalmHTTP2(tr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

func calmAsk(rt http.RoundTripper, ctx context.Context, url string) (string, error) {
	got, _, err := calmAskOn(rt, ctx, url)
	return got, err
}

// calmAskOn is calmAsk, and the connection the answer came on, as the
// vendor saw its far end.
func calmAskOn(rt http.RoundTripper, ctx context.Context, url string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return "", "", err
	}
	res, err := rt.RoundTrip(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return string(b), res.Header.Get("X-Conn"), err
}

// A path that dies after the first ping of a long silence was answered is
// found at the next one, PingAgain on, and the request on it fails in
// Go's words for a lost ping, which the gateway's lostPing reads.
func TestCalmFindsAPathDeadLaterInTheSilence(t *testing.T) {
	scalePings(t, 100*time.Millisecond, 300*time.Millisecond, 800*time.Millisecond)
	srv, _ := slowH2(t, 30*time.Second)
	pipe := newHushPipe(t, srv.Listener.Addr().String())
	tr := calmTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		// after the first ping (100ms) and its answer
		time.Sleep(450 * time.Millisecond)
		pipe.hushed.Store(true)
	}()
	began := time.Now()
	_, err := calmAsk(tr, ctx, "https://"+pipe.Addr()+"/turn")
	took := time.Since(began)
	if err == nil {
		t.Fatal("the turn on a dead path was answered")
	}
	if !strings.Contains(err.Error(), "http2: client connection lost") {
		t.Fatalf("the turn failed with %v after %v; want the lost ping", err, took)
	}
	// the next ping goes PingAgain after the first (~900ms), and is lost
	// PingWait later (~1.2s)
	if took < 800*time.Millisecond || took > 10*time.Second {
		t.Fatalf("the dead path was found after %v; want at the ping PingAgain after the first", took)
	}
}

// CloseIdleConnections closes a calmed transport's HTTP/2 connections, as
// it closes Go's own: the gateway drops them when the proxy or region
// moves (#4523, #5182). A Dispatch clone, for a provider's own proxy,
// keeps connections of its own, dialled through that proxy, never base's.
func TestCalmIdleConnectionsCloseAndClonesKeepTheirOwn(t *testing.T) {
	srv, open := slowH2(t, 0)
	u := "https://" + srv.Listener.Addr().String() + "/turn"
	tr := calmTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for range 2 {
		if got, err := calmAsk(tr, ctx, u); err != nil || got != "HTTP/2.0" {
			t.Fatalf("a request: %q, %v; want HTTP/2.0", got, err)
		}
	}
	if n := open.Load(); n != 1 {
		t.Fatalf("%d connections open after two requests; want one, kept", n)
	}
	tr.CloseIdleConnections()
	waitFor(t, func() bool { return open.Load() == 0 }, "the idle HTTP/2 connection to close")

	d := Dispatch(tr)
	// the clone first: a connection it dialled and left in base's pool
	// would carry base's next request
	got, own, err := calmAskOn(d, With(ctx, "direct"), u)
	if err != nil || got != "HTTP/2.0" {
		t.Fatalf("a request through a provider's own proxy: %q, %v", got, err)
	}
	got, base, err := calmAskOn(d, ctx, u)
	if err != nil || got != "HTTP/2.0" {
		t.Fatalf("a request through base: %q, %v", got, err)
	}
	if base == own {
		t.Fatalf("base's request went on the clone's connection (%s)", own)
	}
	if n := open.Load(); n != 2 {
		t.Fatalf("%d connections open; want base's and the clone's own", n)
	}
	d.(interface{ CloseIdleConnections() }).CloseIdleConnections()
	waitFor(t, func() bool { return open.Load() == 0 }, "base's and the clone's idle connections to close")
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited 10s for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
