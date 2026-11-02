package gui

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// Wails strips the URL's scheme before calling the window's handler. The
// policy must still name that scheme: WebKit blocks a scheme-less host
// source under wails://, so the bridge never says hello.
func TestPluginPageWindowPolicy(t *testing.T) {
	pagePlugins(t)
	page := pluginPages()[0]
	// Wails' own base URLs (internal/assetserver/assetserver_<os>.go):
	// wails://localhost on macOS and Linux, http://wails.localhost on Windows.
	desktop := "wails://localhost"
	if runtime.GOOS == "windows" {
		desktop = "http://wails.localhost"
	}
	desktopHost := strings.TrimPrefix(strings.TrimPrefix(desktop, "wails://"), "http://")
	for _, c := range []struct {
		name   string
		host   string
		window bool
		source string
	}{
		{"desktop", desktopHost, true, desktop},
		{"web", "127.0.0.1:3437", false, "https://127.0.0.1:3437"},
		{"web localhost", "localhost", false, "https://localhost"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var handler http.Handler = pageMux()
			if c.window {
				handler = ownWindow(handler)
			}
			for _, file := range []string{"index.html", pluginBridgePath, "app.js", "style.css", "icon.svg"} {
				url := pluginPagePrefix + page.token + "/" + file
				r := httptest.NewRequest("GET", url, nil)
				r.Host = c.host
				// A web request cannot opt into the native policy by naming
				// the window's host or sending Wails' headers.
				r.Header.Set("X-Wails-Window-Id", "1")
				w := do(handler, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: HTTP %d", file, w.Code)
				}
				policy := w.Header().Get("Content-Security-Policy")
				source := c.source + pluginPagePrefix + page.token + "/"
				for _, directive := range []string{"script-src", "style-src", "img-src", "font-src", "media-src"} {
					if !strings.Contains(policy, directive+" "+source) {
						t.Errorf("%s: %s cannot load the page's own files: %s", file, directive, policy)
					}
				}
				for _, directive := range []string{"sandbox allow-scripts", "connect-src 'none'", "frame-src 'none'", "form-action 'none'"} {
					if !strings.Contains(policy, directive) {
						t.Errorf("%s: missing %s", file, directive)
					}
				}
			}
		})
	}
}

// A browser's page (magpie web) names its folder with a scheme: WebKit
// takes a scheme-less source's scheme from the sandboxed frame's opaque
// origin and matched nothing, so iOS Safari loaded no script (#4198).
// Served over http, or https with TLS ended by a proxy in front, both
// schemes are named, on the Host the browser asked for; what the proxy
// says in its forwarding headers changes nothing.
func TestPluginPageWebPolicyNamesItsScheme(t *testing.T) {
	pagePlugins(t)
	page := pluginPages()[0]
	for _, c := range []struct {
		name, host string
		hdr        map[string]string
	}{
		{"http", "192.168.1.5:3430", nil},
		{"https behind nginx", "magpie.test.xyz", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "1.2.3.4"}},
		{"https behind Cloudflare", "magpie.test.xyz", map[string]string{"X-Forwarded-Proto": "https", "Cf-Visitor": `{"scheme":"https"}`, "X-Forwarded-Host": "evil.example"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", pluginPagePrefix+page.token+"/index.html", nil)
			r.Host = c.host
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			w := do(pageMux(), r)
			if w.Code != http.StatusOK {
				t.Fatalf("HTTP %d", w.Code)
			}
			policy := w.Header().Get("Content-Security-Policy")
			folder := c.host + pluginPagePrefix + page.token + "/"
			want := "https://" + folder + " http://" + folder
			for _, d := range []string{"script-src " + want + ";", "style-src " + want + " 'unsafe-inline';", "img-src " + want + " data:;", "font-src " + want + ";", "media-src " + want + ";"} {
				if !strings.Contains(policy, d) {
					t.Errorf("policy has no %q: %s", d, policy)
				}
			}
			// no source without its scheme is left
			for _, d := range strings.Split(policy, ";") {
				for _, src := range strings.Fields(d)[1:] {
					if strings.Contains(src, "/_magpie/") && !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
						t.Errorf("scheme-less source %q in %s", src, policy)
					}
				}
			}
			if strings.Contains(policy, "evil.example") {
				t.Errorf("a forwarding header named a host: %s", policy)
			}
		})
	}
	// the desktop window names its own transport alone
	r := httptest.NewRequest("GET", pluginPagePrefix+page.token+"/index.html", nil)
	r.Host = "localhost"
	w := do(ownWindow(pageMux()), r)
	if policy := w.Header().Get("Content-Security-Policy"); strings.Contains(policy, "https://") {
		t.Errorf("the window's policy names https: %s", policy)
	}
}
