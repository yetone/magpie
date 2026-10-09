package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"testing"
	"time"
)

func TestM365CertificateCoversOnlyLoopbackAndKeepsNoCAKey(t *testing.T) {
	dir := t.TempDir()
	f := m365CertificateFiles{caCert: filepath.Join(dir, "ca.pem"), serverCert: filepath.Join(dir, "server.pem"), serverKey: filepath.Join(dir, "server-key.pem")}
	if err := ensureM365Certificate(f); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(f.serverCert, f.serverKey)
	if err != nil {
		t.Fatal(err)
	}
	server, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(f.caCert)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("CA PEM was not decoded")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if _, err := server.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if _, err := server.Verify(x509.VerifyOptions{DNSName: "192.168.1.2", Roots: roots}); err == nil {
		t.Fatal("certificate covered a non-loopback address")
	}
	if fi, err := os.Stat(f.serverKey); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("server key permissions: %v %v", fi, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*ca*key*")); len(matches) != 0 {
		t.Fatalf("CA private key was retained: %v", matches)
	}
}

func TestM365HTTPSListenerAndRouteBoundary(t *testing.T) {
	old := m365ListenAddr
	m365ListenAddr = "127.0.0.1:0"
	t.Cleanup(func() { m365ListenAddr = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewM365Server()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.URL.Path) })
	if err := m.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c, stop := context.WithTimeout(context.Background(), time.Second); defer stop(); _ = m.Stop(c) })
	m.mu.Lock()
	addr := m.ln.Addr().String()
	m.mu.Unlock()
	f := m365CertificatePaths()
	caPEM, err := os.ReadFile(f.caCert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/v1/models", 200}, {"POST", "/v1/messages", 200}, {"GET", "/v1/magpie/quotas", 404}, {"GET", "/", 404}} {
		req, _ := http.NewRequest(tc.method, "https://"+addr+tc.path, strings.NewReader(`{}`))
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, res.StatusCode, tc.want)
		}
	}
}

func TestM365PreflightUsesExistingCORSAndKeyPolicy(t *testing.T) {
	fresh(t)
	s := settings.Load()
	s.CORSOrigins = []string{M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	h := m365Only(New().Handler())
	r := httptest.NewRequest(http.MethodOptions, "/v1/messages", nil)
	r.Header.Set("Origin", M365Origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "x-api-key, anthropic-version, content-type")
	r.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != M365Origin || w.Header().Get("Access-Control-Allow-Private-Network") != "true" || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight: %d %#v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestM365HTTPSRunsTheAnthropicRouteWithAKey(t *testing.T) {
	f := &fake{t: t, reply: sse(`event: message_start`, `data: {"type":"message_start","message":{"id":"msg_m365","type":"message","role":"assistant","content":[],"model":"m1","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`, `event: message_stop`, `data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	secret, err := access.Update("add-key", access.Change{Name: "Claude for Microsoft 365"})
	if err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.CORSOrigins = []string{M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	old := m365ListenAddr
	m365ListenAddr = "127.0.0.1:0"
	t.Cleanup(func() { m365ListenAddr = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewM365Server()
	if err := m.Start(ctx, New().Handler()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c, stop := context.WithTimeout(context.Background(), time.Second); defer stop(); _ = m.Stop(c) })
	m.mu.Lock()
	addr := m.ln.Addr().String()
	m.mu.Unlock()
	caPEM, err := os.ReadFile(m365CertificatePaths().caCert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	call := func(key string) (int, string, http.Header) {
		t.Helper()
		body := `{"model":"fake/m1","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/messages", strings.NewReader(body))
		req.Header.Set("Origin", M365Origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
	if code, body, headers := call(secret); code != http.StatusOK || !strings.Contains(body, "msg_m365") || headers.Get("Access-Control-Allow-Origin") != M365Origin {
		t.Fatalf("keyed call: %d %s %#v", code, body, headers)
	}
	if f.path != "/v1/messages" {
		t.Fatalf("provider got %q", f.path)
	}
	if code, body, _ := call(""); code != http.StatusUnauthorized || !strings.Contains(body, "gateway key") {
		t.Fatalf("unkeyed call: %d %s", code, body)
	}
}
