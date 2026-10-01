package netproxy

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSOCKS is a SOCKS5 proxy taking user u and password pw ("" for none),
// counting the connections it carried.
func fakeSOCKS(t *testing.T, u, pw string) (*url.URL, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var h [2]byte
				io.ReadFull(c, h[:])
				ms := make([]byte, h[1])
				io.ReadFull(c, ms)
				if u != "" {
					c.Write([]byte{5, 2})
					var v [2]byte
					io.ReadFull(c, v[:])
					got := make([]byte, v[1])
					io.ReadFull(c, got)
					var l [1]byte
					io.ReadFull(c, l[:])
					p := make([]byte, l[0])
					io.ReadFull(c, p)
					if string(got) != u || string(p) != pw {
						c.Write([]byte{1, 1})
						return
					}
					c.Write([]byte{1, 0})
				} else {
					c.Write([]byte{5, 0})
				}
				var r [4]byte
				io.ReadFull(c, r[:])
				var host string
				switch r[3] {
				case 1:
					b := make([]byte, 4)
					io.ReadFull(c, b)
					host = net.IP(b).String()
				case 3:
					var l [1]byte
					io.ReadFull(c, l[:])
					b := make([]byte, l[0])
					io.ReadFull(c, b)
					host = string(b)
				}
				var pb [2]byte
				io.ReadFull(c, pb[:])
				up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:])))))
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				n.Add(1)
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	s := &url.URL{Scheme: "socks5", Host: ln.Addr().String()}
	if u != "" {
		s.User = url.UserPassword(u, pw)
	}
	return s, &n
}

// A plugin's request goes through a SOCKS5 proxy as a built-in's does:
// the bridge carries a plain http:// request and an https:// one's
// CONNECT tunnel through it, signing in to it when it asks.
func TestBridgeCarriesThroughSOCKS(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain "+r.URL.Path) }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls "+r.URL.Path) }))
	defer tls.Close()
	for _, auth := range []bool{false, true} {
		u, pw := "", ""
		if auth {
			u, pw = "me", "secret"
		}
		socks, n := fakeSOCKS(t, u, pw)
		b, err := Bridge(socks)
		if err != nil {
			t.Fatal(err)
		}
		if again, _ := Bridge(socks); again != b {
			t.Fatalf("a second bridge %s for the same proxy, not %s", again, b)
		}
		if ForBun(socks.String()) != b || !strings.HasPrefix(b, "http://127.0.0.1:") {
			t.Fatalf("ForBun(%s) = %s, want %s", socks, ForBun(socks.String()), b)
		}
		pu, _ := url.Parse(b)
		tr := tls.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(pu)
		c := &http.Client{Transport: tr}
		for _, x := range []struct{ url, want string }{{plain.URL + "/a", "plain /a"}, {tls.URL + "/b", "tls /b"}} {
			res, err := c.Get(x.url)
			if err != nil {
				t.Fatalf("auth=%v %s: %v", auth, x.url, err)
			}
			got, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if string(got) != x.want {
				t.Fatalf("auth=%v %s: %q, want %q", auth, x.url, got, x.want)
			}
		}
		if n.Load() != 2 {
			t.Fatalf("auth=%v: the SOCKS proxy carried %d connections, want 2", auth, n.Load())
		}
	}
	for _, p := range []string{"http://127.0.0.1:7890", "direct", ""} {
		if got := ForBun(p); got != p {
			t.Fatalf("ForBun(%q) = %q, want it as it is", p, got)
		}
	}
}

// A SOCKS proxy that isn't there is said as Go's transport says it, so
// the gateway takes it for the proxy's failure, not the vendor's.
func TestBridgeSaysTheProxyIsDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	b, err := Bridge(&url.URL{Scheme: "socks5h", Host: addr})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", strings.TrimPrefix(b, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	got, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(got), "HTTP/1.1 502") || !strings.Contains(string(got), "socks connect tcp "+addr) {
		t.Fatalf("answered %q", got)
	}
}

// Bun itself, the plugin host's runtime, fetches through the bridge: run
// when a bun is at hand ($MAGPIE_BUN or on PATH).
func TestBunThroughBridge(t *testing.T) {
	bun := os.Getenv("MAGPIE_BUN")
	if bun == "" {
		bun, _ = exec.LookPath("bun")
	}
	if bun == "" {
		t.Skip("no bun")
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain") }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls") }))
	defer tls.Close()
	socks, n := fakeSOCKS(t, "me", "pw")
	b, _ := Bridge(socks)
	// loopback is never proxied by magpie, but the test's servers are on it:
	// Bun is given the proxy outright
	js := `for (const u of process.env.URLS.split(" ")) console.log(await (await fetch(u, { proxy: "` + b + `", tls: { rejectUnauthorized: false } })).text())`
	cmd := exec.Command(bun, "-e", js)
	cmd.Env = append(os.Environ(), "URLS="+plain.URL+" "+tls.URL)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "plain\ntls" {
		t.Fatalf("bun: %v %s", err, out)
	}
	if n.Load() != 2 {
		t.Fatalf("the SOCKS proxy carried %d connections, want 2", n.Load())
	}
}
