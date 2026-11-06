package gateway

import (
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A read-only gateway key (access.Key.ReadOnly) reads this magpie's stats
// and uses nothing (Steven on Discord: an ESP32 desk display polling
// /v1/magpie/quotas and /v1/magpie/usage every 15s, its key kept in the
// device's flash, which had to be a key that could spend every account).
// It is answered the routes below alone, by the pattern the request is
// routed to, so a route added later is refused it until it is put here:
// every model route, count_tokens, a WebSocket's upgrade, every MCP
// server, a plugin's backend and every write are refused, whatever
// method or path spelling reaches them. TestReadOnlyKeyEveryRoute fails
// when a route is neither here nor in its list of refused ones.
//
// What it reads is what a key held to nothing reads: the usage summary,
// every account's quotas (it is no one else's to spend, so heldKey
// leaves it alone unless it is held to a budget, models or accounts as
// well) and the routing as it happens, redacted (route_events.go,
// joe21_eee's display, #3387). None of these routes tells a gateway
// key's secret.
var readOnlyRoutes = map[string]bool{
	"GET /{$}":                        true,
	"GET /v1":                         true,
	"GET /v1/{$}":                     true,
	"GET /v1/magpie/quotas":           true,
	"GET /v1/magpie/quotas/history":   true,
	"GET " + StatuslinePath:           true,
	"POST " + StatuslinePath:          true,
	"GET " + provider.RemoteCardsPath: true,
	"GET /v1/magpie/usage":            true,
	"GET /v1/magpie/usage/requests":   true,
	"GET /v1/magpie/limit":            true,
	"GET /v1/magpie/concurrency":      true,
	"GET " + routeSnapshotPath:        true,
	"GET " + routeEventsPath:          true,
	"GET " + OwnMCPPath:               true,
	"GET " + KeyPagePath:              true,
	"GET " + keyPageData:              true,
	// a proof carries no key, and answers none (remote_prove.go)
	"POST " + provider.RemoteProvePath: true,
}

// ReadOnlyCode is the error code a read-only key is refused a route with.
const ReadOnlyCode = provider.KeyReadOnlyCode

// routeMux is a ServeMux that keeps the patterns it was given.
type routeMux struct {
	*http.ServeMux
	patterns []string
}

func (m *routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.HandleFunc(pattern, h)
}

// BrowserKeyCode is the error code the Magpie Browser extension's key
// (access.Key.Browser) is refused a route with.
const BrowserKeyCode = "gateway_key_browser_only"

// browserKeyRoute is the one route the extension's key is answered: the
// extension's WebSocket. Its key is written into a zip that lands in
// Downloads (gui's browserKey), so whoever reads the zip has it; it must
// spend nothing, read nothing and reach no MCP server, the browser's own
// included (TestBrowserKeyEveryRoute).
const browserKeyRoute = "GET " + BrowserExtensionPath

// readOnlyGuard refuses a read-only key every route of mux but
// readOnlyRoutes, and the Magpie Browser extension's key every route but
// browserKeyRoute, before anything is read, reserved or sent. Both are
// judged here, after the key is known (callerGuard, lanGuard's
// identifyCaller) and by the pattern the request is routed to, so no
// route is left out, one added later included.
func readOnlyGuard(mux *routeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if who.Browser {
			if _, pattern := mux.Handler(r); pattern != browserKeyRoute {
				log.Printf("refused %s %s: gateway key %q is Magpie Browser's", r.Method, r.URL.Path, who.KeyName)
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
					"message": "this gateway key is the Magpie Browser extension's: it connects the extension and nothing else, no model, MCP server, stats or write. Use another gateway key for those (Gateway → Gateway keys)",
					"type":    "permission_error", "code": BrowserKeyCode}})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if who.ReadOnly {
			// a path that isn't clean is routed to a redirect under the
			// pattern of where it goes, and judged again when it gets there
			if _, pattern := mux.Handler(r); !readOnlyRoutes[pattern] {
				log.Printf("refused %s %s: gateway key %q is read-only", r.Method, r.URL.Path, who.KeyName)
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
					"message": "this gateway key is read-only: it may read magpie's quotas and usage (GET /v1/magpie/quotas, /v1/magpie/quotas/history, /v1/magpie/usage, /v1/magpie/usage/requests, /v1/magpie/limit, /v1/magpie/route-events) and use no model, MCP server or write. Use a key with full access for that (Gateway → Gateway keys, or magpie gateway-key access <id> full)",
					"type":    "permission_error", "code": ReadOnlyCode}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
