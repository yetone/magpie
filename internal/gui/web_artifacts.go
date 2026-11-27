package gui

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// An artifact (gateway's ArtifactPagePath) is served by the gateway to a
// browser on this computer alone. magpie web on a server, at
// MAGPIE_PUBLIC_URL, serves it to the browsers signed in to it too
// (#7665): its links name the page's address (ArtifactsAt), where
// <base>/a/<id>/ takes the sign-in (webArtifactLinks) and sends the
// browser on to webArtifactPrefix<token>/<id>/. The page is served there
// in the same sandbox, an opaque origin whose own requests (a module
// script, a fetch of its data, an image) carry no cookie, so the token in
// its folder's path is what lets them in (webArtifactPages), as a
// plugin's page's is (pluginPagePrefix).
//
// The token is this run's: an HMAC of the artifact's id (one artifact,
// every version of it) under pageSecret. It opens that artifact's files
// and nothing else, and a restart makes it worthless: a tab still on an
// old one is sent back to /a/, through the sign-in.

// webArtifactPrefix is where magpie web serves an artifact's files:
// <token>/<id>[@<version>]/<file>.
const webArtifactPrefix = "/_magpie/a/"

// webArtifactToken opens artifact id's files at webArtifactPrefix. A
// plugin's spec never holds a NUL, so no plugin's token is an
// artifact's.
func webArtifactToken(id string) string { return pageToken("artifact\x00" + id) }

// webArtifactPages serves webArtifactPrefix ahead of webGuard: with the
// artifact's token, its files, read only; with a wrong one, a navigation
// is sent to the artifact's address (through the sign-in) and anything
// else gets nothing.
func webArtifactPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, webArtifactPrefix)
		if !ok {
			next.ServeHTTP(rw, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			rw.Header().Set("Allow", "GET, HEAD")
			http.Error(rw, "an artifact is read only here", http.StatusMethodNotAllowed)
			return
		}
		tok, rest, _ := strings.Cut(rest, "/")
		ref, name, _ := strings.Cut(rest, "/")
		id, _, ok := gateway.ArtifactRef(ref)
		if !ok {
			http.NotFound(rw, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(webArtifactToken(id))) != 1 {
			if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
				// a tab opened before magpie restarted: the address
				// takes the sign-in again, and hands it this run's token
				webNoStore(rw)
				http.Redirect(rw, r, (&url.URL{Path: gateway.ArtifactPagePath + ref + "/" + name}).EscapedPath(), http.StatusFound)
				return
			}
			http.NotFound(rw, r)
			return
		}
		gateway.ServeArtifactElsewhere(rw, r, webArtifactPrefix+tok+"/", ref, name)
	})
}

// webArtifactLinks, behind webGuard, sends a browser signed in that opens
// an artifact's address (<base>/a/<id>/…) on to its files with their
// token. Only a navigation is sent on: a script's request, which another
// page of the same site could make with the cookie, gets no token.
func webArtifactLinks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, gateway.ArtifactPagePath)
		if !ok {
			next.ServeHTTP(rw, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			rw.Header().Set("Allow", "GET, HEAD")
			http.Error(rw, "an artifact is read only here", http.StatusMethodNotAllowed)
			return
		}
		if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
			http.Error(rw, "an artifact opens in a tab of its own", http.StatusForbidden)
			return
		}
		ref, name, _ := strings.Cut(rest, "/")
		id, _, ok := gateway.ArtifactRef(ref)
		if !ok {
			http.NotFound(rw, r)
			return
		}
		webNoStore(rw)
		http.Redirect(rw, r, (&url.URL{Path: webArtifactPrefix + webArtifactToken(id) + "/" + ref + "/" + name}).EscapedPath(), http.StatusFound)
	})
}

// webNoStore keeps a redirect that holds, or leads from, a token out of
// every cache and Referer.
func webNoStore(rw http.ResponseWriter) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
}

// WebURL is MAGPIE_WEB_URL, magpie web's own address behind a reverse
// proxy (https:// when it names no scheme), without its trailing slash;
// "" when it isn't set or isn't a plain http(s) address.
func WebURL() string {
	v := strings.TrimSpace(os.Getenv("MAGPIE_WEB_URL"))
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// ArtifactsAt is where artifacts' links point when magpie is reached
// from elsewhere: MAGPIE_WEB_URL, else MAGPIE_PUBLIC_URL (or a LazyCat
// app's address), each as given, its scheme, host, port and path; ""
// with neither. magpie web serves /a/ at its own address (onePort hands
// it to the gateway, or its sign-in takes it), and the gateway at
// MAGPIE_PUBLIC_URL's, so the address the user named is the one that
// reaches it: a proxy's https://magpie.example.com:666/magpie as much as
// a Docker host's http://nas.local:3425. Only its host, on the page's
// own scheme and port, gave a proxy's users an address nothing answers
// at (#8014). Without either the links stay on this computer, where a
// desktop's or a LAN server's user opens them.
func ArtifactsAt() string {
	if u := WebURL(); u != "" {
		return u
	}
	pub, err := url.Parse(gateway.PublicURL())
	if err != nil || pub.Host == "" || (pub.Scheme != "http" && pub.Scheme != "https") {
		return ""
	}
	return strings.TrimRight(pub.String(), "/")
}
