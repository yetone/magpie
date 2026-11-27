package gui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/gateway"
)

// Crazybiaobiao on X, after #7890: the browser UI on another computer
// opened an artifact at the gateway's own address, 127.0.0.1 inside the
// container. It opens it at the page's address now, so `magpie web`'s port
// hands /a/ to the gateway, which reads it to that browser as a shared
// copy: only while magpie is on the network, with no token and every
// capability null, and never the page's runtime (_api), which needs the
// page's key and has no handler there.
func TestWebOnePortOpensAnArtifact(t *testing.T) {
	onePortHome(t)
	g := gateway.New()
	prev := served.Swap(g)
	t.Cleanup(func() { served.Store(prev) })
	var reached bool
	h := onePortPage(&reached)
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<title>T</title><p>hi"), Capabilities: artifact.Capabilities{"db": []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	page := gateway.ArtifactPagePath + a.ID + "/"
	const lo, proxy = "127.0.0.1:50001", "magpie.example.com"
	browser := []string{"User-Agent", "Mozilla/5.0 (Macintosh) Chrome/140", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document", "X-Forwarded-For", "203.0.113.9"}

	// off the network the gateway opens it to no other computer: the
	// page's sign-in does (#7665), and sends a tab on to its files
	reached = false
	if w := onePortCall(t, h, "GET", page, lo, proxy, "", browser...); w.Code != http.StatusUnauthorized || reached || strings.Contains(w.Body.String(), "<p>hi") {
		t.Fatalf("off the network, not signed in: %d, page reached %v: %s", w.Code, reached, w.Body)
	}
	w := onePortCall(t, h, "GET", page, lo, proxy, onePortWebKey, browser...)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || reached || !strings.HasPrefix(loc, webArtifactPrefix) {
		t.Fatalf("off the network, signed in: %d, page reached %v: %v", w.Code, reached, w.Header())
	}
	if w := onePortCall(t, h, "GET", loc, lo, proxy, "", browser...); w.Code != 200 || reached || !strings.Contains(w.Body.String(), "<p>hi") || !strings.Contains(w.Body.String(), `"shared":true`) {
		t.Fatalf("off the network, its files: %d, page reached %v: %s", w.Code, reached, w.Body)
	}

	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425") // the Docker image's
	w = onePortCall(t, h, "GET", page, lo, proxy, onePortWebKey, browser...)
	body := w.Body.String()
	tok, _ := artifact.PageToken(a.ID)
	if w.Code != 200 || reached || !strings.Contains(body, "<p>hi") || !strings.Contains(body, `"shared":true`) || tok == "" || strings.Contains(body, tok) {
		t.Fatalf("on the network: %d, page reached %v: %s", w.Code, reached, body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("its CSP: %q", csp)
	}

	// the runtime stays the page's: no key, no way in; with it, the page
	// port's own refusal (webArtifactLinks: an artifact is read only
	// there), nothing the gateway would answer
	for _, p := range []string{page + "_api/db", page + "_api/publish"} {
		reached = false
		if w := onePortCall(t, h, "POST", p, lo, proxy, "", browser...); w.Code < 300 || reached {
			t.Errorf("%s without the page's key: %d, page reached %v", p, w.Code, reached)
		}
		reached = false
		if w := onePortCall(t, h, "POST", p, lo, proxy, onePortWebKey, browser...); w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Body.String(), "read only here") {
			t.Errorf("%s with the page's key went to the gateway: %d %s", p, w.Code, w.Body)
		}
	}
}
