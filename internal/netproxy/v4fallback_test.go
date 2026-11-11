package netproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// A host whose IPv6 address takes the connection and closes it at once
// (a TUN that can't carry IPv6) is reached over IPv4, the first request
// included, and dialed over IPv4 from then on.
func TestIPv4Fallback(t *testing.T) {
	v6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no ::1:", err)
	}
	defer v6.Close()
	go func() {
		for {
			c, err := v6.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, r.Method+" "+string(b))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	t.Cleanup(func() { v4Only.Lock(); v4Only.at = nil; v4Only.Unlock() })

	var mu sync.Mutex
	var dials []string
	var d net.Dialer
	base := &http.Transport{
		Proxy:             func(*http.Request) (*url.URL, error) { return nil, nil },
		TLSClientConfig:   srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
		ForceAttemptHTTP2: true,
		// example.com resolves to [::1] first, as a dual-stack name does
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, network)
			mu.Unlock()
			if network == "tcp4" {
				return d.DialContext(ctx, "tcp4", srv.Listener.Addr().String())
			}
			return d.DialContext(ctx, "tcp", v6.Addr().String())
		},
	}
	c := &http.Client{Transport: Dispatch(base)}
	for i, method := range []string{"GET", "POST"} {
		req, _ := http.NewRequest(method, "https://example.com/", strings.NewReader("body"))
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v (dials %v)", i, err, dials)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(b) != method+" body" || res.ProtoMajor != 2 {
			t.Fatalf("request %d: %q over %s", i, b, res.Proto)
		}
		base.CloseIdleConnections()
	}
	if got := strings.Join(dials, ","); got != "tcp,tcp4,tcp4" {
		t.Fatalf("dials %s, want tcp,tcp4,tcp4", got)
	}
}

// A name with an A record nobody answers and an AAAA record that served
// before (a remote magpie that is IPv6-only) is not stuck on IPv4 after
// one IPv6 connection fails: the IPv4 dial fails, the mark is cleared and
// IPv6 is tried again (ZeHeaton23141 on X).
func TestIPv6OnlyHostRecovers(t *testing.T) {
	v6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no ::1:", err)
	}
	defer v6.Close()
	go func() {
		for {
			c, err := v6.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				http.ReadRequest(bufio.NewReader(c))
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}()
		}
	}()
	t.Cleanup(func() { v4Only.Lock(); v4Only.at = nil; v4Only.Unlock() })
	var d net.Dialer
	base := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network == "tcp4" {
				return nil, errors.New("dial tcp4: no route to host") // the A record
			}
			return d.DialContext(ctx, "tcp", v6.Addr().String())
		},
	}
	addr := "magpie.example:3425"
	markV4(addr) // an earlier IPv6 hiccup
	c := &http.Client{Transport: Dispatch(base)}
	for i := 0; i < 2; i++ {
		res, err := c.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		io.ReadAll(res.Body)
		res.Body.Close()
	}
	if !v4Since(addr).IsZero() {
		t.Fatal("IPv4 mark kept after the IPv4 dial failed")
	}
	// an IPv6 literal is never marked
	markV4("[::1]:3425")
	if !v4Since("[::1]:3425").IsZero() {
		t.Fatal("IPv6 literal marked")
	}
}
