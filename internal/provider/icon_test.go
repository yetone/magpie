package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestStoreIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q", ref)
	}
	if again, _ := StoreIcon(png); again != ref {
		t.Errorf("same picture stored twice: %q, %q", ref, again)
	}
	p := IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(p); err != nil || string(b) != string(png) {
		t.Fatalf("IconFile(%q) = %q: %v", ref, p, err)
	}

	if ref, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg: %q, %v", ref, err)
	}
	if _, err := StoreIcon([]byte("hello")); err == nil {
		t.Error("text taken as a picture")
	}
	if _, err := StoreIcon(make([]byte, MaxIcon+1)); err == nil {
		t.Error("oversized picture taken")
	}
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", ""} {
		if IconFile(bad) != "" {
			t.Errorf("IconFile(%q) resolved", bad)
		}
	}
}

func TestFetchIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	mux := http.NewServeMux()
	mux.HandleFunc("/logo.png", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(png)
	})
	mux.HandleFunc("/huge.png", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(make([]byte, MaxIcon+1))
	})
	mux.HandleFunc("/text", func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("hello"))
	})
	mux.HandleFunc("/missing", func(rw http.ResponseWriter, r *http.Request) {
		http.NotFound(rw, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// httptest is plain http on loopback, which FetchIcon refuses on purpose;
	// the guard is checked here, then the download itself is driven directly.
	if _, err := FetchIcon(context.Background(), srv.URL+"/logo.png"); err == nil {
		t.Error("a loopback icon URL was fetched")
	}
	if _, err := iconURL(srv.URL + "/logo.png"); err == nil {
		t.Error("iconURL accepted a loopback host")
	}

	get := func(name string) (string, error) {
		return fetchIcon(context.Background(), srv.Client(), srv.URL+name)
	}
	ref, err := get("/logo.png")
	if err != nil || !strings.HasPrefix(ref, "file:") {
		t.Fatalf("fetch = %q, %v", ref, err)
	}
	if b, err := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:"))); err != nil || string(b) != string(png) {
		t.Fatalf("stored picture: %q, %v", b, err)
	}
	if _, err := get("/huge.png"); err == nil {
		t.Error("an oversized icon was stored")
	}
	if _, err := get("/text"); err == nil {
		t.Error("a non-picture was stored")
	}
	if _, err := get("/missing"); err == nil {
		t.Error("a 404 was stored")
	}
}

func TestPublicIP(t *testing.T) {
	// 198.18/15 is what fake-ip proxies (Clash TUN, Surge's enhanced mode)
	// answer with, carrying the connection to the real host (#252)
	public := []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111", "198.18.0.5", "198.19.255.254"}
	private := []string{
		"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1",
		"0.0.0.0", "100.64.0.1", "192.0.0.1", "192.0.2.1", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "fc00::1", "fe80::1", "2002::1",
	}
	for _, s := range public {
		if !publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged private", s)
		}
	}
	for _, s := range private {
		if publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged public", s)
		}
	}
}

func TestIconIPsLoon(t *testing.T) {
	for _, tt := range []struct {
		name string
		ips  []string
		want []string // nil means the entire answer must be refused
	}{
		{"screenshot", []string{"fd27:712::c600:1061", "198.0.16.97"}, []string{"198.0.16.97"}},
		{"ipv4 first", []string{"198.0.16.97", "fd27:712::c600:1061"}, []string{"198.0.16.97"}},
		{"benchmark pool", []string{"fd27:712::c612:139", "198.18.1.57"}, []string{"198.18.1.57"}},
		{"public dual stack", []string{"8.8.8.8", "2606:4700:4700::1111"}, []string{"8.8.8.8", "2606:4700:4700::1111"}},
		{"unpaired", []string{"fd27:712::c600:1061"}, nil},
		{"unrelated ipv4", []string{"fd27:712::c600:1061", "8.8.8.8"}, nil},
		{"private pair", []string{"fd27:712::a00:1", "10.0.0.1"}, nil},
		{"loopback pair", []string{"fd27:712::7f00:1", "127.0.0.1"}, nil},
		{"other ula", []string{"fd27:712:1::c600:1061", "198.0.16.97"}, nil},
		{"mixed private", []string{"fd27:712::c600:1061", "198.0.16.97", "192.168.1.1"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ips []net.IPAddr
			for _, ip := range tt.ips {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(ip)})
			}
			allowed, err := iconIPs("api.example.com", ips)
			if tt.want == nil {
				if err == nil || len(allowed) != 0 {
					t.Fatalf("private answer accepted: %v, %v", allowed, err)
				}
				return
			}
			var got []string
			for _, ip := range allowed {
				if !publicIP(ip.IP) {
					t.Fatalf("private address passed to dial: %v", ip)
				}
				got = append(got, ip.IP.String())
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	// Explicit private-IP URLs remain invalid, including Loon-shaped ones.
	if _, err := iconURL("https://[fd27:712::c600:1061]/favicon.ico"); err == nil {
		t.Fatal("literal ULA icon URL accepted")
	}
}

// The guarded client must refuse a host that resolves to loopback, even
// though the URL scheme and hostname look fine.
func TestGuardClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("x"))
	}))
	defer srv.Close()
	// a real FetchIcon already refuses 127.0.0.1 by hostname; this checks the
	// dial-time guard by aiming the same client at the loopback server
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := guardClient().Do(req); err == nil {
		t.Fatal("the guarded client reached a loopback address")
	}

	// a name that is not a literal address but resolves to loopback: the
	// guard has to judge the resolved IP, which is what stops DNS rebinding.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	req2, _ := http.NewRequest(http.MethodGet, "https://localhost:"+strconv.Itoa(port), nil)
	if _, err := guardClient().Do(req2); err == nil {
		t.Fatal("the guarded client reached a name resolving to loopback")
	}
}

// A public address must get past the guard: the dial may then fail (nothing
// is listening), but not because the guard refused it. A literal IP needs no
// resolver, so this is deterministic offline.
func TestPublicDialAllowsPublic(t *testing.T) {
	var got string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		got = addr
		return nil, errorf("connection refused for the test")
	}
	_, err := publicDial(context.Background(), dial, "tcp", "93.184.216.34:443")
	if got != "93.184.216.34:443" {
		t.Fatalf("public host not dialed: %q (%v)", got, err)
	}
	if err == nil || strings.Contains(err.Error(), "not public") {
		t.Fatalf("a public host was refused by the guard: %v", err)
	}

	// and the same shape for a private literal is refused before any dial
	got = ""
	if _, err := publicDial(context.Background(), dial, "tcp", "10.0.0.1:443"); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("a private address was not refused: %v", err)
	}
	if got != "" {
		t.Fatalf("dialed a private address anyway: %q", got)
	}
}

// With Clash's TUN (fake-ip) or Surge's enhanced mode on, every name
// resolves into 198.18.0.0/15 and the proxy carries the connection on (#252):
// the dial-time guard has to let that through, while loopback, private,
// link-local and carrier-NAT addresses are still refused before any dial.
func TestPublicDialFakeIP(t *testing.T) {
	var got string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		got = addr
		return nil, errorf("connection refused for the test")
	}
	for _, ip := range []string{"198.18.0.5", "198.19.0.200"} {
		got = ""
		_, err := publicDial(context.Background(), dial, "tcp", net.JoinHostPort(ip, "443"))
		if got != net.JoinHostPort(ip, "443") || err == nil || strings.Contains(err.Error(), "not public") {
			t.Errorf("fake-ip %s: dialed %q, %v", ip, got, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.10", "169.254.169.254", "100.64.0.1"} {
		got = ""
		_, err := publicDial(context.Background(), dial, "tcp", net.JoinHostPort(ip, "443"))
		if err == nil || !strings.Contains(err.Error(), "not public") || got != "" {
			t.Errorf("%s: dialed %q, %v", ip, got, err)
		}
	}
}

// Through a proxy on this computer (Clash's 127.0.0.1:7890, #252) the
// fetch reaches the proxy, which fetches the public site; a site that is
// not public is still refused before the proxy is asked.
func TestGuardClientThroughLocalProxy(t *testing.T) {
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.String())
		rw.Write([]byte("icon"))
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	dt := http.DefaultTransport.(*http.Transport)
	old := dt.Proxy
	dt.Proxy = http.ProxyURL(pu)
	defer func() { dt.Proxy = old }()

	req, _ := http.NewRequest(http.MethodGet, "http://203.0.114.5/favicon.ico", nil)
	resp, err := guardClient().Do(req)
	if err != nil {
		t.Fatalf("a public site through a local proxy was refused: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "icon" || len(asked) != 1 || asked[0] != "http://203.0.114.5/favicon.ico" {
		t.Fatalf("got %q, proxy asked %v", body, asked)
	}

	for _, u := range []string{"http://10.1.2.3/x.png", "http://192.168.1.10/x.png"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if _, err := guardClient().Do(req); err == nil || !strings.Contains(err.Error(), "not public") {
			t.Errorf("%s through the proxy: %v", u, err)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("the proxy was asked for a private site: %v", asked)
	}
}
