package gui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
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
	{pattern: "GET /gateway-keys/account-windows", to: "/caller-keys/account-windows"},
	{pattern: "GET /gateway-keys/mcp", to: "/caller-keys/mcp"},
	{pattern: "POST /gateway-keys/{action}", to: "/caller-keys/{action}", actions: adminKeyActions},
	{pattern: "GET /account-groups"},
	{pattern: "POST /account-groups/{action}"},
	{pattern: "GET /routing-groups", to: "/groups"},
	{pattern: "POST /routing-groups/{action}", to: "/groups/{action}", actions: adminGroupActions},
}

// adminGroupActions are the Routing page's actions on its groups (#6307):
// saving one (its members, combos of groups and each member's account
// among them), removing, copying, switching, ordering, picking a Manual
// group's member, the defaults every group follows, a template, and a
// group shared as text or imported from it. Not "found" and "skip": they
// are the page's switch for groups magpie finds, and moving the agents
// set to one rewrites this computer's agents. "create" and "update" are
// the API's own, served by adminGroupWrite before they reach "save".
var adminGroupActions = []string{
	"save", "delete", "copy", "switch", "arrange", "pick", "defaults",
	"template", "share", "import", "show",
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
	// One routing group, as GET routing-groups lists it; and create and
	// update, which are the app's save with the id checked first.
	mux.HandleFunc("GET /routing-groups/{id...}", func(w http.ResponseWriter, r *http.Request) {
		g, ok := adminGroup(r.PathValue("id"))
		if !ok {
			adminError(w, http.StatusNotFound, "magpie has no routing group "+strconv.Quote(r.PathValue("id")))
			return
		}
		writeJSON(w, g)
	})
	mux.HandleFunc("POST /routing-groups/create", func(w http.ResponseWriter, r *http.Request) { adminGroupWrite(app, w, r, true) })
	mux.HandleFunc("POST /routing-groups/update", func(w http.ResponseWriter, r *http.Request) { adminGroupWrite(app, w, r, false) })
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

// adminGroup is the routing group id as the Routing page reads it
// (groupsState), by its id or by the ref agents pick it by (group/<id>,
// auto/<model>).
func adminGroup(id string) (groupJSON, bool) {
	id = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(id, "magpie/")))
	for _, g := range groupsState().Groups {
		if g.ID == id || g.Ref == id {
			return g, true
		}
	}
	return groupJSON{}, false
}

// adminGroupWrite serves routing-groups/create and /update, each the
// app's own save (POST /api/groups/save), so a group is checked exactly
// as the Routing page's editor has it checked. Create refuses, with 409,
// an id a group has already, where save would write over it; update
// refuses one magpie has no group for (404), and takes the group as it is
// with the fields the body sends put over it, so a script changes one
// field without sending the rest. Its "from" names the group to change
// when "id" gives it a new one (a rename, which moves the agents set to
// it as the page's does).
func adminGroupWrite(app http.Handler, w http.ResponseWriter, r *http.Request, create bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil || in == nil {
		adminError(w, http.StatusBadRequest, "the body is a routing group as JSON")
		return
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(in[k], &v)
		return strings.TrimSpace(v)
	}
	id, from := strings.ToLower(str("id")), strings.ToLower(str("from"))
	if id == "" {
		id = provider.GroupSlug(str("name")) // as SaveGroup names one
	}
	if create {
		if from != "" {
			adminError(w, http.StatusBadRequest, "create makes a new group: it takes no \"from\" (rename one with update)")
			return
		}
		if _, ok := adminGroup(id); ok && id != "" {
			adminError(w, http.StatusConflict, "magpie has a routing group "+strconv.Quote(id)+" already: change it with update")
			return
		}
	} else {
		if from == "" {
			from = id
		}
		cur, ok := adminGroup(from)
		if !ok || from == "" {
			adminError(w, http.StatusNotFound, "magpie has no routing group "+strconv.Quote(from))
			return
		}
		if from != id && id != "" {
			if _, taken := adminGroup(id); taken {
				adminError(w, http.StatusConflict, "magpie has a routing group "+strconv.Quote(id)+" already")
				return
			}
		}
		raw, err := json.Marshal(cur.Group)
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var merged map[string]json.RawMessage
		if err := json.Unmarshal(raw, &merged); err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := in["members"]; ok {
			// new members: what the old ones' patterns matched, and the
			// order they were placed in, are found again from them
			for _, k := range []string{"matched", "placed"} {
				if _, sent := in[k]; !sent {
					delete(merged, k)
				}
			}
		}
		for k, v := range in {
			merged[k] = v
		}
		from = cur.ID
		if id == "" {
			id = from
		}
		merged["from"], _ = json.Marshal(from)
		merged["id"], _ = json.Marshal(id)
		in = merged
	}
	out, err := json.Marshal(in)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodPost
	r2.URL.Path, r2.URL.RawPath = "/api/groups/save", ""
	r2.Body = io.NopCloser(bytes.NewReader(out))
	r2.ContentLength = int64(len(out))
	app.ServeHTTP(w, r2)
}

func adminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
