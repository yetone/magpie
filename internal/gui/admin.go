package gui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// The management API (gateway.AdminPath, ljayic on X) serves the app's own
// handlers for providers, accounts and gateway keys, so that what a script
// does and what the pages do can't drift. adminRoutes lists, by the path
// under /v1/magpie/admin/, what is served and which of the app's /api route
// answers it; a route not listed is a 404, whatever the app has, and
// TestAdminRoutesEveryAPIRoute fails when the list holds a route that is
// not the app's.
//
// Left out on purpose, because they tell a secret or act on this
// computer's apps and not on magpie's own objects: a provider's saved key
// ("key"), a gateway key's secret ("copy-key"), exporting sign-ins, and
// the actions that move, restart or rewrite an agent. Setting a key's
// Admin ("admin-key") is left out so that a key can't give another more
// than it was given: only the command line and the app do.
type adminRoute struct {
	pattern string // "GET /providers": the method, and the path under AdminPath
	to      string // the app's route under /api it is served by, "" for the same path
	// actions, when set, are the {action}s the route takes; any other is 404
	actions []string
}

var adminRoutes = []adminRoute{
	{pattern: "GET /providers"},
	{pattern: "POST /provider/{action}", actions: adminProviderActions},
	{pattern: "POST /keys/{action}"},
	{pattern: "POST /login/{action}"},
	{pattern: "GET /login/usage"},
	{pattern: "GET /signin/{id}"},
	{pattern: "POST /signin/{id}/cancel"},
	{pattern: "POST /signin/{id}/callback"},
	{pattern: "GET /gateway-keys", to: "/caller-keys"},
	{pattern: "GET /gateway-keys/models", to: "/caller-keys/models"},
	{pattern: "GET /gateway-keys/mcp", to: "/caller-keys/mcp"},
	{pattern: "POST /gateway-keys/{action}", to: "/caller-keys/{action}", actions: adminKeyActions},
	{pattern: "GET /account-groups"},
	{pattern: "POST /account-groups/{action}"},
}

// adminProviderActions are the provider actions the API takes: saving and
// removing a provider, switching it, and the settings of its keys and
// accounts, and trying it. Not "key" (it tells the saved key), nor the
// actions on this computer's apps (show, move, adopt, restartapp, …).
var adminProviderActions = []string{
	"save", "delete", "on", "off", "name", "efforts", "images", "arrange",
	"accountmodels", "keylisted", "accountcap", "accountname", "windowcap",
	"accountconcurrency", "keyspendcap", "keyownbalance", "keybalancerefill",
	"keyexpiring", "accountweight", "route", "affinity", "sink", "usefirst",
	"test", "teststop", "detect", "balance", "models", "list",
}

// adminKeyActions are access.Update's actions but "admin-key" and "copy-key".
var adminKeyActions = []string{
	"add-key", "rotate-key", "rename-key", "limit-key", "reset-limit-key",
	"models-key", "aliases-key", "accounts-key", "mcp-key", "quotas-key",
	"concurrency-key", "tier-key", "redact-key", "issuer-key", "access-key",
	"on-key", "off-key", "remove-key", "move-key",
}

var (
	adminOnce sync.Once
	adminMux  http.Handler
)

func init() { gateway.SetAdminHandler(http.HandlerFunc(serveAdmin)) }

func serveAdmin(w http.ResponseWriter, r *http.Request) {
	adminOnce.Do(func() { adminMux = adminAPI() })
	adminMux.ServeHTTP(w, r)
}

// adminAPI is the management API's routes. The app's handlers are built
// with no window (nil Windows), so none opens a browser or a folder here.
func adminAPI() http.Handler {
	app := http.NewServeMux()
	providerRoutes(app, nil)
	callerKeyRoutes(app)
	mux := http.NewServeMux()
	for _, ar := range adminRoutes {
		mux.HandleFunc(ar.pattern, func(w http.ResponseWriter, r *http.Request) {
			if ar.actions != nil && !slices.Contains(ar.actions, r.PathValue("action")) {
				adminError(w, http.StatusNotFound, "this action is not in the management API")
				return
			}
			if strings.HasPrefix(ar.pattern, "POST /gateway-keys/") && !adminMayChangeKey(w, r) {
				return
			}
			to := r.URL.Path
			if ar.to != "" {
				to = strings.ReplaceAll(ar.to, "{action}", r.PathValue("action"))
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path, r2.URL.RawPath = "/api"+to, ""
			app.ServeHTTP(w, r2)
		})
	}
	// Signing in: the vendor's page and the code to type there are the
	// answer, for the caller to open on a computer with a browser. Never
	// opened here, and never given a password: the vendor's page takes it.
	mux.HandleFunc("POST /signin", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent, Site string
			Proxy       *string
			User        string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			adminError(w, http.StatusBadRequest, err.Error())
			return
		}
		st, err := provider.StartSignInWith(in.Agent, provider.SignInOptions{Site: in.Site, Proxy: in.Proxy, User: in.User})
		if err != nil {
			adminError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, st)
	})
	return mux
}

// adminMayChangeKey refuses, with 403, a key action through the API on
// another key that may manage magpie. Rotating one hands the caller its
// new secret (or sets one the caller chose), so a key allowed from this
// computer only would become one allowed from anywhere, or take over a
// peer's; and turning one off or removing it locks its owner out. A key
// that manages magpie is changed by the command line and the app, as its
// Admin is set there (admin-key is left out above). The caller's own key
// stays its to change. The body is read and put back for the handler.
func adminMayChangeKey(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(body, &in) != nil || in.Key == "" {
		return true // add-key, or a body the handler refuses itself
	}
	if in.Key == access.Caller(r.Context()).KeyID {
		return true
	}
	keys, err := access.List()
	if err != nil {
		// unknown: refused, not let through
		adminError(w, http.StatusServiceUnavailable, "the gateway keys can't be read: "+err.Error())
		return false
	}
	for _, k := range keys {
		if k.ID == in.Key && k.Admin != "" {
			adminError(w, http.StatusForbidden, "this key may manage magpie, so the management API doesn't change it: use `magpie gateway-key` or the app")
			return false
		}
	}
	return true
}

func adminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
