package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// #2593: with Clash's TUN fake-ip (Clash Verge Rev on Windows, TomXPRIME),
// Surge's enhanced mode (h4rk8s) or Loon on, the system resolver answers
// every name in 198.18.0.0/15, and every web_fetch, caller's image and
// vendor's image was refused as "a private address". These tests stand a
// fake resolver in for the fake-IP one, a local server for the TUN or the
// proxy; nothing reaches the network.

// answers points lookupIP at several addresses per name.
func answers(t *testing.T, names map[string][]string) {
	t.Helper()
	was := lookupIP
	lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		as, ok := names[host]
		if !ok {
			return nil, errors.New("no such host in the test: " + host)
		}
		var out []netip.Addr
		for _, a := range as {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIP = was })
}

// useProxy sets magpie's proxy ("direct" for none) for the test.
func useProxy(t *testing.T, v string) {
	t.Helper()
	was := settings.Load()
	st := was
	st.Proxy = v
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(was) })
}

// tun stands a local server in for the TUN: a direct request's dial to the
// pinned address goes there, once the guard has said the address may be
// dialled. It records the addresses dialled, and fails the test when the
// same dial guard would let in another address in the fake range, or a
// private one.
func tun(t *testing.T, srv *httptest.Server) func() []string {
	t.Helper()
	var mu sync.Mutex
	var dialled []string
	was := dialPinned
	dialPinned = func(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialled = append(dialled, addr)
		mu.Unlock()
		if err := d.Control(network, addr, nil); err != nil {
			return nil, err
		}
		for _, other := range []string{"198.18.0.8:80", "198.19.1.1:443", "127.0.0.1:80", "192.168.1.10:80"} {
			if other != addr && d.Control(network, other, nil) == nil {
				t.Errorf("dialling %s, the guard would let %s in too", addr, other)
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(func() { dialPinned = was })
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), dialled...) }
}

// recorder is a proxy on this computer that records what it is asked: the
// URL of a plain request, the authority of a CONNECT.
func fakeProxy(t *testing.T, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodConnect {
			asked = append(asked, "CONNECT "+r.Host)
		} else {
			asked = append(asked, r.URL.String()+" Host="+r.Host)
		}
		mu.Unlock()
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusOK) // and forwards nowhere: TLS fails after
			return
		}
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "http://inside.test/", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), asked...) }
}

func guardedGet(u string) error {
	req, _ := http.NewRequest("GET", u, nil)
	resp, err := publicClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

// Through a proxy on this computer (Clash's 127.0.0.1:7890, Surge's
// 6152), a name the fake-IP resolver answered is handed to the proxy by
// its name: a real proxy can't route the 198.18 address (h4rk8s: CONNECT
// by the name went through, by the fake address didn't). web_fetch reads
// the page; https:// is a CONNECT by name.
func TestFakeIPNameGoesToALocalProxyByName(t *testing.T) {
	fresh(t)
	proxy, asked := fakeProxy(t, "<html><head><title>Example Domain</title></head><body><p>fetched by name</p></body></html>")
	useProxy(t, proxy.URL)
	answers(t, map[string][]string{
		"example.com": {"198.18.0.7"},
		"loon.test":   {"198.18.1.57", "fd27:712::c612:139"}, // Loon's AAAA for its own A
		"inside.test": {"198.18.0.9", "192.168.1.10"},
	})
	h := lanGuard(New().Handler())
	got, isErr := fetchText_(t, h, fetchCall("http://example.com/", ""))
	if isErr || !strings.Contains(got, "fetched by name") {
		t.Fatalf("web_fetch of a fake-IP name through a local proxy: %v %q", isErr, got)
	}
	if a := asked(); len(a) != 1 || a[0] != "http://example.com:80/ Host=example.com:80" {
		t.Fatalf("the proxy was asked %q, want the name", a)
	}
	if err := guardedGet("http://loon.test/x.png"); err != nil {
		t.Fatalf("Loon's answer: %v", err)
	}
	if a := asked(); len(a) != 2 || !strings.HasPrefix(a[1], "http://loon.test:80/x.png") {
		t.Fatalf("Loon: the proxy was asked %q", a)
	}
	// https://: the CONNECT names the host, never 198.18
	if err := guardedGet("https://example.com/x.png"); err == nil {
		t.Fatal("a CONNECT that forwards nowhere answered")
	}
	if a := asked(); len(a) != 3 || a[2] != "CONNECT example.com:443" {
		t.Fatalf("https: the proxy was asked %q", a)
	}
	// a request's own Host can't name the proxy another target
	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	req.Host = "169.254.169.254"
	if resp, err := publicClient.Do(req); err == nil {
		resp.Body.Close()
	}
	if a := asked(); len(a) != 4 || a[3] != "http://example.com:80/ Host=example.com:80" {
		t.Fatalf("a foreign Host: the proxy was asked %q", a)
	}
	// a redirect is judged again: one to a name with a private answer
	// beside its fake one is refused before the proxy is asked for it
	if err := guardedGet("http://example.com/to-private"); !errors.Is(err, errPrivateAddress) {
		t.Fatalf("a redirect to a mixed answer: %v", err)
	}
	if a := asked(); len(a) != 5 {
		t.Fatalf("a redirect to a mixed answer reached the proxy: %q", a)
	}
}

// Pure TUN, no proxy set (TomXPRIME: Windows, Clash Verge Rev, no
// HTTPS_PROXY): the fake address is pinned and dialled, and the TUN
// carries it. The dial guard lets that one address in, no other in the
// range.
func TestFakeIPNameDialledThroughTheTUN(t *testing.T) {
	fresh(t)
	useProxy(t, "direct")
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("through the tun " + r.Host))
	}))
	t.Cleanup(page.Close)
	dialled := tun(t, page)
	answers(t, map[string][]string{"example.com": {"198.18.0.7"}, "real.test": {"93.184.216.34", "fd27:712::5db8:d822"}})
	h := lanGuard(New().Handler())
	got, isErr := fetchText_(t, h, fetchCall("http://example.com/", ""))
	if isErr || !strings.Contains(got, "through the tun example.com") {
		t.Fatalf("web_fetch of a fake-IP name with no proxy: %v %q", isErr, got)
	}
	if d := dialled(); len(d) != 1 || d[0] != "198.18.0.7:80" {
		t.Fatalf("dialled %q, want the pinned fake address", d)
	}
	// a real answer is pinned as ever; Loon's AAAA beside it isn't dialled
	if err := guardedGet("http://real.test/"); err != nil {
		t.Fatalf("a real answer: %v", err)
	}
	if d := dialled(); len(d) != 2 || d[1] != "93.184.216.34:80" {
		t.Fatalf("dialled %q", d)
	}
}

// Clash Verge Rev turns on IPv6 with mihomo's fake-ip-range6 by default
// (fdfe:dcba:9876::1/64, or 2001:2::/64 it fills in), so the resolver
// answers a fake AAAA beside the fake A, and every web_fetch was refused
// as a private address (jiezhengj on Discord, mihomo 1.19.33's TUN). The
// AAAA is never dialled: the fake A is pinned, through the TUN, and a
// proxy on this computer is given the name.
func TestFakeIPWithItsIPv6Answer(t *testing.T) {
	fresh(t)
	useProxy(t, "direct")
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("through the tun " + r.Host))
	}))
	t.Cleanup(page.Close)
	dialled := tun(t, page)
	answers(t, map[string][]string{
		"example.com": {"fdfe:dcba:9876::7", "198.18.0.7"},
		"filled.test": {"198.18.0.8", "2001:2::8"},
	})
	h := lanGuard(New().Handler())
	got, isErr := fetchText_(t, h, fetchCall("http://example.com/", ""))
	if isErr || !strings.Contains(got, "through the tun example.com") {
		t.Fatalf("web_fetch of a name with a fake A and AAAA, no proxy: %v %q", isErr, got)
	}
	if err := guardedGet("http://filled.test/"); err != nil {
		t.Fatalf("2001:2::/64 beside the fake A: %v", err)
	}
	if d := dialled(); len(d) != 2 || d[0] != "198.18.0.7:80" || d[1] != "198.18.0.8:80" {
		t.Fatalf("dialled %q, want the fake A answers only", d)
	}
	proxy, asked := fakeProxy(t, "<html><body><p>fetched by name</p></body></html>")
	useProxy(t, proxy.URL)
	got, isErr = fetchText_(t, h, fetchCall("http://example.com/", ""))
	if isErr || !strings.Contains(got, "fetched by name") {
		t.Fatalf("web_fetch through a local proxy: %v %q", isErr, got)
	}
	if a := asked(); len(a) != 1 || a[0] != "http://example.com:80/ Host=example.com:80" {
		t.Fatalf("the proxy was asked %q, want the name", a)
	}
}

// What stays refused, with a proxy on this computer and without: a
// private answer, a fake answer beside a private (or any real) one, a
// fake address written as the URL, and a fake answer through a proxy
// elsewhere. Neither the proxy nor the TUN is asked.
func TestFakeIPRefusesTheRest(t *testing.T) {
	fresh(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("reached")) }))
	t.Cleanup(page.Close)
	dialled := tun(t, page)
	answers(t, map[string][]string{
		"example.com":   {"198.18.0.7"},
		"lan.test":      {"192.168.1.10"},
		"loop.test":     {"127.0.0.1"},
		"meta.test":     {"169.254.169.254"},
		"cgnat.test":    {"100.64.0.1"},
		"ten.test":      {"10.0.0.1"},
		"mixed.test":    {"198.18.0.9", "192.168.1.10"},
		"mixed6.test":   {"198.18.0.9", "::1"},
		"mixedpub.test": {"198.18.0.9", "93.184.216.34"},
		"loonlan.test":  {"fd27:712::a00:1", "10.0.0.1"},
		"loonbare.test": {"fd27:712::c612:139"},
		"ulabare.test":  {"fdfe:dcba:9876::7"},
		"ulapub.test":   {"fdfe:dcba:9876::7", "93.184.216.34"},
		"fake6lan.test": {"198.18.0.9", "fdfe:dcba:9876::7", "10.0.0.1"},
		"fake6lo.test":  {"198.18.0.9", "fdfe:dcba:9876::7", "::1"},
	})
	refused := []string{
		"http://lan.test/", "http://loop.test/", "http://meta.test/latest/meta-data/", "http://cgnat.test/",
		"http://ten.test/", "http://mixed.test/", "http://mixed6.test/", "http://mixedpub.test/",
		"http://loonlan.test/", "http://loonbare.test/", "http://198.18.0.7/", "https://198.18.0.7/",
		"http://[::ffff:198.18.0.7]/",
		"http://ulabare.test/", "http://ulapub.test/", "http://fake6lan.test/", "http://fake6lo.test/",
		"http://[fdfe:dcba:9876::7]/", "http://[2001:2::7]/",
	}
	proxy, asked := fakeProxy(t, "reached")
	for _, p := range []string{"direct", proxy.URL, "http://localhost:" + proxy.URL[strings.LastIndex(proxy.URL, ":")+1:]} {
		useProxy(t, p)
		for _, u := range refused {
			if err := guardedGet(u); !errors.Is(err, errPrivateAddress) {
				t.Errorf("proxy %s, %s: %v, want the private-address refusal", p, u, err)
			}
		}
	}
	// a proxy elsewhere can't be handed the name for a fake answer
	// (203.0.113.9 is never dialled: the request is refused first)
	useProxy(t, "http://203.0.113.9:3128")
	if err := guardedGet("http://example.com/"); !errors.Is(err, errPrivateAddress) {
		t.Errorf("a fake answer through a proxy elsewhere: %v", err)
	}
	if a, d := asked(), dialled(); len(a) != 0 || len(d) != 0 {
		t.Fatalf("the proxy was asked %q, the TUN dialled %q", a, d)
	}
}
