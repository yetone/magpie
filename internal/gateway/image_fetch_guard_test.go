package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": false, "127.9.9.9": false, "10.1.2.3": false, "172.16.0.1": false,
		"192.168.1.1": false, "169.254.169.254": false, "100.64.0.1": false, "100.100.100.100": false,
		"0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false,
		"::1": false, "::": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"::ffff:169.254.169.254": false, "::ffff:100.64.0.1": false, "fe80::1": false, "fc00::1": false, "fd12::1": false,
		"ff02::1": false, "64:ff9b::7f00:1": false, "2002:7f00:1::": false, "::7f00:1": false,
		"8.8.8.8": true, "93.184.216.34": true, "::ffff:8.8.8.8": true, "2606:4700::1111": true,
		"64:ff9b::808:808": true,
	} {
		if got := publicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

// secret is a service only this computer reaches; what it was asked is
// counted.
func secret(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func editFrom(t *testing.T, s *Server, url string, edit func(*http.Request)) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader(`{"model":"art/gpt-image-1","prompt":"make it blue","image":"`+url+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if edit != nil {
		edit(req)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func resolveTo(t *testing.T, names map[string]string) {
	t.Helper()
	was := lookupIP
	lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := names[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return was(context.Background(), host)
	}
	t.Cleanup(func() { lookupIP = was })
}

// An image URL a caller sends to be edited is downloaded by magpie for an
// images API that takes files only. From another computer (a gateway
// key's holder) it is never downloaded from this computer or its network,
// however the URL names it.
func TestEditImageURLNotFetchedFromPrivateAddresses(t *testing.T) {
	s, e := easeled(t)
	srv, hits := secret(t, string(pngBytes))
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	resolveTo(t, map[string]string{"inside.test": "127.0.0.1", "nat64.test": "64:ff9b::7f00:1"})
	for _, u := range []string{
		srv.URL + "/img.png",
		"http://localhost:" + port + "/img.png",
		"http://[::1]:" + port + "/img.png",
		"http://[::ffff:127.0.0.1]:" + port + "/img.png",
		"http://inside.test:" + port + "/img.png",
		"http://nat64.test:" + port + "/img.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/img.png",
		"http://0.0.0.0:" + port + "/img.png",
	} {
		code, raw := editFrom(t, s, u, nil)
		if code != 400 || !strings.Contains(raw, "private address") {
			t.Errorf("%s: %d %s", u, code, raw)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the private service was asked %d times", n)
	}
	if sent := e.got("/v1/images/edits"); len(sent) != 0 {
		t.Fatalf("the vendor was sent %d edits", len(sent))
	}
}

// Through a proxy the dialler can't see where it goes: the proxy is sent
// the address that was checked, never the name, and a redirect to a
// private address is refused before it is asked for.
func TestEditImageURLThroughProxyIsPinnedAndRedirectsChecked(t *testing.T) {
	s, e := easeled(t)
	srv, hits := secret(t, string(pngBytes))
	var mu sync.Mutex
	var asked []string
	seen := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), asked...) }
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.String()+" Host="+r.Host)
		mu.Unlock()
		switch r.URL.Path {
		case "/img.png":
			w.Write(pngBytes)
		case "/hop":
			http.Redirect(w, r, srv.URL+"/img.png", http.StatusFound)
		case "/far":
			http.Redirect(w, r, "http://inside.test/img.png", http.StatusFound)
		}
	}))
	t.Cleanup(proxy.Close)
	st := settings.Load()
	st.Proxy = proxy.URL
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	resolveTo(t, map[string]string{"pics.test": "93.184.216.34", "inside.test": "192.168.1.10"})

	code, raw := editFrom(t, s, "http://pics.test/img.png", nil)
	if code != 200 || len(e.got("/v1/images/edits")) != 1 {
		t.Fatalf("a public image: %d %s", code, raw)
	}
	if a := seen(); len(a) != 1 || a[0] != "http://93.184.216.34:80/img.png Host=93.184.216.34:80" {
		t.Fatalf("the proxy was asked %q", a)
	}
	for _, u := range []string{"http://pics.test/hop", "http://pics.test/far"} {
		if code, raw := editFrom(t, s, u, nil); code != 400 || !strings.Contains(raw, "private address") {
			t.Errorf("%s: %d %s", u, code, raw)
		}
	}
	if a := seen(); hits.Load() != 0 || len(a) != 3 {
		t.Fatalf("private service asked %d times, proxy asked %q", hits.Load(), a)
	}
}

// Where a request came from gives it nothing: one from this computer is
// also what a reverse proxy or relay in front of magpie on the same host
// sends for anyone it serves, so it may not name a private address either.
// Only the user's own settings.json "fetchPrivate" lets one in, and what
// is then downloaded must still be an image.
func TestEditImageURLFromThisComputer(t *testing.T) {
	s, e := easeled(t)
	srv, hits := secret(t, string(pngBytes))
	local := func(r *http.Request) { r.RemoteAddr = "127.0.0.1:50000" }
	for name, edit := range map[string]func(*http.Request){
		"this computer": local,
		"a reverse proxy here": func(r *http.Request) {
			local(r)
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
		},
		"[::1]": func(r *http.Request) { r.RemoteAddr = "[::1]:50000" },
	} {
		if code, raw := editFrom(t, s, srv.URL+"/img.png", edit); code != 400 || !strings.Contains(raw, "private address") || !strings.Contains(raw, "fetchPrivate") {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	if hits.Load() != 0 || len(e.got("/v1/images/edits")) != 0 {
		t.Fatalf("the private service was asked %d times", hits.Load())
	}

	allowPrivate(t, "127.0.0.1")
	if code, raw := editFrom(t, s, srv.URL+"/img.png", local); code != 200 || hits.Load() != 1 || len(e.got("/v1/images/edits")) != 1 {
		t.Fatalf("named in fetchPrivate: %d %s", code, raw)
	}
	json, _ := secret(t, `{"admin_token":"s3cret"}`)
	code, raw := editFrom(t, s, json.URL+"/status", local)
	if code != 400 || !strings.Contains(raw, "isn't an image") || strings.Contains(raw, "s3cret") || len(e.got("/v1/images/edits")) != 1 {
		t.Fatalf("not an image: %d %s", code, raw)
	}
}

// allowPrivate writes the user's settings.json "fetchPrivate" for the test.
func allowPrivate(t *testing.T, entries ...string) {
	t.Helper()
	was := settings.Load()
	st := was
	st.FetchPrivate = entries
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(was) })
}

// fetchPrivate's entries are a name, name:port, an IP or a range; a
// settings.json that can't be read lets nothing extra in.
func TestFetchPrivateEntries(t *testing.T) {
	allowPrivate(t, "nas.lan", "box.lan:8080", "10.1.2.3", "192.168.7.0/24", "fd00::/8", "  ", "not a range/99")
	a := allowFor([]string{"vendor.lan:9000"})
	for _, c := range []struct {
		host, port string
		ok         bool
	}{
		{"nas.lan", "443", true}, {"NAS.lan", "80", true}, {"box.lan", "8080", true}, {"box.lan", "80", false},
		{"vendor.lan", "9000", true}, {"vendor.lan", "9001", false}, {"other.lan", "80", false},
	} {
		if a.host(c.host, c.port) != c.ok {
			t.Errorf("host %s:%s: %v", c.host, c.port, !c.ok)
		}
	}
	for ip, ok := range map[string]bool{
		"10.1.2.3": true, "10.1.2.4": false, "192.168.7.200": true, "192.168.8.1": false,
		"fd12::1": true, "127.0.0.1": false, "169.254.169.254": false, "8.8.8.8": true,
	} {
		if a.addr(netip.MustParseAddr(ip)) != ok {
			t.Errorf("%s: %v", ip, !ok)
		}
	}
	// the package's tests share one HOME: the file's own bytes go back
	// before allowPrivate's Save, which refuses to write over "{not json"
	raw, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(settings.Path(), raw, 0o600); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(settings.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a = allowFor(nil)
	if a.host("nas.lan", "443") || a.addr(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("an unreadable settings.json let a private host in")
	}
	if got := hostOf("http://Box.lan/x"); got != "box.lan:80" {
		t.Fatalf("hostOf: %q", got)
	}
	if got := hostOf("https://[fd00::1]/v1"); got != "[fd00::1]:443" {
		t.Fatalf("hostOf: %q", got)
	}
}

// The dialler checks the address it connects to, whatever name led there.
func TestPrivateDialerRefusesPrivateAddresses(t *testing.T) {
	srv, hits := secret(t, "")
	for _, addr := range []string{srv.Listener.Addr().String(), "[::1]:9"} {
		c, err := privateDialer.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			c.Close()
		}
		if !errors.Is(err, errPrivateAddress) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("the private service was asked")
	}
}
