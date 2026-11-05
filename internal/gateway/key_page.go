package gateway

import (
	_ "embed"
	"net/http"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// KeyPagePath is the page a gateway key's holder opens in a browser at the
// address they reach the gateway at (zytszone on X: magpie shared with a
// whole department, whose people can't be given the admin's app). It asks
// for their key and shows, from keyInfo, that key's own name, limits,
// usage and models, and the setup to paste into Claude Code, Codex or an
// OpenAI-compatible client, with the address it was opened at as the base
// URL.
const KeyPagePath = "/me"

// keyPageData is the key page's own route.
const keyPageData = "/v1/magpie/me"

//go:embed keypage.html
var keyPageHTML []byte

// keyPageOpen: r asks for the key page itself, which lanGuard serves to
// another machine without a key while magpie is shared: it is the same
// bytes for everyone and has no data in it. Its data (keyInfo) needs the
// key as every other route does.
func keyPageOpen(r *http.Request) bool {
	return r.URL.Path == KeyPagePath && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// keyPage serves the key page. It loads nothing from anywhere else, posts
// nothing and can't be framed, so a page that tricks a holder into
// opening it can't read or steer what they type into it.
func (s *Server) keyPage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	w.Write(keyPageHTML)
}

// keyPageUsage are the periods the key page sums its key's calls over.
var keyPageUsage = []usage.Period{usage.Today, usage.Week, usage.Month}

// keyInfo is what the calling gateway key may know of itself, for the key
// page: its name, its limits and what it has spent of them, its calls
// over today, 7 and 30 days and the models it called most, and the models
// it may use. Nothing in it is another key's, an account's, a provider's
// key or the admin's: no account names, no other key, no secret (the
// holder typed theirs). No key, a wrong one, one turned off and magpie's
// own token (which is no gateway key) are answered 401 alike.
func (s *Server) keyInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Has("key") {
		writeError(w, provider.Chat, http.StatusBadRequest, "send the gateway key as Authorization: Bearer <key> or x-api-key, never in the address")
		return
	}
	who := access.Caller(r.Context())
	if who.KeyID == "" || who.KeyID == usage.NoCallerKey {
		writeError(w, provider.Chat, http.StatusUnauthorized, "send an enabled gateway key as Authorization: Bearer <key> or x-api-key")
		return
	}
	limits := budget.All(access.Key{ID: who.KeyID, Name: who.KeyName, Limit: who.Limit, More: who.More}, limitClock())
	if limits == nil {
		limits = []budget.Status{}
	}
	sums := map[string]keyPageTotals{}
	// the models it called most, as their vendors name them
	top := []keyPageModel{}
	for _, p := range keyPageUsage {
		page := usage.QueryPage(p, usage.Filter{CallerKey: who.KeyID}, 0, 1)
		sums[string(p)] = keyPageSum(page.Sum)
		if p != usage.Month {
			continue
		}
		for _, sh := range page.By["model"] {
			if len(top) < 8 {
				top = append(top, keyPageModel{ID: sh.ID, keyPageTotals: keyPageSum(sh.Totals)})
			}
		}
	}
	models := []map[string]string{}
	// a read-only key is refused every model (readOnlyGuard)
	if !who.ReadOnly {
		// as /v1/models tells this key, for no agent in particular
		shown, _ := provider.CatalogFor("")
		shown = keyListed(r, shown, provider.ModelOff(""))
		labels := provider.Labels(shown)
		for i, e := range shown {
			models = append(models, map[string]string{"id": e.ID, "label": labels[i]})
		}
	}
	writeJSON(w, 200, map[string]any{
		"name":      who.KeyName,
		"readOnly":  who.ReadOnly,
		"limits":    limits,
		"usage":     sums,
		"topModels": top,
		"models":    models,
		"version":   Version,
	})
}

// keyPageTotals are the sums the key page shows of a key's calls.
type keyPageTotals struct {
	Calls      int     `json:"calls"`
	Errors     int     `json:"errors"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Cost       float64 `json:"cost"`
	Unpriced   int     `json:"unpriced"`
}

type keyPageModel struct {
	ID string `json:"id"`
	keyPageTotals
}

func keyPageSum(t usage.Totals) keyPageTotals {
	return keyPageTotals{Calls: t.Calls, Errors: t.Errors, Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, Cost: t.Cost, Unpriced: t.Unpriced}
}
