package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// Zhangzhangi on X: a Remote magpie on a domain of its own with a
// self-signed certificate. Its editor peeks at the certificate, the user
// trusts it (ServerPin), and then its models and its test go through;
// before, both fail on the certificate. A pin of another certificate is
// refused at the handshake, and another server at the same pin too.
func TestServerPinRemoteMagpie(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	srv := selfSignedVendor(t)
	other := freshVendor(t)
	ctx := context.Background()
	if err := Save(Provider{ID: "home", Preset: RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("home")
	if _, err := p.List(ctx); !certError(err) {
		t.Fatalf("not pinned: want a certificate error, got %v", err)
	}
	if rs := p.Test(ctx); len(rs) == 0 || rs[0].OK {
		t.Fatalf("its test not pinned: %+v", rs)
	}

	seen, err := p.PeekServerCert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := netproxy.CertSHA256(srv.Certificate()); seen.SHA256 != want {
		t.Fatalf("peeked %s, the server's is %s", seen.SHA256, want)
	}
	if seen.Checks || seen.Why == "" {
		t.Fatalf("a self-signed certificate checks? %+v", seen)
	}

	p.ServerPin = netproxy.ColonHex(seen.SHA256) // as people copy it
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("home")
	if p.ServerPin != seen.SHA256 {
		t.Fatalf("pin kept as %q", p.ServerPin)
	}
	if ms, err := p.List(ctx); err != nil || len(ms) != 1 {
		t.Fatalf("pinned: %v %v", ms, err)
	}
	if rs := p.Test(ctx); len(rs) == 0 || !rs[0].OK {
		t.Fatalf("its test pinned: %+v", rs)
	}

	// the same pin, another server answering at the provider's address
	moved := *p
	for _, f := range []*string{&moved.Chat, &moved.Responses, &moved.Anthropic, &moved.Gemini, &moved.Catalog, &moved.Decide, &moved.ModelsURL} {
		*f = strings.ReplaceAll(*f, srv.URL, other.URL)
	}
	const other1 = "showed a certificate other than the one trusted"
	if _, err := moved.List(ctx); err == nil || !strings.Contains(err.Error(), other1) {
		t.Fatalf("another certificate at a pinned host: %v", err)
	}

	// a pin of another certificate
	p.ServerPin = netproxy.CertSHA256(other.Certificate())
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("home")
	if _, err := p.List(ctx); err == nil || !strings.Contains(err.Error(), other1) || !strings.Contains(err.Error(), "trust the new one") {
		t.Fatalf("pinned to another certificate: %v", err)
	}

	// taken off: checked as before
	p.ServerPin = ""
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("home")
	if _, err := p.List(ctx); !certError(err) || strings.Contains(err.Error(), other1) {
		t.Fatalf("pin taken off: %v", err)
	}
}

// What Save refuses: a pin that isn't a SHA-256, and one on a preset's
// provider that takes no certificate of its own.
func TestServerPinSaveChecks(t *testing.T) {
	azureHome(t)
	good := strings.Repeat("ab", 32)
	for name, c := range map[string]struct {
		p  Provider
		ok bool
	}{
		"own":              {Provider{ID: "home", Name: "Home", Chat: "https://home.example/v1", Key: "k", Models: []string{"m"}, ServerPin: good}, true},
		"colons":           {Provider{ID: "home2", Name: "Home2", Chat: "https://home2.example/v1", Key: "k", Models: []string{"m"}, ServerPin: "SHA256:" + netproxy.ColonHex(good)}, true},
		"short":            {Provider{ID: "home3", Name: "Home3", Chat: "https://home3.example/v1", Key: "k", Models: []string{"m"}, ServerPin: "abcd"}, false},
		"not hex":          {Provider{ID: "home4", Name: "Home4", Chat: "https://home4.example/v1", Key: "k", Models: []string{"m"}, ServerPin: strings.Repeat("zz", 32)}, false},
		"a preset's":       {Provider{ID: "openrouter", Preset: "openrouter", Key: "k", ServerPin: good}, false},
		"a preset at home": {Provider{ID: "lmstudio", Preset: "lmstudio", Chat: "https://box.lan:1234/v1", ServerPin: good}, true},
	} {
		err := Save(c.p)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
		if c.ok {
			if p, _ := Find(c.p.ID); p == nil || p.ServerPin != good {
				t.Errorf("%s: kept as %+v", name, p)
			}
		}
	}
}

// A preset pointed at a server of the user's own (LM Studio on another
// computer behind https) trusts its certificate by fingerprint too
// (Zhangzhangi on X: 建议对别的自签名域名也生效), with no client
// certificate sent there.
func TestServerPinPresetAtOwnServer(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	srv := selfSignedVendor(t)
	ctx := context.Background()
	if err := Save(Provider{ID: "lmstudio", Preset: "lmstudio", Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("lmstudio")
	if !p.TakesServerPin() || len(p.PinHosts()) != 1 {
		t.Fatalf("a preset at the user's own https host takes no pin: %v", p.PinHosts())
	}
	if _, err := p.List(ctx); !certError(err) {
		t.Fatalf("not pinned: want a certificate error, got %v", err)
	}
	seen, err := p.PeekServerCert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.ServerPin = seen.SHA256
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("lmstudio")
	if ms, err := p.List(ctx); err != nil || len(ms) != 1 {
		t.Fatalf("pinned preset: %v %v", ms, err)
	}
	// a client certificate stays one for a provider that takes it
	p.ClientCert, p.ServerCA = "/nowhere/client.pem", "/nowhere/ca.pem"
	if _, err := p.List(ctx); err != nil {
		t.Fatalf("a preset's client certificate was sent: %v", err)
	}
}

// Every preset at the vendor's own addresses, its regions' too, takes no
// pin: a vendor's host is never trusted by a fingerprint.
func TestServerPinNoVendorHost(t *testing.T) {
	for _, pr := range Presets() {
		ps := []Provider{{Preset: pr.ID, Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic, Decide: pr.Decide}}
		for _, r := range pr.Regions {
			ps = append(ps, Provider{Preset: pr.ID, Chat: r.Chat, Responses: r.Responses, Anthropic: r.Anthropic})
		}
		for _, p := range ps {
			if p.IsRemoteMagpie() {
				continue
			}
			if hs := p.PinHosts(); len(hs) > 0 {
				t.Errorf("%s pins its vendor's %v", pr.ID, hs)
			}
		}
	}
	if (Provider{Preset: "openai", Chat: "http://box.lan/v1"}).TakesServerPin() {
		t.Error("an http:// address has no certificate to pin")
	}
	if !(Provider{Preset: "openai", Chat: "https://box.lan/v1"}).TakesServerPin() {
		t.Error("a preset at the user's own https host takes no pin")
	}
}

// A server signed by the provider's own Server CA checks already, so the
// peek offers nothing to pin.
func TestServerPinPeekTakesItsCA(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	srv := selfSignedVendor(t)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "home", Preset: RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL}
	seen, err := p.PeekServerCert(context.Background())
	if err != nil || seen.Checks {
		t.Fatalf("without its CA: %+v %v", seen, err)
	}
	p.ServerCA = ca
	if seen, err = p.PeekServerCert(context.Background()); err != nil || !seen.Checks || seen.Why != "" {
		t.Fatalf("with its CA: %+v %v", seen, err)
	}
}

// freshCert is a self-signed certificate of its own for 127.0.0.1:
// httptest's servers all show one built-in certificate, so a server that
// must show another needs this.
func freshCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "api.magpie.local"},
		DNSNames:     []string{"api.magpie.local"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// freshVendor is selfSignedVendor with a certificate of its own.
func freshVendor(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"qwen3-32b"}]}`)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{freshCert(t)}}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
