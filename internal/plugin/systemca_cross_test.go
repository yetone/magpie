package plugin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// crossChain is a server whose chain goes up to a root (root) that
// another root (top) also signed (cross): leaf, then int, then root's
// subject, as USERTrust RSA's is for agent.minimaxi.com. The server sends
// leaf and int, as that one does.
type crossChain struct {
	top, root, cross, int, leaf []byte
	url                         string
}

func newCrossChain(t *testing.T) crossChain {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	serial := int64(0)
	ca := func(name string, skid byte) *x509.Certificate {
		serial++
		return &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			SubjectKeyId: []byte{skid},
		}
	}
	mk := func(tpl, parent *x509.Certificate, pub *ecdsa.PublicKey, priv *ecdsa.PrivateKey) ([]byte, *x509.Certificate) {
		der, err := x509.CreateCertificate(rand.Reader, tpl, parent, pub, priv)
		if err != nil {
			t.Fatal(err)
		}
		x, _ := x509.ParseCertificate(der)
		return der, x
	}
	topKey, rootKey, intKey, leafKey := key(), key(), key(), key()
	var c crossChain
	topTpl := ca("magpie test AAA root", 1)
	var top *x509.Certificate
	c.top, top = mk(topTpl, topTpl, &topKey.PublicKey, topKey)
	rootTpl := ca("magpie test USERTrust root", 2)
	var root *x509.Certificate
	c.root, root = mk(rootTpl, rootTpl, &rootKey.PublicKey, rootKey)
	c.cross, _ = mk(ca("magpie test USERTrust root", 2), top, &rootKey.PublicKey, topKey)
	intTpl := ca("magpie test DV server CA", 3)
	var in *x509.Certificate
	c.int, in = mk(intTpl, root, &intKey.PublicKey, rootKey)
	serial++
	c.leaf, _ = mk(&x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, in, &leafKey.PublicKey, intKey)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "through") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{c.leaf, c.int}, PrivateKey: leafKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c.url = srv.URL
	return c
}

// The roots magpie hands Bun go self-signed first, and a certificate whose
// issuer isn't among them is left out (rootsFirst).
func TestRootsFirstPutsTheSelfSignedCopyAhead(t *testing.T) {
	c := newCrossChain(t)
	name := map[string]string{string(c.top): "top", string(c.root): "root", string(c.cross): "cross", string(c.int): "int"}
	names := func(certs [][]byte) string {
		var s []string
		for _, d := range certs {
			s = append(s, name[string(d)])
		}
		return strings.Join(s, " ")
	}
	for _, tc := range []struct {
		in   [][]byte
		want string
	}{
		{[][]byte{c.cross, c.root}, "root"},
		{[][]byte{c.cross, c.top}, "top cross"},
		{[][]byte{c.cross, c.root, c.top}, "root top cross"},
		{[][]byte{c.cross}, ""},
		{[][]byte{c.int, c.cross, c.top}, "top cross int"},
		{[][]byte{c.int}, ""},
		{[][]byte{c.root, []byte("not a certificate")}, "root "},
	} {
		if got := names(rootsFirst(tc.in)); got != tc.want {
			t.Errorf("rootsFirst(%s) = %q, want %q", names(tc.in), got, tc.want)
		}
	}
}

// youyo.p_10001 (Discord, Windows): every request the MiniMax plugin made
// to agent.minimaxi.com failed "unable to get issuer certificate", while
// the same chain verified outside the plugin host. USERTrust RSA's copy
// cross-signed by AAA Certificate Services was in the user's ROOT store,
// so in the file magpie gives Bun, and Bun took it over the self-signed
// one and found no AAA. Here the ROOT store holds the cross-signed copy
// ahead of the self-signed one; Bun gets through, as it does with the
// copy's issuer beside it, and still refuses with neither.
func TestBunGetsPastACrossSignedRootCopy(t *testing.T) {
	bun := testenv.Bun(t)
	sandbox(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy",
		"NODE_EXTRA_CA_CERTS", "NODE_USE_SYSTEM_CA", "NODE_TLS_REJECT_UNAUTHORIZED", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		t.Setenv(k, "")
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	c := newCrossChain(t)
	old := systemCAFile
	t.Cleanup(func() { systemCAFile = old })
	fetch := `try { const r = await fetch(` + strconv.Quote(c.url) + `); console.log(await r.text()) } catch (e) { console.log("refused", e?.code) }`
	try := func(t *testing.T, store [][]byte, want bool) {
		t.Helper()
		systemCAFile = func() string { return rootsFile(store) }
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"p","private":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := bunCommand(ctx, bun, dir, "-e", fetch).CombinedOutput()
		if got := err == nil && strings.Contains(string(out), "through"); got != want {
			t.Errorf("Bun's fetch got through: %v, want %v (%v)\n%s", got, want, err, out)
		}
	}
	t.Run("the cross-signed copy first", func(t *testing.T) { try(t, [][]byte{c.cross, c.root}, true) })
	t.Run("with its issuer", func(t *testing.T) { try(t, [][]byte{c.cross, c.top}, true) })
	t.Run("the self-signed one alone", func(t *testing.T) { try(t, [][]byte{c.root}, true) })
	t.Run("only the cross-signed copy", func(t *testing.T) { try(t, [][]byte{c.cross}, false) })
	t.Run("neither", func(t *testing.T) { try(t, [][]byte{c.top}, false) })
}

// A plugin's request whose server's chain Bun refuses says so, with the
// code and the host, after Bun's own words: "unable to get issuer
// certificate" alone read like the vendor's fault (youyo.p_10001).
func TestPluginTLSRefusalSaysWhose(t *testing.T) {
	testenv.Bun(t)
	sandbox(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy",
		"NODE_EXTRA_CA_CERTS", "NODE_USE_SYSTEM_CA", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		t.Setenv(k, "")
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	c := newCrossChain(t)
	old := systemCAFile
	t.Cleanup(func() { systemCAFile = old })
	// the cross-signed copy alone: Bun's words are the reporter's
	systemCAFile = func() string { return writeCAFile(pemOf(c.cross)) }
	Restart()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	res, err := Fetch(ctx, FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: c.url + "/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
	if err == nil {
		res.Body.Close()
		t.Fatalf("got %d, want the chain refused", res.StatusCode)
	}
	if !strings.Contains(err.Error(), "unable to get issuer certificate (UNABLE_TO_GET_ISSUER_CERT: the plugin host's TLS didn't verify 127.0.0.1's certificate chain against the roots it trusts)") {
		t.Errorf("err = %v, want Bun's words, then the code and whose chain", err)
	}
}

func pemOf(ders ...[]byte) []byte {
	var b []byte
	for _, d := range ders {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return b
}
