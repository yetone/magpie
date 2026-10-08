package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// connectTo is an HTTP proxy that tunnels every CONNECT to addr, whatever
// host it is asked for, counting them and keeping the last host asked.
func connectTo(t *testing.T, addr string) (string, *atomic.Int32, *atomic.Value) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	var asked atomic.Value
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var head []byte
				b := make([]byte, 1)
				for !strings.HasSuffix(string(head), "\r\n\r\n") {
					if _, err := c.Read(b); err != nil {
						return
					}
					head = append(head, b[0])
				}
				f := strings.Fields(string(head))
				if len(f) < 2 || f[0] != "CONNECT" {
					fmt.Fprint(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
					return
				}
				asked.Store(f[1])
				up, err := net.Dial("tcp", addr)
				if err != nil {
					fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				n.Add(1)
				fmt.Fprint(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), &n, &asked
}

// #1035: Cursor's plugin runs its chats on node:http2, which Bun connects
// directly whatever the proxy: where Cursor is reached only through
// magpie's proxy, the plugin's fetches (sign-in, models, usage) came
// through it and its chats didn't. A session a plugin opens goes through
// the proxy its fetches take, as the built-in's requests did; one for a
// provider set to "direct" goes through none.
func TestPluginHTTP2TakesTheProxy(t *testing.T) {
	sandbox(t)
	vendor := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "vendor %s %s", r.Proto, r.URL.Path)
	}))
	vendor.EnableHTTP2 = true
	vendor.StartTLS()
	defer vendor.Close()
	proxy, carried, asked := connectTo(t, strings.TrimPrefix(vendor.URL, "https://"))
	// a host that resolves nowhere: reached only through the proxy
	t.Setenv("FAKE_H2", "https://h2vendor.invalid:8443")
	t.Setenv("FAKE_BASE", "https://h2vendor.invalid:8443/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	fetch := func(proxy string) (int, string) {
		res, err := Fetch(netproxy.With(ctx, proxy), FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: "https://h2vendor.invalid:8443/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, b := fetch(proxy); code != 200 || b != "vendor HTTP/2.0 /v1/chat/completions" || carried.Load() != 1 {
		t.Fatalf("an HTTP/2 session through the proxy: %d %q; it carried %d", code, b, carried.Load())
	}
	if got := asked.Load(); got != "h2vendor.invalid:8443" {
		t.Fatalf("the proxy was asked for %v, want h2vendor.invalid:8443", got)
	}
	if code, b := fetch("direct"); code != 502 || carried.Load() != 1 {
		t.Fatalf("a provider set to direct: %d %q; the proxy carried %d, want it untouched", code, b, carried.Load())
	}
	dead := "http://" + closedPort(t)
	if code, b := fetch(dead); code != 502 || !strings.Contains(b, "proxyconnect tcp: dial tcp 127.0.0.1:") {
		t.Fatalf("through a proxy that is down: %d %q, want it said as Go's transport says it", code, b)
	}
}
