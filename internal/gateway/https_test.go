package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func setHTTPS(t *testing.T, on bool) {
	t.Helper()
	if err := settings.SetHTTPS(on); err != nil {
		t.Fatal(err)
	}
	if HTTPSOn() != on {
		t.Fatalf("HTTPSOn() = %v after SetHTTPS(%v)", !on, on)
	}
}

// trusting is an HTTPS client that trusts only roots' certificates.
func trusting(t *testing.T, roots ...*x509.Certificate) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, c := range roots {
		pool.AddCert(c)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: 5 * time.Second, Transport: tr}
}

func magpieCA(t *testing.T) *x509.Certificate {
	t.Helper()
	b, fp, err := CA()
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatalf("CA() isn't PEM: %q", b)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if fp != Fingerprint(c.Raw) || len(fp) != 32*3-1 {
		t.Fatalf("fingerprint %q", fp)
	}
	return c
}

// zhangly1 on X: Claude on another computer on the LAN needed magpie at
// https://. With HTTPS on, another computer is served TLS with a
// certificate magpie's CA signs, which a client trusting that CA alone
// verifies, plain http:// from it is refused, and the switch takes
// effect at the next connection; off, it is plain http:// again.
func TestHTTPSOtherComputer(t *testing.T) {
	key := mtlsHome(t, false)
	addr := serveMTLSGateway(t, true, "")
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("off: %d %q %v", code, body, err)
	}
	setHTTPS(t, true)
	if !TLSOn() || Scheme() != "https" {
		t.Fatalf("TLSOn %v, Scheme %q with HTTPS on", TLSOn(), Scheme())
	}
	c := trusting(t, magpieCA(t))
	for _, rt := range mtlsRoutes {
		code, body, err := mcall(t, c, rt[0], "https://"+addr+rt[1], key)
		if err != nil || code != 200 || body != "served "+rt[1] {
			t.Errorf("on, %s %s: %d %q %v", rt[0], rt[1], code, body, err)
		}
	}
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err == nil && code < 400 {
		t.Errorf("on, plain from another computer: %d %q, want it refused", code, body)
	}
	if _, _, err := mcall(t, trusting(t), "GET", "https://"+addr+"/v1/models", key); err == nil {
		t.Error("a client that doesn't trust magpie's CA verified its certificate")
	}
	info, err := ServedCert()
	if err != nil || info.From != "magpie" || info.Issuer == "" || !info.Until.After(time.Now()) {
		t.Errorf("ServedCert: %+v %v", info, err)
	}
	setHTTPS(t, false)
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("off again: %d %q %v", code, body, err)
	}
}

// This computer's own agents keep plain http:// with HTTPS on.
func TestHTTPSThisComputerPlain(t *testing.T) {
	key := mtlsHome(t, false)
	setHTTPS(t, true)
	addr := serveMTLSGateway(t, false, "")
	if code, body, err := mcall(t, plain, "GET", "http://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("this computer, plain: %d %q %v", code, body, err)
	}
	if code, body, err := mcall(t, trusting(t, magpieCA(t)), "GET", "https://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("this computer, https: %d %q %v", code, body, err)
	}
}

// testPKI is a CA of the test's own and a server certificate it signs
// for 127.0.0.1, valid until notAfter.
type testPKI struct {
	ca                 *x509.Certificate
	caPEM, cert, key   []byte
	otherKey, encKey   []byte
	expiredC, expiredK []byte
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	var p testPKI
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	p.ca, _ = x509.ParseCertificate(der)
	p.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	leaf := func(serial int64, until time.Time) ([]byte, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "nas.home"}, DNSNames: []string{"nas.home"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: until.Add(-48 * time.Hour), NotAfter: until, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		d, err := x509.CreateCertificate(rand.Reader, tpl, p.ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kd, _ := x509.MarshalPKCS8PrivateKey(k)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
	}
	p.cert, p.key = leaf(2, time.Now().Add(12*time.Hour))
	_, p.otherKey = leaf(3, time.Now().Add(12*time.Hour))
	p.expiredC, p.expiredK = leaf(4, time.Now().Add(-time.Hour))
	p.encKey = pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	return p
}

// The user's own certificate and key, imported (a chain and a key apart,
// in any order), are served instead of one magpie's CA signs; a pair
// that can't be served is refused and leaves nothing behind; removed,
// magpie's CA signs again.
func TestHTTPSImportedCert(t *testing.T) {
	key := mtlsHome(t, false)
	setHTTPS(t, true)
	p := newTestPKI(t)
	for name, files := range map[string][][]byte{
		"no key":          {p.cert},
		"no certificate":  {p.key},
		"a stranger key":  {p.cert, p.otherKey},
		"a passphrase":    {p.cert, p.encKey},
		"run out":         {p.expiredC, p.expiredK},
		"nothing in them": {[]byte("hello")},
	} {
		if err := ImportCert(files...); err == nil {
			t.Errorf("%s: imported", name)
		}
		if c, _ := ImportedFiles(); c != "" {
			t.Fatalf("%s: left %s behind", name, c)
		}
	}
	// the key first, the CA's certificate before the leaf: the leaf the
	// key belongs to goes first
	if err := ImportCert(p.key, append(append([]byte{}, p.caPEM...), p.cert...)); err != nil {
		t.Fatal(err)
	}
	if c, k := ImportedFiles(); c == "" || k == "" {
		t.Fatal("nothing kept")
	} else if fi, err := os.Stat(k); err == nil && fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("key.pem mode %v", fi.Mode().Perm())
	}
	addr := serveMTLSGateway(t, true, "")
	if code, body, err := mcall(t, trusting(t, p.ca), "GET", "https://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("imported: %d %q %v", code, body, err)
	}
	if _, _, err := mcall(t, trusting(t, magpieCA(t)), "GET", "https://"+addr+"/v1/models", key); err == nil {
		t.Error("magpie's CA's certificate was served over the imported one")
	}
	if info, err := ServedCert(); err != nil || info.From != "imported" || info.Issuer != "Test Root" || !strings.Contains(strings.Join(info.Names, ","), "nas.home") {
		t.Errorf("ServedCert: %+v %v", info, err)
	}
	if err := RemoveImportedCert(); err != nil {
		t.Fatal(err)
	}
	if code, body, err := mcall(t, trusting(t, magpieCA(t)), "GET", "https://"+addr+"/v1/models", key); err != nil || code != 200 {
		t.Fatalf("removed: %d %q %v", code, body, err)
	}
}

// Another computer fetches magpie's CA without a key, from an address
// the share lets in, before it trusts magpie's https://; every other
// route still asks for the key. With no CA made yet there is none.
func TestHTTPSCAFile(t *testing.T) {
	mtlsHome(t, false)
	var s Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CAPath, s.caFile)
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "models") })
	h := lanGuard(mux)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.168.1.9:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get(CAPath)
	want, _, _ := CA()
	if w.Code != 200 || w.Body.String() != string(want) || w.Header().Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("CA: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w := get("/v1/models"); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("/v1/models without a key: %d", w.Code)
	}
	if w := get(CAPath + "x"); w.Code < 400 {
		t.Errorf("%sx without a key: %d", CAPath, w.Code)
	}
	if err := os.RemoveAll(filepath.Dir(MTLSStore().CAFile())); err != nil {
		t.Fatal(err)
	}
	if w := get(CAPath); w.Code != http.StatusNotFound {
		t.Errorf("no CA: %d", w.Code)
	}
}
