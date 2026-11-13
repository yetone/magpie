package netproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// resolveEnv is a TLS server whose certificate is for api.example.com, at
// 127.0.0.1, and a transport that trusts it and has no DNS: a name its
// dial is given is refused, but for the ones in hosts, which stand for
// the system's resolver (the "direct dial"). Every address the transport
// dialled is kept, so a test can say where a connection was tried.
type resolveEnv struct {
	srv   *httptest.Server
	port  string
	d     http.RoundTripper
	mu    sync.Mutex
	dials []string
	sni   []string
	hosts []string // Host headers the server saw
	// direct is where a name of hosts goes, when the system resolves it
	hosts2 map[string]string
}

func (e *resolveEnv) dialed() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.dials)
}

func newResolveEnv(t *testing.T, system map[string]string) *resolveEnv {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "api.example.com"},
		DNSNames: []string{"api.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	e := &resolveEnv{hosts2: system}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.hosts = append(e.hosts, r.Host)
		e.mu.Unlock()
		io.WriteString(w, r.Proto)
	}))
	e.srv.EnableHTTP2 = true
	e.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	e.srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			e.mu.Lock()
			e.sni = append(e.sni, h.ServerName)
			e.mu.Unlock()
			return nil, nil
		},
	}
	e.srv.StartTLS()
	t.Cleanup(e.srv.Close)
	_, e.port, _ = net.SplitHostPort(e.srv.Listener.Addr().String())
	var dialer net.Dialer
	base := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			e.mu.Lock()
			e.dials = append(e.dials, addr)
			e.mu.Unlock()
			h, p, _ := net.SplitHostPort(addr)
			if ip, ok := system[h]; ok {
				addr = net.JoinHostPort(ip, p)
			} else if net.ParseIP(h) == nil {
				return nil, errors.New("lookup " + h + ": no such host (no DNS in this test)")
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	d := Dispatch(base).(*dispatch)
	t.Cleanup(d.CloseIdleConnections)
	e.d = d
	return e
}

func (e *resolveEnv) url() string { return "https://api.example.com:" + e.port + "/v1" }

func (e *resolveEnv) get(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url(), nil)
	res, err := e.d.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b), nil
}

// deadPort is a loopback port nothing listens on.
func deadPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return p
}

// A rule connects the name to the address given, and only there: the TLS
// server name and the Host header are the URL's, so a server that has a
// certificate for the name takes it. The same request with no rule is
// dialled to the name, which this transport can't resolve.
func TestResolveKeepsNameForSNIAndHost(t *testing.T) {
	e := newResolveEnv(t, nil)
	if _, err := e.get(context.Background()); err == nil {
		t.Fatal("with no rule the name can't be reached here: the test has no DNS")
	}
	ctx := WithResolve(context.Background(), map[string][]string{"api.example.com": {"127.0.0.1"}})
	got, err := e.get(ctx)
	if err != nil {
		t.Fatalf("with the rule: %v", err)
	}
	if got != "HTTP/2.0" {
		t.Errorf("proto %q: the rule's connection should speak HTTP/2 as any other", got)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !slices.Equal(e.sni, []string{"api.example.com"}) {
		t.Errorf("SNI %v, want the URL's name", e.sni)
	}
	if !slices.Equal(e.hosts, []string{"api.example.com:" + e.port}) {
		t.Errorf("Host %v, want the URL's", e.hosts)
	}
	if last := e.dials[len(e.dials)-1]; last != "127.0.0.1:"+e.port {
		t.Errorf("dialled %v: the rule's address with the URL's port is where it connects", e.dials)
	}
}

// A place that names a port connects there, not to the URL's.
func TestResolvePlaceWithPort(t *testing.T) {
	e := newResolveEnv(t, nil)
	rule := map[string][]string{"api.example.com": {net.JoinHostPort("127.0.0.1", e.port)}}
	u := "https://api.example.com:1/v1" // nothing is at port 1
	req, _ := http.NewRequestWithContext(WithResolve(context.Background(), rule), http.MethodGet, u, nil)
	res, err := e.d.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

// "dns:<name>" is looked up when the connection is made, each time one is,
// so a record that moves is followed; its addresses are tried in the order
// the resolver gave them.
func TestResolveDNSPlaceFollowsItsRecords(t *testing.T) {
	e := newResolveEnv(t, nil)
	dead := deadPort(t)
	var mu sync.Mutex
	var asked []string
	answers := [][]string{{"127.0.0.1"}, {"127.0.0.1"}}
	lookupHost = func(ctx context.Context, name string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, name)
		a := answers[min(len(asked), len(answers))-1]
		return a, nil
	}
	t.Cleanup(func() {
		lookupHost = func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, name)
		}
	})
	ctx := WithResolve(context.Background(), map[string][]string{"api.example.com": {"dns:gw.managed.test"}})
	if _, err := e.get(ctx); err != nil {
		t.Fatal(err)
	}
	e.d.(*dispatch).CloseIdleConnections()
	// the name now answers with an address nothing takes first, then the good one
	mu.Lock()
	answers = [][]string{{"127.0.0.1", "127.0.0.1"}}
	mu.Unlock()
	_ = dead
	if _, err := e.get(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{"gw.managed.test", "gw.managed.test"}) {
		t.Errorf("looked up %v: once for each new connection", asked)
	}
}

// The addresses of a rule are tried in order, a dead one handing over to
// the next, a "dns:" name's own answers among them.
func TestResolveOrderedFailover(t *testing.T) {
	e := newResolveEnv(t, nil)
	dead1, dead2 := deadPort(t), deadPort(t)
	lookupHost = func(ctx context.Context, name string) ([]string, error) {
		return []string{net.JoinHostPort("127.0.0.1", dead2)[:9], "127.0.0.1"}, nil
	}
	t.Cleanup(func() {
		lookupHost = func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, name)
		}
	})
	rule := map[string][]string{"api.example.com": {"127.0.0.1:" + dead1, "dns:gw.managed.test:" + dead2, "127.0.0.1"}}
	if _, err := e.get(WithResolve(context.Background(), rule)); err != nil {
		t.Fatal(err)
	}
	// the dns place's two answers are dialled at its own port, the dead one
	want := []string{"127.0.0.1:" + dead1, "127.0.0.1:" + dead2, "127.0.0.1:" + dead2, "127.0.0.1:" + e.port}
	if got := e.dialed(); !slices.Equal(got, want) {
		t.Errorf("dialled %v, want %v", got, want)
	}
}

// When no address of a rule takes the connection, the name is dialled as
// it would have been with no rule, so the network that answers for it
// still does.
func TestResolveFallsBackToDirect(t *testing.T) {
	e := newResolveEnv(t, map[string]string{"api.example.com": "127.0.0.1"})
	rule := map[string][]string{"api.example.com": {"127.0.0.1:" + deadPort(t), "dns:gone.managed.test"}}
	lookupHost = func(ctx context.Context, name string) ([]string, error) { return nil, errors.New("no such host") }
	t.Cleanup(func() {
		lookupHost = func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, name)
		}
	})
	if _, err := e.get(WithResolve(context.Background(), rule)); err != nil {
		t.Fatalf("the direct dial should have answered: %v", err)
	}
	got := e.dialed()
	if len(got) != 2 || got[1] != "api.example.com:"+e.port {
		t.Errorf("dialled %v: the dead address, then the name itself", got)
	}
}

// A host with no rule is dialled as before, and so is one the rule's
// provider no longer owns (With).
func TestResolveOnlyItsHost(t *testing.T) {
	e := newResolveEnv(t, nil)
	rule := map[string][]string{"api.example.com": {"127.0.0.1"}}
	req, _ := http.NewRequest(http.MethodGet, "https://other.example.com:"+e.port+"/", nil)
	if _, err := e.d.RoundTrip(req.WithContext(WithResolve(context.Background(), rule))); err == nil {
		t.Error("another host reached through a rule that isn't its own")
	}
	if got := e.dialed(); !slices.Equal(got, []string{"other.example.com:" + e.port}) {
		t.Errorf("dialled %v", got)
	}
	if _, err := e.get(With(WithResolve(context.Background(), rule), "")); err == nil {
		t.Error("a request made for another provider took this one's rule")
	}
}

// Through a proxy the proxy connects to the name, so a rule has no say:
// the proxy is dialled and asked for the name, never the rule's address.
func TestResolveNotThroughAProxy(t *testing.T) {
	e := newResolveEnv(t, nil)
	var mu sync.Mutex
	var connects []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		connects = append(connects, r.Host)
		mu.Unlock()
		up, err := net.Dial("tcp", e.srv.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		down, _, _ := w.(http.Hijacker).Hijack()
		io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(up, down); up.Close() }()
		io.Copy(down, up)
		down.Close()
	}))
	t.Cleanup(proxy.Close)
	pu, _ := url.Parse(proxy.URL)
	rule := map[string][]string{"api.example.com": {"127.0.0.1:" + deadPort(t)}}
	ctx := With(context.Background(), "")
	ctx = WithResolve(With(ctx, proxy.URL), rule)
	_ = ctx
	ctx = With(context.Background(), proxy.URL)
	ctx = WithResolve(ctx, rule)
	// With(…) drops a rule made before it; the rule is made after it, as Via does
	got, err := e.get(ctx)
	if err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	if got == "" {
		t.Error("no reply")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(connects, []string{"api.example.com:" + e.port}) {
		t.Errorf("the proxy was asked for %v, want the URL's name", connects)
	}
	if d := e.dialed(); !slices.Equal(d, []string{pu.Host}) {
		t.Errorf("dialled %v: only the proxy", d)
	}
}

// A proxy that has the rule's own name is dialled as it always was: the
// rule is for the connection to the host, not to whatever the name stands
// for as a proxy.
func TestResolveLeavesAProxyOfTheSameNameAlone(t *testing.T) {
	e := newResolveEnv(t, map[string]string{"api.example.com": "127.0.0.1"})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up, err := net.Dial("tcp", e.srv.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		down, _, _ := w.(http.Hijacker).Hijack()
		io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(up, down); up.Close() }()
		io.Copy(down, up)
		down.Close()
	}))
	t.Cleanup(proxy.Close)
	_, pp, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	via := "http://api.example.com:" + pp
	rule := map[string][]string{"api.example.com": {"127.0.0.1:" + deadPort(t)}}
	if _, err := e.get(WithResolve(With(context.Background(), via), rule)); err != nil {
		t.Fatal(err)
	}
	if d := e.dialed(); !slices.Equal(d, []string{"api.example.com:" + pp}) {
		t.Errorf("dialled %v: only the proxy, at its name", d)
	}
}

func TestCheckResolve(t *testing.T) {
	good, err := CheckResolve(map[string][]string{" API.Example.com. ": {" dns:gw.managed.test ", "10.0.0.1", "10.0.0.1", "10.0.0.2:8443", "[fd00::1]:8443", "fd00::2"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dns:gw.managed.test", "10.0.0.1", "10.0.0.2:8443", "[fd00::1]:8443", "fd00::2"}
	if !slices.Equal(good["api.example.com"], want) {
		t.Errorf("kept %v, want %v", good, want)
	}
	if got, err := CheckResolve(nil); got != nil || err != nil {
		t.Errorf("none: %v, %v", got, err)
	}
	for name, rules := range map[string]map[string][]string{
		"no place":      {"api.example.com": {}},
		"blank place":   {"api.example.com": {" "}},
		"url as host":   {"https://api.example.com": {"10.0.0.1"}},
		"port as host":  {"api.example.com:443": {"10.0.0.1"}},
		"ip as host":    {"10.0.0.1": {"10.0.0.2"}},
		"name as place": {"api.example.com": {"gateway.corp"}},
		"empty dns":     {"api.example.com": {"dns:"}},
		"bad port":      {"api.example.com": {"10.0.0.1:99999"}},
		"same twice":    {"a.example.com": {"10.0.0.1"}, "A.example.com.": {"10.0.0.2"}},
		"empty host":    {"": {"10.0.0.1"}},
	} {
		if _, err := CheckResolve(rules); err == nil || !strings.HasPrefix(err.Error(), "resolve: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
