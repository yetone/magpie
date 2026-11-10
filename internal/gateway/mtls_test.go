package gateway

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

// mtlsHome is a sandbox HOME with magpie shared, a gateway key, and mTLS
// on or off; it returns the key.
func mtlsHome(t *testing.T, on bool) string {
	t.Helper()
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	t.Setenv(AllowIPsEnv, "")
	t.Setenv(TLSCertEnv, "")
	t.Setenv(TLSKeyEnv, "")
	key, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := MTLSStore().CA(); err != nil {
		t.Fatal(err)
	}
	setMTLS(t, on)
	return key
}

func setMTLS(t *testing.T, on bool) {
	t.Helper()
	if err := settings.SetMTLS(on); err != nil {
		t.Fatal(err)
	}
	if MTLSOn() != on {
		t.Fatalf("MTLSOn() = %v after SetMTLS(%v)", !on, on)
	}
}

// issued is a client certificate of st's for name, as a TLS client
// shows it.
func issued(t *testing.T, st mtls.Store, name string) tls.Certificate {
	t.Helper()
	iss, err := st.Issue(name)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tls.X509KeyPair(iss.Cert, iss.Key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// mtlsClient trusts magpie's CA for the server and shows cert (none when
// nil); keepAlive keeps one connection for every request.
func mtlsClient(t *testing.T, cert *tls.Certificate, keepAlive bool) *http.Client {
	t.Helper()
	pool, err := MTLSStore().Pool()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{RootCAs: pool}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	tr := &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: !keepAlive}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: 5 * time.Second, Transport: tr}
}

// asFrom has every request look, to the guards, as if it came from addr:
// the listener judges the connection (fromThisComputer), the guards the
// request, as a WSL distro's or a connection made before mTLS was turned
// on is let through the first and judged by the second.
func asFrom(addr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = addr
		next.ServeHTTP(w, r)
	})
}

// serveMTLSGateway serves the guard over a stub on a listener that sees
// its peers as another computer's (other) or this one's, each request as
// from remote ("" keeps the real loopback peer).
func serveMTLSGateway(t *testing.T, other bool, remote string) string {
	t.Helper()
	was := fromThisComputer
	fromThisComputer = func(net.Conn) bool { return !other }
	t.Cleanup(func() { fromThisComputer = was })
	srvTLS, err := serverTLS()
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.tls = srvTLS
	ln, err := s.listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler = lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "served "+r.URL.Path)
	}))
	if remote != "" {
		h = asFrom(remote, h)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close(); <-done })
	return ln.Addr().String()
}

func mcall(t *testing.T, c *http.Client, method, url, key string, header ...string) (int, string, error) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), nil
}

// the routes a remote agent calls, each refused the same without a
// certificate (a restriction covers every route that reaches the thing)
var mtlsRoutes = [][2]string{
	{"GET", "/v1/models"},
	{"POST", "/v1/chat/completions"},
	{"POST", "/v1/messages"},
	{"POST", "/v1/responses"},
	{"GET", "/v1/magpie/quotas"},
	{"POST", "/backend-api/codex/responses"},
	{"GET", "/api/state"},
	{"GET", "/"},
}

// Another computer's connection (#5596, hhdaxia on X): with mTLS on, the
// handshake ends without a client certificate magpie issued — none, one
// another CA signed, one revoked, or plain HTTP — before any HTTP is
// read; one with a good certificate and the gateway key is answered.
func TestMTLSOtherComputerHandshake(t *testing.T) {
	key := mtlsHome(t, true)
	st := MTLSStore()
	addr := serveMTLSGateway(t, true, "")
	good := issued(t, st, "laptop")
	foreign := issued(t, mtls.Store{Dir: t.TempDir()}, "laptop")
	revoked := issued(t, st, "old phone")
	if _, err := st.Revoke("old phone"); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]*http.Client{
		"no certificate": mtlsClient(t, nil, false),
		"another CA's":   mtlsClient(t, &foreign, false),
		"a revoked one":  mtlsClient(t, &revoked, false),
		"plain HTTP":     plain,
	} {
		scheme := "https://"
		if strings.HasPrefix(name, "plain") {
			scheme = "http://"
		}
		code, body, err := mcall(t, c, "GET", scheme+addr+"/v1/models", key)
		if err == nil && (code < 400 || strings.Contains(body, "served")) {
			t.Errorf("%s: %d %s, want the connection refused", name, code, body)
		}
	}
	c := mtlsClient(t, &good, false)
	for _, rt := range mtlsRoutes {
		code, body, err := mcall(t, c, rt[0], "https://"+addr+rt[1], key)
		if err != nil || code != 200 || body != "served "+rt[1] {
			t.Errorf("good certificate, %s %s: %d %q %v", rt[0], rt[1], code, body, err)
		}
	}
}

// The guards judge each request: one from another machine that reached
// the gateway through a lenient listener (a WSL network's, a connection
// made before mTLS was turned on) is refused on every route without a
// good certificate, the gateway key notwithstanding; with one it still
// needs the key.
func TestMTLSGuardEveryRoute(t *testing.T) {
	key := mtlsHome(t, true)
	st := MTLSStore()
	addr := serveMTLSGateway(t, false, "192.168.1.9:5000")
	good := issued(t, st, "laptop")
	foreign := issued(t, mtls.Store{Dir: t.TempDir()}, "x")

	for _, rt := range mtlsRoutes {
		for name, c := range map[string]*http.Client{
			"plain HTTP":     plain,
			"no certificate": mtlsClient(t, nil, false),
		} {
			scheme := "https://"
			if c == plain {
				scheme = "http://"
			}
			code, body, err := mcall(t, c, rt[0], scheme+addr+rt[1], key)
			if err != nil || code != http.StatusForbidden || !strings.Contains(body, "client certificate") {
				t.Errorf("%s %s, %s: %d %q %v, want 403 saying why", rt[0], rt[1], name, code, body, err)
			}
		}
		// another CA's: the lenient handshake verifies a certificate if
		// given, and refuses one that doesn't verify
		if code, body, err := mcall(t, mtlsClient(t, &foreign, false), rt[0], "https://"+addr+rt[1], key); err == nil && (code != http.StatusForbidden || strings.Contains(body, "served")) {
			t.Errorf("%s %s, another CA's: %d %q", rt[0], rt[1], code, body)
		}
		c := mtlsClient(t, &good, false)
		if code, body, err := mcall(t, c, rt[0], "https://"+addr+rt[1], key); err != nil || code != 200 || body != "served "+rt[1] {
			t.Errorf("%s %s, good certificate and key: %d %q %v", rt[0], rt[1], code, body, err)
		}
	}
	if code, body, _ := mcall(t, mtlsClient(t, &good, false), "POST", "https://"+addr+"/v1/messages", ""); code != http.StatusUnauthorized && code != http.StatusForbidden || strings.Contains(body, "served") {
		t.Errorf("good certificate without the key: %d %q, want it refused", code, body)
	}
}

// This computer is answered as before, over plain HTTP and without a
// key; a request through a proxy or tunnel on loopback is another
// machine's, and is refused without a certificate even with the key,
// unless MAGPIE_TRUST_PROXY says the proxy checks its clients.
func TestMTLSThisComputerAndProxies(t *testing.T) {
	key := mtlsHome(t, true)
	addr := serveMTLSGateway(t, false, "")
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", ""); err != nil || code != 200 {
		t.Fatalf("loopback, plain: %d %q %v", code, body, err)
	}
	if code, body, err := mcall(t, mtlsClient(t, nil, false), "GET", "https://"+addr+"/v1/models", ""); err != nil || code != 200 {
		t.Fatalf("loopback, TLS without a certificate: %d %q %v", code, body, err)
	}
	for _, h := range []string{"X-Forwarded-For", "Cf-Connecting-IP", "Forwarded"} {
		code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key, h, "203.0.113.5")
		if err != nil || code != http.StatusForbidden || !strings.Contains(body, "proxy") {
			t.Errorf("proxied (%s) with key: %d %q %v", h, code, body, err)
		}
	}
	code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key, PagePortHeader, "1")
	if err != nil || code != http.StatusForbidden || !strings.Contains(body, "client certificate") {
		t.Errorf("magpie web's page port with key: %d %q %v", code, body, err)
	}
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", "", "X-Forwarded-For", "203.0.113.5"); err != nil || code != 200 {
		t.Errorf("a trusted proxy: %d %q %v", code, body, err)
	}
}

// Turning mTLS on or off takes effect at the next connection, without
// listening again: off, another computer is answered over plain HTTP with
// its key, as before; on, plain HTTP is refused and a certificate is
// asked for; off again, plain HTTP is answered.
func TestMTLSToggleWithoutRelisten(t *testing.T) {
	key := mtlsHome(t, false)
	addr := serveMTLSGateway(t, true, "")
	good := issued(t, MTLSStore(), "laptop")
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("off: %d %q %v", code, body, err)
	}
	setMTLS(t, true)
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err == nil && code < 400 {
		t.Fatalf("on, plain: %d %q, want it refused", code, body)
	}
	if code, body, err := mcall(t, mtlsClient(t, &good, false), "GET", "https://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("on, certificate: %d %q %v", code, body, err)
	}
	setMTLS(t, false)
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("off again: %d %q %v", code, body, err)
	}
}

// A certificate revoked while its connection stays open is refused at
// the connection's next request, not only its next handshake.
func TestMTLSRevokeMidConnection(t *testing.T) {
	key := mtlsHome(t, true)
	st := MTLSStore()
	addr := serveMTLSGateway(t, false, "192.168.1.9:5000")
	good := issued(t, st, "laptop")
	pool, _ := st.Pool()
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{good}, ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	ask := func() (int, string) {
		t.Helper()
		io.WriteString(conn, "GET /v1/models HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer "+key+"\r\n\r\n")
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(b)
	}
	if code, body := ask(); code != 200 {
		t.Fatalf("before: %d %q", code, body)
	}
	if _, err := st.Revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	if code, body := ask(); code != http.StatusForbidden || !strings.Contains(body, "revoked") {
		t.Fatalf("after revoke, same connection: %d %q", code, body)
	}
}

// magpie web's page refuses another machine's request without a good
// certificate (ClientCertGuard), and serves this computer's.
func TestClientCertGuardPage(t *testing.T) {
	mtlsHome(t, true)
	good := issued(t, MTLSStore(), "laptop")
	leaf, _ := x509.ParseCertificate(good.Certificate[0])
	h := ClientCertGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "page") }))
	call := func(from string, cs *tls.ConnectionState) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr, r.TLS = from, cs
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	verified := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	unverified := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	for _, c := range []struct {
		from string
		cs   *tls.ConnectionState
		want int
	}{
		{"127.0.0.1:5000", nil, 200},
		{"192.168.1.9:5000", nil, 403},
		{"192.168.1.9:5000", &tls.ConnectionState{}, 403},
		{"192.168.1.9:5000", unverified, 403},
		{"192.168.1.9:5000", verified, 200},
	} {
		if got := call(c.from, c.cs); got != c.want {
			t.Errorf("%s %v: %d, want %d", c.from, c.cs != nil, got, c.want)
		}
	}
	setMTLS(t, false)
	if got := call("192.168.1.9:5000", nil); got != 200 {
		t.Errorf("mTLS off: %d", got)
	}
}
