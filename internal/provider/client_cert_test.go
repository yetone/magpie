package provider

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mtls"
)

// mtlsMagpie is a magpie behind mTLS (#5596, clxsh: an Nginx asking for
// a client certificate in front of the magpie at home): its certificate
// is st's CA's, and it answers only a client showing one st issued, with
// the gateway key.
func mtlsMagpie(t *testing.T, st mtls.Store) *httptest.Server {
	t.Helper()
	cert, err := st.ServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.Pool()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer remote-key" && r.Header.Get("x-api-key") != "remote-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"home/model"}]}`)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// Remote magpie with a client certificate, its key and the server's CA
// lists the other magpie's models and passes its connection test; without
// them both fail on the certificate. The paths are kept whole, a ~ taken
// as home.
func TestRemoteMagpieClientCert(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	st := mtls.Store{Dir: t.TempDir()}
	srv := mtlsMagpie(t, st)
	iss, err := st.Issue("office")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	dir, err := mtls.Export(iss, home)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Save(Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("office")
	if _, err := p.List(ctx); err == nil {
		t.Fatal("without a certificate: listed")
	}
	if rs := p.Test(ctx); len(rs) == 0 || rs[0].OK {
		t.Fatalf("its test without a certificate: %+v", rs)
	}
	base := "~/" + filepath.Base(dir) + "/"
	p.ClientCert, p.ClientKey, p.ServerCA = base+"client.pem", base+"client-key.pem", base+"ca.pem"
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("office")
	if p.ClientCert != filepath.Join(dir, "client.pem") || p.ServerCA != filepath.Join(dir, "ca.pem") {
		t.Fatalf("paths kept as %q %q", p.ClientCert, p.ServerCA)
	}
	ms, err := p.List(ctx)
	if err != nil || len(ms) != 1 {
		t.Fatalf("with a certificate: %v %v", ms, err)
	}
	if rs := p.Test(ctx); len(rs) == 0 || !rs[0].OK {
		t.Fatalf("its test with a certificate: %+v", rs)
	}
	// the server's CA alone: the server is trusted, the handshake still
	// refused for want of a certificate
	p.ClientCert, p.ClientKey = "", ""
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("office")
	if _, err := p.List(ctx); err == nil || certError(err) && strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("CA alone: %v, want the handshake refused, not the server", err)
	}
}

// What Save refuses: a certificate for a preset's provider, one without
// its key, a relative path, a pair that doesn't match, a CA file that
// isn't one.
func TestClientCertSaveChecks(t *testing.T) {
	azureHome(t)
	st := mtls.Store{Dir: t.TempDir()}
	a, _ := st.Issue("a")
	b, _ := st.Issue("b")
	da, _ := mtls.Export(a, t.TempDir())
	db, _ := mtls.Export(b, t.TempDir())
	cert, key, ca := filepath.Join(da, "client.pem"), filepath.Join(da, "client-key.pem"), filepath.Join(da, "ca.pem")
	own := func(c, k, s string) Provider {
		return Provider{ID: "home", Name: "Home", Chat: "https://home.example/v1", Key: "k", Models: []string{"m"}, ClientCert: c, ClientKey: k, ServerCA: s}
	}
	for name, c := range map[string]struct {
		p  Provider
		ok bool
	}{
		"all three":     {own(cert, key, ca), true},
		"CA alone":      {own("", "", ca), true},
		"no key":        {own(cert, "", ""), false},
		"key alone":     {own("", key, ""), false},
		"relative":      {own("client.pem", "client-key.pem", ""), false},
		"another's key": {own(cert, filepath.Join(db, "client-key.pem"), ""), false},
		"CA not PEM":    {own("", "", key), false},
		"a preset's":    {Provider{ID: "openrouter", Preset: "openrouter", Key: "k", ClientCert: cert, ClientKey: key}, false},
	} {
		err := Save(c.p)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
