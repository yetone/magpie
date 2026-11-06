package netproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// h2Server is an HTTP/2 server over TLS answering each request with its
// protocol, and a dispatch over a transport that trusts it.
func h2Server(t *testing.T, h http.HandlerFunc) (*httptest.Server, *dispatch) {
	t.Helper()
	if h == nil {
		h = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) }
	}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	d := Dispatch(&http.Transport{Proxy: Func, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool}}).(*dispatch)
	t.Cleanup(d.CloseIdleConnections)
	return srv, d
}

func protoOf(t *testing.T, d *dispatch, ctx context.Context, u string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := d.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s: %v", u, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// kepner_53726: a provider the user asked HTTP/1.1 of is spoken it at its
// own host, and only there: another host, and a request made for another
// provider (With), go over HTTP/2 as before.
func TestHTTP1AskedOnlyItsHost(t *testing.T) {
	clearProxyEnv(t)
	srv, d := h2Server(t, nil)
	other, _ := h2Server(t, nil)
	asked := WithHTTP1(context.Background(), srv.URL+"/v1")
	if got := protoOf(t, d, asked, srv.URL); got != "HTTP/1.1" {
		t.Errorf("asked HTTP/1.1, spoken %s", got)
	}
	if got := protoOf(t, d, asked, other.URL); got != "HTTP/2.0" {
		t.Errorf("another host spoken %s", got)
	}
	if got := protoOf(t, d, With(asked, ""), srv.URL); got != "HTTP/2.0" {
		t.Errorf("a request for another provider spoken %s", got)
	}
	if got := protoOf(t, d, context.Background(), srv.URL); got != "HTTP/2.0" {
		t.Errorf("not asked, spoken %s", got)
	}
}

// mangler is a TCP relay to addr that, once mangle is set, flips the last
// byte of the next chunk the server sends: a TLS record's MAC no longer
// matches, as a box between that mangles the stream would have it.
func mangler(t *testing.T, addr string) (string, *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mangle := new(atomic.Bool)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", addr)
			if err != nil {
				c.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, c, s)
			mu.Unlock()
			wg.Add(2)
			go func() { defer wg.Done(); io.Copy(s, c); s.Close() }()
			go func() {
				defer wg.Done()
				defer c.Close()
				b := make([]byte, 64<<10)
				for {
					n, err := s.Read(b)
					if n > 0 {
						if mangle.CompareAndSwap(true, false) {
							b[n-1] ^= 0xff
						}
						if _, werr := c.Write(b[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), mangle
}

// kepner_53726: an HTTP/2 stream from a company gateway failed mid-reply
// with "tls: bad record MAC" again and again until a restart. The
// connection it broke on is gone with it (Go's transport drops it), and
// the requests to that host after it go over HTTP/1.1, so the next turn
// doesn't break the same way on a new HTTP/2 connection.
func TestH2RecordErrorMovesHostToHTTP1(t *testing.T) {
	clearProxyEnv(t)
	more := make(chan struct{})
	srv, d := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			io.WriteString(w, r.Proto)
			return
		}
		io.WriteString(w, "first ")
		w.(http.Flusher).Flush()
		select {
		case <-more:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, strings.Repeat("x", 4096))
		w.(http.Flusher).Flush()
	})
	addr, mangle := mangler(t, srv.Listener.Addr().String())
	base := "https://" + addr
	u, _ := url.Parse(base)
	t.Cleanup(func() {
		h1For.Lock()
		delete(h1For.at, hostKey(u))
		h1For.Unlock()
	})

	req, _ := http.NewRequest(http.MethodGet, base+"/stream", nil)
	res, err := d.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtoMajor != 2 {
		t.Fatalf("the stream went over %s", res.Proto)
	}
	first := make([]byte, len("first "))
	if _, err := io.ReadFull(res.Body, first); err != nil {
		t.Fatal(err)
	}
	mangle.Store(true)
	close(more)
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(res.Body); done <- err }()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the mangled stream never ended")
	}
	res.Body.Close()
	if !recordError(err) {
		t.Fatalf("the mangled stream ended with %v, not a TLS record error", err)
	}
	if got := protoOf(t, d, context.Background(), base+"/after"); got != "HTTP/1.1" {
		t.Errorf("the request after a TLS record error on HTTP/2 went over %s", got)
	}
	if got := protoOf(t, d, context.Background(), srv.URL+"/other"); got != "HTTP/2.0" {
		t.Errorf("another host:port went over %s", got)
	}
}

func TestRecordError(t *testing.T) {
	for s, want := range map[string]bool{
		"local error: tls: bad record MAC":                                  true,
		"remote error: tls: bad record MAC":                                 true,
		"tls: received record with version eaf1 when expecting version 303": true,
		"tls: oversized record received with length 20000":                  true,
		"EOF":                           false,
		"http2: client connection lost": false,
	} {
		if got := recordError(errString(s)); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
	if !recordError(tls.RecordHeaderError{Msg: "x"}) {
		t.Error("a RecordHeaderError not taken")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
