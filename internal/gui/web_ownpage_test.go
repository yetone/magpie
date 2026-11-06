package gui

import (
	"net/http"
	"testing"
)

// A page on another port of the same host, on another port of the LAN
// address, or on a sibling name behind the same proxy is the same site, so
// the browser sends it the page's SameSite=Lax cookie: its "simple" POST
// (text/plain, no-cors) to /api/library/servers/save, then
// /api/library/mcp/check, would start an MCP server of its own as the
// user. Through the whole chain `magpie web` serves, with the page's
// cookie, every such request is turned away, and the page's own still
// reach the API.
func TestWebAPIAnswersItsOwnPageOnly(t *testing.T) {
	onePortHome(t)
	const lo = "127.0.0.1:50001"
	noCors := []string{"Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "empty"}
	marks := func(site string, hdr ...string) []string {
		if site != "" {
			hdr = append(hdr, "Sec-Fetch-Site", site)
		}
		return append(hdr, noCors...)
	}
	call := func(name string, want bool, method, target, host string, hdr ...string) {
		t.Helper()
		var reached bool
		h := onePortPage(&reached)
		w := onePortCall(t, h, method, target, lo, host, onePortWebKey, hdr...)
		if reached != want || !want && w.Code != http.StatusForbidden {
			t.Errorf("%s: %s %s under Host %s: %d, reached %v, want reached %v: %s", name, method, target, host, w.Code, reached, want, w.Body.String())
		}
	}
	const save, check = "/api/library/servers/save", "/api/library/mcp/check"
	for _, path := range []string{save, check} {
		// another port of 127.0.0.1
		call("same-site Origin on another port", false, "POST", path, "127.0.0.1:3430", marks("same-site", "Origin", "http://127.0.0.1:8080")...)
		call("cross-port Origin from a browser without Sec-Fetch-Site", false, "POST", path, "127.0.0.1:3430", marks("", "Origin", "http://127.0.0.1:8080")...)
		call("localhost on another port", false, "POST", path, "localhost:3430", marks("same-site", "Origin", "http://localhost:5173")...)
		// another port of the LAN address
		call("the LAN address on another port", false, "POST", path, "192.168.1.20:3430", marks("same-site", "Origin", "http://192.168.1.20:8080")...)
		call("the LAN address on another port, an old browser", false, "POST", path, "192.168.1.20:3430", marks("", "Origin", "http://192.168.1.20:8080")...)
		// a sibling name behind the same reverse proxy
		call("a sibling subdomain", false, "POST", path, "magpie.example.com", marks("same-site", "Origin", "https://blog.example.com")...)
		call("a sibling subdomain, an old browser", false, "POST", path, "magpie.example.com", marks("", "Origin", "https://blog.example.com")...)
		call("another site", false, "POST", path, "127.0.0.1:3430", marks("cross-site", "Origin", "https://evil.example")...)
		// a sandboxed frame or a local file
		call("Origin null", false, "POST", path, "127.0.0.1:3430", marks("cross-site", "Origin", "null")...)
		call("Origin null, an old browser", false, "POST", path, "127.0.0.1:3430", marks("", "Origin", "null")...)
		// what the Origin leaves out the browser still says
		call("no Origin, Sec-Fetch-Site same-site", false, "POST", path, "127.0.0.1:3430", marks("same-site")...)
		call("no Origin, Sec-Fetch-Site cross-site", false, "POST", path, "127.0.0.1:3430", marks("cross-site")...)
	}
	// a read another page sends: an <img>, a link, a form's GET
	call("a GET from another port", false, "GET", "/api/import/x", "127.0.0.1:3430", marks("same-site")...)
	call("a GET from a sibling subdomain", false, "GET", "/api/import/x", "magpie.example.com", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Dest", "image")
	call("a link from another site", false, "GET", "/api/import/x", "127.0.0.1:3430", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "document", "Sec-Fetch-Mode", "navigate")
	call("a preflight from another port", false, "OPTIONS", save, "127.0.0.1:3430", "Origin", "http://127.0.0.1:8080", "Sec-Fetch-Site", "same-site")

	// the page's own
	own := []string{"Content-Type", "application/json", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}
	call("the page's own origin", true, "POST", save, "127.0.0.1:3430", append([]string{"Origin", "http://127.0.0.1:3430", "Sec-Fetch-Site", "same-origin"}, own...)...)
	call("the page's own origin, an old browser", true, "POST", save, "127.0.0.1:3430", append([]string{"Origin", "http://127.0.0.1:3430"}, own...)...)
	call("the page's own origin on the LAN", true, "POST", save, "192.168.1.20:3430", append([]string{"Origin", "http://192.168.1.20:3430", "Sec-Fetch-Site", "same-origin"}, own...)...)
	call("behind a proxy that keeps the Host", true, "POST", save, "magpie.example.com", append([]string{"Origin", "https://magpie.example.com"}, own...)...)
	// a proxy that passes on a Host of its own (nginx's default): the
	// browser's word is enough, and so is MAGPIE_PUBLIC_URL's address
	call("behind a proxy that rewrites the Host", true, "POST", save, "127.0.0.1:3430", append([]string{"Origin", "https://magpie.example.com", "Sec-Fetch-Site", "same-origin"}, own...)...)
	call("a page's own read", true, "GET", "/api/state", "127.0.0.1:3430", "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty")
	call("the address typed in", true, "GET", "/api/state", "127.0.0.1:3430", "Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document")
	call("a client that isn't a browser, with the key's cookie", true, "POST", save, "127.0.0.1:3430", "Content-Type", "application/json")
	// the page's files aren't the API
	call("the page itself from a link elsewhere", true, "GET", "/", "127.0.0.1:3430", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document")

	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.com/")
	call("an old browser behind a proxy that rewrites the Host, MAGPIE_PUBLIC_URL set", true, "POST", save, "127.0.0.1:3430", append([]string{"Origin", "https://magpie.example.com"}, own...)...)
	call("a sibling subdomain, MAGPIE_PUBLIC_URL set", false, "POST", save, "127.0.0.1:3430", marks("same-site", "Origin", "https://blog.example.com")...)
}
