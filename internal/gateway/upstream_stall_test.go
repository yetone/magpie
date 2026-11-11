package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// starvingVendor is an HTTP/2 vendor that has a small stream window and
// never reads a request's body, so it never sends WINDOW_UPDATE: it
// answers pings (Go's server does) and the sender is left in
// awaitFlowControl for good. @imty42 on X: a 535971ms net.send, 499 after
// 8m56s, on a connection the health ping called healthy.
func starvingVendor(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	vendor := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	vendor.EnableHTTP2 = true
	vendor.Config.HTTP2 = &http.HTTP2Config{MaxReceiveBufferPerConnection: 128 << 10, MaxReceiveBufferPerStream: 128 << 10}
	vendor.StartTLS()
	t.Cleanup(func() {
		close(release)
		vendor.Close()
	})
	return vendor
}

func TestBodyStalledByFlowControlIsGivenUp(t *testing.T) {
	fresh(t)
	var hits atomic.Int32
	vendor := starvingVendor(t, &hits)
	trustVendor(t, vendor)
	was := bodyStall
	bodyStall = 400 * time.Millisecond
	t.Cleanup(func() { bodyStall = was })
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("a", 512<<10) + `"}]}`)
	// the cap only keeps a failing run from hanging; a run that works
	// is far inside it
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vendor.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.client.Do(req)
	if err == nil {
		res.Body.Close()
		t.Fatalf("a body the vendor never took got a %d; want the stall to end the try", res.StatusCode)
	}
	if !strings.Contains(err.Error(), stalledWords) {
		t.Fatalf("error = %v (ctx: %v); want one saying %q", err, ctx.Err(), stalledWords)
	}
	if failure(http.StatusBadGateway, []byte(err.Error())) != failProxy {
		t.Fatalf("failure() = %q; the vendor never had the request, so nobody rests (%q)", failure(http.StatusBadGateway, []byte(err.Error())), failProxy)
	}
	// the request went again on a new connection: both tries reached the
	// vendor's handler (a first connection, still starved, would hold the
	// second try too)
	if n := hits.Load(); n != 2 {
		t.Fatalf("the vendor saw %d tries; want 2 (one, then one more on a new connection)", n)
	}
}

// A body the vendor reads slowly, each chunk taking longer than nothing
// but far less than the stall bound, is sent whole.
func TestBodySlowButMovingIsNotCut(t *testing.T) {
	fresh(t)
	vendor := h2Vendor(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4<<10)
		var n int
		for {
			k, err := r.Body.Read(buf)
			n += k
			if err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprintf(w, "%d", n)
	})
	quickPings(t, vendor)
	was := bodyStall
	bodyStall = 20 * time.Second
	t.Cleanup(func() { bodyStall = was })
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)
	body := bytes.Repeat([]byte("a"), 256<<10)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, vendor.URL+"/x", bytes.NewReader(body))
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("a body that kept moving failed: %v", err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if string(got) != fmt.Sprint(len(body)) {
		t.Fatalf("the vendor read %q bytes; want %d", got, len(body))
	}
}

// The clock runs while the transport holds a chunk, not while the body
// makes one, and stops at the body's end.
func TestStallBodyClock(t *testing.T) {
	const after = 30 * time.Millisecond
	t.Run("a slow source is not a stall", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		pr, pw := io.Pipe()
		b := &stallBody{ReadCloser: pr, after: after, onStall: func() { fired <- struct{}{} }}
		defer b.Close()
		go func() {
			for range 3 {
				time.Sleep(3 * after)
				pw.Write([]byte("x"))
			}
			pw.Close()
		}()
		// reads taking 3x the bound each: the clock only runs between them
		buf := make([]byte, 1)
		for {
			if _, err := b.Read(buf); err != nil {
				break
			}
			// consumed at once
		}
		select {
		case <-fired:
			t.Fatal("a source that was slow to make its chunks counted as a stall")
		case <-time.After(4 * after):
		}
	})
	t.Run("a held chunk is a stall", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		b := &stallBody{ReadCloser: io.NopCloser(strings.NewReader("abcdef")), after: after, onStall: func() { fired <- struct{}{} }}
		defer b.Close()
		if _, err := b.Read(make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-fired:
		case <-time.After(30 * time.Second):
			t.Fatal("a chunk the sender held never ended the try")
		}
	})
	t.Run("the end of the body stops it", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		b := &stallBody{ReadCloser: io.NopCloser(strings.NewReader("ab")), after: after, onStall: func() { fired <- struct{}{} }}
		defer b.Close()
		buf := make([]byte, 8)
		for {
			if _, err := b.Read(buf); err != nil {
				break
			}
		}
		select {
		case <-fired:
			t.Fatal("a body read to its end was cut afterwards")
		case <-time.After(4 * after):
		}
	})
}

// The MCP relay's client is a sender of request bodies over HTTP/2 too: a
// remote MCP server that never opens its window is given up the same way.
func TestMCPRelayBodyStalledIsGivenUp(t *testing.T) {
	var hits atomic.Int32
	vendor := starvingVendor(t, &hits)
	was := bodyStall
	bodyStall = 400 * time.Millisecond
	t.Cleanup(func() { bodyStall = was })
	pool := x509.NewCertPool()
	pool.AddCert(vendor.Certificate())
	c := &http.Client{Transport: mcpTransport(&tls.Config{RootCAs: pool, ServerName: "127.0.0.1"})}
	t.Cleanup(c.CloseIdleConnections)
	body := []byte(`{"jsonrpc":"2.0","params":"` + strings.Repeat("a", 512<<10) + `"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vendor.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Do(req)
	if err == nil {
		res.Body.Close()
		t.Fatalf("a body the server never took got a %d", res.StatusCode)
	}
	if !strings.Contains(err.Error(), stalledWords) {
		t.Fatalf("error = %v (ctx: %v); want one saying %q", err, ctx.Err(), stalledWords)
	}
}
