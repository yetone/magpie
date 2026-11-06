package plugin

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// #5508: Bun follows a redirect anywhere and drops only Authorization and
// Cookie on a hop to another origin, so a plugin's x-api-key,
// x-goog-api-key and a vendor's own headers went wherever the vendor's
// Location named. The host follows a redirect with them only to the
// request's own origin (provider.KeepsCredentials' rule), and gives any
// other back to the plugin as it came. A request that carries nothing of
// its own (the usage hook's bare GET here) still follows one anywhere.
func TestPluginRedirectKeepsTheKeyAtItsOrigin(t *testing.T) {
	sandbox(t)
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy", "NO_PROXY", "no_proxy", "FAKE_H2"} {
		t.Setenv(k, "")
	}
	var mu sync.Mutex
	var stolen []string // what reached the other origin: path and its headers
	thief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stolen = append(stolen, fmt.Sprintf("%s %s auth=%q key=%q goog=%q own=%q cookie=%q", r.Method, r.URL.Path,
			r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("X-Goog-Api-Key"), r.Header.Get("X-Own"), r.Header.Get("Cookie")))
		mu.Unlock()
		if r.URL.Path == "/plan" {
			fmt.Fprint(w, "cdn")
			return
		}
		fmt.Fprint(w, `{"stolen":true}`)
	}))
	defer thief.Close()
	var landed []http.Header
	vendorMux := http.NewServeMux()
	vendorMux.HandleFunc("/v1/away307", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, thief.URL+"/x", http.StatusTemporaryRedirect)
	})
	vendorMux.HandleFunc("/v1/away302", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, thief.URL+"/x", http.StatusFound)
	})
	vendorMux.HandleFunc("/v1/here", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/landed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	vendorMux.HandleFunc("/v1/see", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/landed")
		w.WriteHeader(http.StatusSeeOther)
	})
	vendorMux.HandleFunc("/v1/landed", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		landed = append(landed, r.Header.Clone())
		mu.Unlock()
		fmt.Fprintf(w, "landed %s %s", r.Method, b)
	})
	vendorMux.HandleFunc("/v1/loop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/loop")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	vendorMux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, thief.URL+"/plan", http.StatusFound)
	})
	vendor := httptest.NewServer(vendorMux)
	defer vendor.Close()

	// an https vendor whose redirect is to http on its own host and port
	cert, caPEM := certFor(t, net.ParseIP("127.0.0.1"))
	secure := httptest.NewUnstartedServer(nil)
	secure.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+secure.Listener.Addr().String()+"/x")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	defer secure.Close()
	ca := filepath.Join(t.TempDir(), "redirect-ca.pem")
	if err := os.WriteFile(ca, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_EXTRA_CA_CERTS", ca)
	t.Setenv("FAKE_BASE", vendor.URL+"/v1")
	t.Setenv("FAKE_USAGE", vendor.URL+"/usage")

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
	keys := map[string]string{"x-api-key": "sk-ant", "x-goog-api-key": "AIza", "x-own": "relay-secret", "cookie": "session=s1", "content-type": "application/json"}
	fetch := func(url string) (*http.Response, string, error) {
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res, err := Fetch(fctx, FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: url, Method: "POST", Headers: keys, Body: []byte(`{}`)})
		if err != nil {
			return nil, "", err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b), nil
	}
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), stolen...)
	}

	// elsewhere: the 3xx is the answer, and nothing goes there
	for path, code := range map[string]int{"/v1/away307": 307, "/v1/away302": 302} {
		res, body, err := fetch(vendor.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.StatusCode != code || res.Header.Get("Location") != thief.URL+"/x" {
			t.Errorf("%s: %d, Location %q, %q; want the vendor's %d to %s/x as it came", path, res.StatusCode, res.Header.Get("Location"), body, code, thief.URL)
		}
	}
	if s := took(); len(s) > 0 {
		t.Fatalf("a redirect to another origin was followed with the plugin's request: %q", s)
	}

	// https to http on the same host and port: not followed either
	res, body, err := fetch("https://" + secure.Listener.Addr().String() + "/v1/plain")
	if err != nil {
		t.Fatalf("https vendor: %v", err)
	}
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("an https vendor's redirect to http was followed: %d %q", res.StatusCode, body)
	}

	// its own origin: followed with every key, the body sent again
	res, body, err = fetch(vendor.URL + "/v1/here")
	if err != nil || res.StatusCode != 200 || body != "landed POST {}" {
		t.Fatalf("same-origin 307: %v, %v, %q; want it followed with its body", err, res, body)
	}
	mu.Lock()
	h := landed[len(landed)-1]
	mu.Unlock()
	for k, v := range keys {
		if h.Get(k) != v {
			t.Errorf("same-origin redirect: %s = %q, want %q", k, h.Get(k), v)
		}
	}
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		t.Errorf("same-origin redirect: Authorization %q, want the plugin's", h.Get("Authorization"))
	}
	// a 303 is followed as a GET, its body and the body's headers left
	res, body, err = fetch(vendor.URL + "/v1/see")
	if err != nil || res.StatusCode != 200 || body != "landed GET " {
		t.Fatalf("same-origin 303: %v, %v, %q; want a GET", err, res, body)
	}
	mu.Lock()
	h = landed[len(landed)-1]
	mu.Unlock()
	if h.Get("Content-Type") != "" || h.Get("X-Api-Key") != "sk-ant" {
		t.Errorf("303: Content-Type %q, X-Api-Key %q; want the body's headers dropped and the key kept", h.Get("Content-Type"), h.Get("X-Api-Key"))
	}
	// at most 10
	if _, _, err := fetch(vendor.URL + "/v1/loop"); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("a redirect loop: %v, want it stopped after 10", err)
	}
	if s := took(); len(s) > 0 {
		t.Fatalf("the thief got %q", s)
	}

	// a bare request carries nothing of the plugin's: a CDN's redirect is followed
	u, err := AccountUsage(ctx, "fakeco", "")
	if err != nil || u.Plan != "cdn" {
		t.Fatalf("a bare GET's redirect elsewhere: %+v, %v; want it followed", u, err)
	}
	if s := took(); len(s) != 1 || !strings.HasPrefix(s[0], `GET /plan auth="" key="" goog="" own="" cookie=""`) {
		t.Errorf("the bare GET's hop: %q", s)
	}
}
