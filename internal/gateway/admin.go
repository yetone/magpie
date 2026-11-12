package gateway

import (
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/yetone/magpie/internal/access"
)

// AdminPath is the management API: add, change and remove the providers,
// accounts and gateway keys the app's pages manage, for automation
// (ljayic on X). The routes under it are the app's own (internal/gui,
// adminAPI), so the two can't drift; this file decides who may call them.
//
// Unlike every model route, no call is let in as "from this computer": it
// needs an enabled gateway key that carries Admin (access.Key.Admin, set by
// `magpie gateway-key admin` and the app, never through the API), from
// loopback and from anywhere. A key the gateway doesn't know, and a call
// with none, are 401; any other key is 403. From another machine the key
// must be AdminAny, and the gateway is shared (lanGuard) as for any call
// from there.
//
// And no browser calls it, with a key or without: a page sends Origin, or
// the Sec-Fetch headers, and a script can't take them off, so one is
// refused whatever its Origin says (null, a foreign page, a listed page,
// localhost). A request that names this computer by a name that isn't its
// own (DNS rebinding) is refused too, with its key or without.
const AdminPath = "/v1/magpie/admin/"

// adminHandler is what serves the routes under AdminPath, set by the app's
// package (gui) at start; nil serves 503.
var adminHandler atomic.Pointer[http.Handler]

// SetAdminHandler sets the handler for the routes under AdminPath. The path
// it sees has AdminPath's prefix taken off, with its leading slash kept.
func SetAdminHandler(h http.Handler) { adminHandler.Store(&h) }

// AdminCode is the error code a key without Admin is refused with.
const AdminCode = "gateway_key_not_admin"

func adminRefuse(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "permission_error", "code": code}})
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// a page, whatever it says it is: its headers aren't its script's to drop
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		log.Printf("refused %s %s: the management API answers no web page (Origin %q)", r.Method, r.URL.Path, r.Header.Get("Origin"))
		adminRefuse(w, http.StatusForbidden, "admin_browser_refused", "magpie's management API is for scripts, not web pages: a request that carries Origin or Sec-Fetch headers is refused, with a key or without")
		return
	}
	who := access.Caller(r.Context())
	if who.KeyID == "" {
		adminRefuse(w, http.StatusUnauthorized, "admin_key_required", "the management API needs a gateway key that may manage magpie, even from this computer: send it as Authorization: Bearer <key>, after `magpie gateway-key admin <id> local`")
		return
	}
	if who.Admin == "" {
		log.Printf("refused %s %s: gateway key %q may not manage magpie", r.Method, r.URL.Path, who.KeyName)
		adminRefuse(w, http.StatusForbidden, AdminCode, "this gateway key may not manage magpie: allow it with `magpie gateway-key admin <id> local` (or `any` for other machines)")
		return
	}
	if local(r) {
		if !ownHost(r.Host) {
			log.Printf("refused %s %s: reached loopback as %q", r.Method, r.URL.Path, r.Host)
			adminRefuse(w, http.StatusForbidden, "admin_host_refused", "magpie's management API doesn't answer a request that reached this computer under the name "+r.Host+": call it as localhost or 127.0.0.1")
			return
		}
	} else if who.Admin != access.AdminAny {
		log.Printf("refused %s %s from another machine: gateway key %q may manage magpie from this computer only", r.Method, r.URL.Path, who.KeyName)
		adminRefuse(w, http.StatusForbidden, AdminCode, "this gateway key may manage magpie from this computer only: allow other machines with `magpie gateway-key admin <id> any`")
		return
	}
	hp := adminHandler.Load()
	if hp == nil {
		adminRefuse(w, http.StatusServiceUnavailable, "admin_unavailable", "this magpie serves no management API")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	rest := "/" + strings.TrimPrefix(r.URL.Path, AdminPath)
	log.Printf("management API: %s %s by gateway key %q", r.Method, rest, who.KeyName)
	r2 := r.Clone(r.Context())
	r2.URL.Path, r2.URL.RawPath = rest, ""
	// the app's own handlers answer a key in the request as they would a
	// page's; none of the headers are theirs to see
	for _, h := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		r2.Header.Del(h)
	}
	(*hp).ServeHTTP(w, r2)
}
