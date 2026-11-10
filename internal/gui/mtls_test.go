package gui

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func mtlsMux(t *testing.T, w Windows) func(r *http.Request) (int, settingsJSON, string) {
	t.Helper()
	mux := http.NewServeMux()
	mtlsRoutes(mux, w)
	return func(r *http.Request) (int, settingsJSON, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		var s settingsJSON
		json.Unmarshal(rec.Body.Bytes(), &s)
		return rec.Code, s, rec.Body.String()
	}
}

func mtlsReq(path, body string) *http.Request {
	return httptest.NewRequest("POST", path, strings.NewReader(body))
}

// leafFile reads the certificate an issue saved.
func leafFile(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := pem.Decode(b)
	c, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Settings → Network and sharing (hhdaxia on X, #5596): turning mTLS on
// makes the CA, Issue saves a device's three files to Downloads and lists
// it, Revoke takes it off the list and out of the admitted.
func TestSettingsMTLSIssueRevoke(t *testing.T) {
	h := sandboxHome(t)
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o755)
	post := mtlsMux(t, folderOnly{})

	code, s, out := post(mtlsReq("/api/settings/mtls", `{"on":true}`))
	if code != 200 || !s.MTLS || !settings.Shared().MTLS {
		t.Fatalf("on: %d %s", code, out)
	}
	if _, _, err := gateway.MTLSStore().CA(); err != nil {
		t.Fatal(err)
	}
	code, s, out = post(mtlsReq("/api/settings/mtls/issue", `{"name":"laptop"}`))
	if code != 200 || len(s.MTLSCerts) != 1 || s.MTLSCerts[0].Name != "laptop" || s.MTLSSaved == "" {
		t.Fatalf("issue: %d %s", code, out)
	}
	dir := filepath.Join(h, "Downloads", "magpie-laptop")
	for _, f := range []string{"client.pem", "client-key.pem", "ca.pem"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("not saved: %v", err)
		}
	}
	leaf := leafFile(t, filepath.Join(dir, "client.pem"))
	if err := gateway.MTLSStore().Admits(leaf); err != nil {
		t.Fatalf("issued, not admitted: %v", err)
	}
	if code, _, _ := post(mtlsReq("/api/settings/mtls/issue", `{"name":"a/b"}`)); code == 200 {
		t.Fatal("a name with a slash issued")
	}
	code, s, out = post(mtlsReq("/api/settings/mtls/revoke", `{"serial":"`+s.MTLSCerts[0].Serial+`"}`))
	if code != 200 || len(s.MTLSCerts) != 0 {
		t.Fatalf("revoke: %d %s", code, out)
	}
	if gateway.MTLSStore().Admits(leaf) == nil {
		t.Fatal("revoked, still admitted")
	}
	if code, s, _ = post(mtlsReq("/api/settings/mtls", `{"on":false}`)); code != 200 || s.MTLS || settings.Shared().MTLS {
		t.Fatal("still on")
	}
}

// A page open from another computer never shuts itself out: without a
// certificate it can't turn mTLS on; with one it can't revoke that one
// or issue its name again. The computer magpie runs on can do all three.
func TestSettingsMTLSPageLockout(t *testing.T) {
	h := sandboxHome(t)
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o755)
	app := mtlsMux(t, folderOnly{})
	app(mtlsReq("/api/settings/mtls", `{"on":true}`))
	_, s, _ := app(mtlsReq("/api/settings/mtls/issue", `{"name":"phone"}`))
	app(mtlsReq("/api/settings/mtls", `{"on":false}`))
	if len(s.MTLSCerts) != 1 {
		t.Fatal("not issued")
	}
	serial := s.MTLSCerts[0].Serial
	leaf := leafFile(t, filepath.Join(h, "Downloads", "magpie-phone", "client.pem"))
	web := mtlsMux(t, webHost{})

	far := mtlsReq("/api/settings/mtls", `{"on":true}`)
	far.RemoteAddr = "192.168.1.30:50000"
	if code, _, out := web(far); code == 200 || !strings.Contains(out, "without a client certificate") || settings.Shared().MTLS {
		t.Fatalf("a page from another computer without a certificate turned it on: %d %s", code, out)
	}
	near := mtlsReq("/api/settings/mtls", `{"on":true}`)
	near.RemoteAddr = "127.0.0.1:50000"
	if code, _, out := web(near); code != 200 || !settings.Shared().MTLS {
		t.Fatalf("this computer's page: %d %s", code, out)
	}
	// the page that came with phone's certificate
	withCert := func(path, body string) *http.Request {
		r := mtlsReq(path, body)
		r.RemoteAddr = "192.168.1.30:50000"
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		return r
	}
	if code, _, out := web(withCert("/api/settings/mtls/revoke", `{"serial":"`+strings.ToUpper(serial)+`"}`)); code == 200 || !strings.Contains(out, "this page came with that certificate") {
		t.Fatalf("revoked its own: %d %s", code, out)
	}
	if code, _, out := web(withCert("/api/settings/mtls/issue", `{"name":" PHONE "}`)); code == 200 || !strings.Contains(out, "this page came with that certificate") {
		t.Fatalf("issued its own name again: %d %s", code, out)
	}
	if gateway.MTLSStore().Admits(leaf) != nil {
		t.Fatal("its certificate was revoked")
	}
	if code, _, out := web(withCert("/api/settings/mtls", `{"on":true}`)); code != 200 {
		t.Fatalf("a page with a good certificate: %d %s", code, out)
	}
	if code, _, out := app(mtlsReq("/api/settings/mtls/revoke", `{"serial":"`+serial+`"}`)); code != 200 {
		t.Fatalf("the app's revoke: %d %s", code, out)
	}
}

// The provider editor's certificate paths (#5596): saved typed with ~,
// shown back with it, kept by a Save that leaves them out, and cleared by
// one that sends them empty.
func TestProviderSaveClientCert(t *testing.T) {
	h := sandboxHome(t)
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o755)
	app := mtlsMux(t, folderOnly{})
	if code, _, out := app(mtlsReq("/api/settings/mtls/issue", `{"name":"laptop"}`)); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	base := `{"id":"home","name":"Home","key":"k","chat":"https://home.example:3425/v1","icon":"generic"`
	certs := `"clientCert":"~/Downloads/magpie-laptop/client.pem","clientKey":"~/Downloads/magpie-laptop/client-key.pem","serverCA":"~/Downloads/magpie-laptop/ca.pem"`
	if code, out := post(base + `,"new":true,` + certs + `}`); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	p, _ := provider.Find("home")
	if p.ClientCert != filepath.Join(h, "Downloads", "magpie-laptop", "client.pem") || p.ServerCA == "" {
		t.Fatalf("saved %q %q", p.ClientCert, p.ServerCA)
	}
	info := providerInfo(*p, nil)
	if !info.TakesClientCert || info.ClientCert != "~/Downloads/magpie-laptop/client.pem" {
		t.Fatalf("shown %v %q", info.TakesClientCert, info.ClientCert)
	}
	if code, out := post(base + `,"from":"home","models":[],"headers":{}}`); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if p, _ = provider.Find("home"); p.ClientCert == "" || p.ClientKey == "" {
		t.Fatal("a Save that left them out cleared them")
	}
	if code, out := post(base + `,"from":"home","models":[],"headers":{},"clientCert":"","clientKey":"","serverCA":""}`); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if p, _ = provider.Find("home"); p.ClientCert != "" || p.ServerCA != "" {
		t.Fatal("sent empty, still kept")
	}
	if code, _ := post(base + `,"from":"home","models":[],"headers":{},"clientCert":"~/Downloads/magpie-laptop/client.pem"}`); code == 200 {
		t.Fatal("a certificate without its key saved")
	}
}
