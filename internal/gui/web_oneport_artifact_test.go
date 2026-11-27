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

	reached = false
	if w := onePortCall(t, h, "GET", page, lo, proxy, onePortWebKey, browser...); w.Code < 400 || reached {
		t.Fatalf("off the network: %d, page reached %v: %s", w.Code, reached, w.Body)
	}

	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425") // the Docker image's
	w := onePortCall(t, h, "GET", page, lo, proxy, onePortWebKey, browser...)
	body := w.Body.String()
	tok, _ := artifact.PageToken(a.ID)
	if w.Code != 200 || reached || !strings.Contains(body, "<p>hi") || !strings.Contains(body, `"shared":true`) || tok == "" || strings.Contains(body, tok) {
		t.Fatalf("on the network: %d, page reached %v: %s", w.Code, reached, body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("its CSP: %q", csp)
	}

	// the runtime stays the page's: no key, no way in; with it, nothing
	// the gateway would answer
	for _, p := range []string{page + "_api/db", page + "_api/publish"} {
		reached = false
		if w := onePortCall(t, h, "POST", p, lo, proxy, "", browser...); w.Code < 300 || reached {
			t.Errorf("%s without the page's key: %d, page reached %v", p, w.Code, reached)
		}
		reached = false
		if w := onePortCall(t, h, "POST", p, lo, proxy, onePortWebKey, browser...); w.Code != http.StatusTeapot || !reached {
			t.Errorf("%s with the page's key went to the gateway: %d", p, w.Code)
		}
	}
}
