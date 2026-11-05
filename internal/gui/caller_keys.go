package gui

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
)

// keyRow is a gateway key as the Gateway page lists it, with what it has
// used of its limit (#585): Used of its first, UsedAll of each (#2227),
// in the order of its limits.
type keyRow struct {
	access.Key
	Used    *budget.Status  `json:"used,omitempty"`
	UsedAll []budget.Status `json:"usedAll,omitempty"`
}

// keyModelJSON is a model a gateway key may be held to, as its picker
// lists it.
type keyModelJSON struct {
	ID           string `json:"id"` // "<provider>/<model>" or "group/<id>", what the key keeps
	Name         string `json:"name"`
	Provider     string `json:"provider,omitempty"`
	ProviderName string `json:"providerName,omitempty"`
	Group        bool   `json:"group,omitempty"`   // a routing group (Magic_zero on Discord)
	Account      bool   `json:"account,omitempty"` // a signed-in account the key may be held to (#905)
	Key          bool   `json:"key,omitempty"`     // one of a provider's keys, by its KeyID
	Plan         string `json:"plan,omitempty"`    // the account's subscription, for the menu's note
	// Decides marks a decision model (Jev, Clef…), which no agent lists:
	// a key held to some models reaches it at /v1/systemone, and another
	// magpie lists it, only when it is picked (Kautism on Discord)
	Decides bool `json:"decides,omitempty"`
}

func withLimits(keys []access.Key) []keyRow {
	now := time.Now()
	out := make([]keyRow, 0, len(keys))
	for _, k := range keys {
		row := keyRow{Key: k, UsedAll: budget.All(k, now)}
		if len(row.UsedAll) > 0 {
			row.Used = &row.UsedAll[0]
		}
		out = append(out, row)
	}
	return out
}

func callerKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/caller-keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		access.MigrateLegacyLANKeyBestEffort()
		keys, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"keys": withLimits(keys), "accountNames": provider.AccountNames()})
	})
	// the models a key may be held to (#882), and the accounts and keys
	// (#905): the routing groups, then each provider's models, then its
	// accounts and keys; a group the key names is its with every member
	mux.HandleFunc("GET /api/caller-keys/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := []keyModelJSON{}
		accounts := []keyModelJSON{}
		for _, e := range provider.Catalog() {
			if e.Group != "" {
				// by the id magpie keeps it by, group/<id>, as a key held to it
				// before names it: a found group's auto/<model> passes for it
				// (gateway's allows), and its name is what the picker shows
				out = append(out, keyModelJSON{ID: provider.GroupPrefix + e.Group, Name: cmp.Or(e.Name, e.Group), Group: true})
				continue
			}
			out = append(out, keyModelJSON{ID: e.Provider.ID + "/" + e.Model, Name: cmp.Or(e.Name, e.Model), Provider: e.Provider.ID, ProviderName: cmp.Or(e.Provider.Name, e.Provider.ID)})
		}
		// the models a routing group listing alone keeps out of the lists,
		// each after its provider's: a key not given the group is listed
		// those it may use (the gateway's keyListed), so the picker offers
		// them to pick one at a time too (luckymanjc on Discord: Codex
		// offered GPT-6-Luna alone)
		for _, s := range provider.GroupStandIns() {
			e := s.Entry
			id := e.Provider.ID + "/" + e.Model
			if slices.ContainsFunc(out, func(m keyModelJSON) bool { return strings.EqualFold(m.ID, id) }) {
				continue
			}
			m := keyModelJSON{ID: id, Name: cmp.Or(e.Name, e.Model), Provider: e.Provider.ID, ProviderName: cmp.Or(e.Provider.Name, e.Provider.ID)}
			at := len(out)
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].Provider == m.Provider {
					at = i + 1
					break
				}
			}
			out = slices.Insert(out, at, m)
		}
		// the decision models the routing groups' pickers offer, each
		// after its provider's chat models: the gateway holds a key to
		// them as to any model (serveSystemOne, and another magpie's list
		// of them), so a key held to some models had no way to be let use
		// one but All models (Kautism on Discord: a key held to two
		// groups, and Clef and Jev listed nowhere)
		for _, e := range provider.OfferedDeciders(nil) {
			id := e.Provider.ID + "/" + e.Model
			if slices.ContainsFunc(out, func(m keyModelJSON) bool { return strings.EqualFold(m.ID, id) }) {
				continue
			}
			m := keyModelJSON{ID: id, Name: cmp.Or(e.Name, e.Model), Provider: e.Provider.ID, ProviderName: cmp.Or(e.Provider.Name, e.Provider.ID), Decides: true}
			at := len(out)
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].Provider == m.Provider {
					at = i + 1
					break
				}
			}
			out = slices.Insert(out, at, m)
		}
		for _, p := range provider.All() {
			if !p.On() {
				continue
			}
			name := cmp.Or(p.Name, p.ID)
			// an account by its stable id, kept through renames, shown by the
			// name the user gave it, else who is signed in; a key by its
			// fingerprint
			for _, a := range p.AccountIDs() {
				accounts = append(accounts, keyModelJSON{ID: p.ID + "/" + a.ID, Name: a.Shown(p), Provider: p.ID, ProviderName: name, Account: !a.Key, Key: a.Key, Plan: a.Plan})
			}
		}
		writeJSON(w, map[string]any{"models": out, "accounts": accounts})
	})
	mux.HandleFunc("POST /api/caller-keys/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in access.Change
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		access.MigrateLegacyLANKeyBestEffort()
		secret, err := access.Update(r.PathValue("action"), in)
		if err != nil {
			fail(w, err)
			return
		}
		keys, err := access.List()
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"keys": withLimits(keys), "accountNames": provider.AccountNames(), "secret": secret})
	})
}
