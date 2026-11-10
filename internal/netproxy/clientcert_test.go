package netproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mtls"
)

// mtlsServer is an HTTPS server with a certificate st's CA signs, that
// asks for a client certificate st's CA issued (require) or takes one if
// given, and answers with the name on the one shown.
func mtlsServer(t *testing.T, st mtls.Store, require bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	cert, err := st.ServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.Pool()
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		h = func(w http.ResponseWriter, r *http.Request) {
			name := "none"
			if len(r.TLS.PeerCertificates) > 0 {
				name = r.TLS.PeerCertificates[0].Subject.CommonName
			}
			io.WriteString(w, name)
		}
	}
	s := httptest.NewUnstartedServer(h)
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven}
	if require {
		s.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// device issues a certificate for name and writes the three files a
// device is given, returning their paths.
func device(t *testing.T, st mtls.Store, name string) (cert, key, ca string) {
	t.Helper()
	iss, err := st.Issue(name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := mtls.Export(iss, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(d, "client.pem"), filepath.Join(d, "client-key.pem"), filepath.Join(d, "ca.pem")
}

func body(t *testing.T, c *http.Client, ctx context.Context, u string) (string, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return string(b), err
}

// A provider's client certificate and server CA (#5596) reach its own
// host alone: the host it names is reached with them, while the same
// request without them, one to another host signed by the same CA, a
// redirect there, and one made for another provider (With) are not shown
// the certificate nor take the CA.
func TestClientCertOnlyItsHost(t *testing.T) {
	st := mtls.Store{Dir: t.TempDir()}
	other := mtlsServer(t, st, false, nil)
	own := mtlsServer(t, st, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			http.Redirect(w, r, other.URL+"/", http.StatusFound)
			return
		}
		io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	})
	cert, key, ca := device(t, st, "laptop")
	d := Dispatch(&http.Transport{Proxy: Func, ForceAttemptHTTP2: true}).(*dispatch)
	t.Cleanup(d.CloseIdleConnections)
	c := &http.Client{Transport: d}
	bg := context.Background()
	mine := WithClientCert(bg, cert, key, ca, own.URL+"/v1")

	if _, err := body(t, c, bg, own.URL); !certRefused(err) {
		t.Fatalf("without them: want a certificate error, got %v", err)
	}
	if got, err := body(t, c, mine, own.URL+"/v1/models"); err != nil || got != "laptop" {
		t.Fatalf("with them: %q, %v", got, err)
	}
	if _, err := body(t, c, mine, other.URL); !certRefused(err) {
		t.Fatalf("another host: want a certificate error, got %v", err)
	}
	if _, err := body(t, c, mine, own.URL+"/elsewhere"); !certRefused(err) {
		t.Fatalf("a redirect to another host: want a certificate error, got %v", err)
	}
	if _, err := body(t, c, With(mine, ""), own.URL); !certRefused(err) {
		t.Fatalf("a request made for another provider: want a certificate error, got %v", err)
	}
	if _, err := body(t, c, WithClientCert(bg, cert, key, ca, "http://"+strings.TrimPrefix(own.URL, "https://")), own.URL); !certRefused(err) {
		t.Fatalf("an http:// base: want a certificate error, got %v", err)
	}
}

// A server CA alone (a server that takes no client certificate, signed by
// a CA of the user's) is trusted beside the system's roots, and no
// certificate is shown; a server that asks for one is refused without.
func TestServerCAAlone(t *testing.T) {
	st := mtls.Store{Dir: t.TempDir()}
	lenient := mtlsServer(t, st, false, nil)
	strict := mtlsServer(t, st, true, nil)
	_, _, ca := device(t, st, "x")
	d := Dispatch(&http.Transport{Proxy: Func}).(*dispatch)
	t.Cleanup(d.CloseIdleConnections)
	c := &http.Client{Transport: d}
	ctx := WithClientCert(context.Background(), "", "", ca, lenient.URL, strict.URL)
	if got, err := body(t, c, ctx, lenient.URL); err != nil || got != "none" {
		t.Fatalf("CA alone: %q, %v", got, err)
	}
	if _, err := body(t, c, ctx, strict.URL); err == nil {
		t.Fatal("a server asking for a certificate answered without one")
	}
}

// A certificate renewed in place (the same paths) is shown from the next
// request: the route names each file's change.
func TestClientCertRenewedInPlace(t *testing.T) {
	st := mtls.Store{Dir: t.TempDir()}
	s := mtlsServer(t, st, true, nil)
	cert, key, ca := device(t, st, "old")
	d := Dispatch(&http.Transport{Proxy: Func, DisableKeepAlives: true}).(*dispatch)
	c := &http.Client{Transport: d}
	ctx := WithClientCert(context.Background(), cert, key, ca, s.URL)
	if got, _ := body(t, c, ctx, s.URL); got != "old" {
		t.Fatalf("got %q", got)
	}
	c2, k2, _ := device(t, st, "new")
	for from, to := range map[string]string{c2: cert, k2: key} {
		b, _ := os.ReadFile(from)
		if err := os.WriteFile(to, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := body(t, c, ctx, s.URL); got != "new" {
		t.Fatalf("after renewal: %q, %v", got, err)
	}
}

// An https:// proxy would be shown the certificate (its TLS is dialled
// with the same config), so a request with one is refused there; an
// http:// proxy is used as it is.
func TestClientCertRefusesHTTPSProxy(t *testing.T) {
	for scheme, refused := range map[string]bool{"https": true, "http": false} {
		tr := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return &url.URL{Scheme: scheme, Host: "proxy:8080"}, nil }}
		clientTLS(tr, "c.pem\x00k.pem\x00\x00")
		u, err := tr.Proxy(httptest.NewRequest(http.MethodGet, "https://h/", nil))
		if refused != errors.Is(err, ErrClientCertOverHTTPSProxy) || (!refused && u == nil) {
			t.Errorf("%s proxy: %v, %v", scheme, u, err)
		}
	}
}

func TestCheckClientCert(t *testing.T) {
	st := mtls.Store{Dir: t.TempDir()}
	cert, key, ca := device(t, st, "x")
	_, otherKey, _ := device(t, st, "y")
	junk := filepath.Join(t.TempDir(), "junk.pem")
	os.WriteFile(junk, []byte("not pem"), 0o600)
	for _, c := range []struct {
		cert, key, ca string
		ok            bool
	}{
		{"", "", "", true},
		{cert, key, ca, true},
		{cert, key, "", true},
		{"", "", ca, true},
		{cert, "", "", false},
		{"", key, "", false},
		{cert, otherKey, "", false},
		{cert, key, junk, false},
		{cert, key, filepath.Join(t.TempDir(), "gone.pem"), false},
	} {
		if err := CheckClientCert(c.cert, c.key, c.ca); (err == nil) != c.ok {
			t.Errorf("%q %q %q: %v", filepath.Base(c.cert), filepath.Base(c.key), filepath.Base(c.ca), err)
		}
	}
}
