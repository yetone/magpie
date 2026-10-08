package gateway

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// corsGuard lets the web pages the user listed in Settings › Gateway
// (settings.CORSOrigins, #1051) call the gateway from a browser: their
// preflight is answered, and their calls get the headers that let the page
// read the reply. Such a call carries an enabled gateway key — a page is
// not an agent the user started, and on loopback a call needs no key, so
// without one any script served at that origin (another dev server on
// the same port) could spend the user's subscriptions. A page not listed
// gets no CORS headers, as before, and a request with no Origin (every
// agent's) passes untouched.
//
// Any other web page is refused (403, foreignPage): on loopback a call
// needs no key and the body's Content-Type isn't checked, so a page the
// user merely visits could send a "simple" text/plain POST and spend their
// subscriptions blind. Such a page never read a reply before (no CORS
// headers), so refusing it breaks nothing that worked.
func corsGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !corsAllowed(origin) {
			if foreignPage(r, origin) {
				log.Printf("refused %s %s from the web page %s: not one listed in Settings", r.Method, r.URL.Path, origin)
				writeError(w, provider.Chat, http.StatusForbidden, "magpie doesn't answer the web page "+origin+": list it in Settings → Network and sharing → Web pages to let it call magpie with a gateway key")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Access-Control-Expose-Headers", SessionHeader)
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if asked := corsHeaders(r.Header.Get("Access-Control-Request-Headers")); asked != "" {
				h.Set("Access-Control-Allow-Headers", asked)
			}
			// Chrome asks before a public page reaches this computer
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// lanGuard, in front of Handler on the real server, has already
		// taken the key and put magpie's own token in its place
		keyed := access.Caller(r.Context()).KeyID != "" ||
			slices.ContainsFunc(callerKeys(r), func(k string) bool { _, ok := access.Authenticate(k); return ok })
		if !keyed {
			writeError(w, provider.Chat, http.StatusUnauthorized, "a web page calls magpie with a gateway key: create one in magpie's Gateway page and send it as Authorization: Bearer <key> (or x-api-key)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// foreignPage: origin is an http(s) web page that is neither on this
// computer (localhost, *.localhost, 127.0.0.0/8, [::1], any port) nor the
// request's own origin (a page served at the address the request names,
// such as magpie's web app on a LAN address), so a browser sent it for a
// site elsewhere. usemagpie.ai may still read the Omarchy theme
// (omarchyTheme).
//
// Only http and https are judged. "null" (a sandboxed frame, a file://
// page, some Electron renderers) and other schemes — app://, file://,
// tauri://, vscode-webview://, chrome-extension:// — come from desktop
// apps, editor webviews and browser extensions. Such a client could call
// magpie before only because it doesn't enforce CORS (a webview with web
// security off, an extension holding host permissions), and no ordinary
// web page can send those origins, so they pass as before. *.localhost
// counts as this computer: browsers resolve it to loopback, and Tauri's
// and Wails' webviews on Windows are http://tauri.localhost and
// http://wails.localhost, whose HTTP plugins send that Origin outside CORS.
func foreignPage(r *http.Request, origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		// a browser's always parses; an unreadable http one is no page of ours
		return strings.HasPrefix(strings.ToLower(origin), "http")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	if hostPort(u.Host, scheme) == hostPort(r.Host, scheme) {
		return false
	}
	if r.URL.Path == "/v1/magpie/omarchy" && siteOrigin(origin) {
		return false
	}
	return true
}

// hostPort is host[:port] lower-cased with the scheme's port filled in, so
// https://a.example and a Host of a.example:443 compare equal.
func hostPort(hp, scheme string) string {
	hp = strings.ToLower(hp)
	if hp == "" {
		return ""
	}
	if h, p, err := net.SplitHostPort(hp); err == nil {
		return net.JoinHostPort(strings.Trim(h, "[]"), p)
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(hp, "[]"), port)
}

// corsAllowed: origin is one the user listed.
func corsAllowed(origin string) bool {
	saved := settings.Load().CORSOrigins
	if len(saved) == 0 {
		return false
	}
	// as saved from Settings, or as written into settings.json by hand
	list, _ := settings.CleanOrigins(saved)
	o, err := settings.CleanOrigins([]string{origin})
	return err == nil && len(o) == 1 && slices.Contains(list, o[0])
}

// corsHeaders is a preflight's Access-Control-Request-Headers as the
// answer allows them: the header names asked for, nothing else.
func corsHeaders(asked string) string {
	var out []string
	for _, f := range strings.Split(asked, ",") {
		f = strings.TrimSpace(f)
		if f != "" && !strings.ContainsFunc(f, func(c rune) bool {
			return !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
		}) {
			out = append(out, f)
		}
	}
	return strings.Join(out, ", ")
}
