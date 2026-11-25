package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
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

// keyMCPJSON is an MCP server a gateway key may be held to, with its
// tools, as its picker lists it.
type keyMCPJSON struct {
	Name  string   `json:"name"`
	Title string   `json:"title,omitempty"`
	Tools []string `json:"tools"`
	Own   bool     `json:"own,omitempty"` // one of magpie's own
}

func withLimits(keys []access.Key) []keyRow {
	now := time.Now()
	out := make([]keyRow, 0, len(keys))
	all := budget.AllOf(keys, now)
	for i, k := range keys {
		row := keyRow{Key: k, UsedAll: all[i]}
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
		writeJSON(w, map[string]any{"keys": withLimits(keys), "accountNames": provider.AccountNames(), "accountGroups": accountGroupsJSON()})
	})
	// the subscription accounts' windows a key's limit can follow
	// (ljayic on X): each one's 5 hours, week or month, from the cards as
	// last read; an account not read yet is read meanwhile, waited for a
	// few seconds at most
	mux.HandleFunc("GET /api/caller-keys/account-windows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		out := provider.KeyWindows(ctx, time.Now())
		if out == nil {
			out = []provider.KeyWindow{}
		}
		writeJSON(w, map[string]any{"windows": out})
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
		// the decision groups, after the chat groups: Catalog keeps them
		// out as no agent chats with one, but the gateway gives a key
		// naming one its models at /v1/systemone (serveDecisionGroup's
		// groupGiven), so a key held to some models had no way to be let
		// use one but All models (Kautism on Discord: a group of Clef and
		// Jev, not in the key's list). Only those the gateway serves now:
		// on, and the user's own while their groups are served.
		groupsEnd := 0
		for groupsEnd < len(out) && out[groupsEnd].Group {
			groupsEnd++
		}
		for _, g := range provider.Groups() {
			if g.Hidden || g.Disabled || !g.Decides() {
				continue
			}
			if _, _, ok := provider.FindGroup(provider.GroupPrefix + g.ID); !ok {
				continue
			}
			id := provider.GroupPrefix + g.ID
			if slices.ContainsFunc(out, func(m keyModelJSON) bool { return strings.EqualFold(m.ID, id) }) {
				continue
			}
			out = slices.Insert(out, groupsEnd, keyModelJSON{ID: id, Name: cmp.Or(g.Name, g.ID), Group: true, Decides: true})
			groupsEnd++
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
		// the image and video models, which Catalog leaves out as agents
		// don't chat with them: /v1/images and /v1/videos hold a key to its
		// models as chat does (keyHolds), so a key held to some models had
		// no way to be let draw but All models (aipha2025 on X)
		for _, p := range provider.All() {
			if !p.On() {
				continue
			}
			var media []catalog.Model
			media = append(media, gateway.Drawers(p)...)
			media = append(media, gateway.Videomakers(p)...)
			for _, e := range media {
				id := p.ID + "/" + e.ID
				if slices.ContainsFunc(out, func(m keyModelJSON) bool { return strings.EqualFold(m.ID, id) }) {
					continue
				}
				m := keyModelJSON{ID: id, Name: cmp.Or(e.Name, e.ID), Provider: p.ID, ProviderName: cmp.Or(p.Name, p.ID)}
				at := len(out)
				for i := len(out) - 1; i >= 0; i-- {
					if out[i].Provider == m.Provider {
						at = i + 1
						break
					}
				}
				out = slices.Insert(out, at, m)
			}
		}
		// a provider that serves no model now (a plugin still starting, a
		// list not fetched yet) can still be allowed whole, <provider>/*,
		// for the models it serves later
		listed := map[string]bool{}
		for _, m := range out {
			listed[m.Provider] = true
		}
		empty := []keyModelJSON{}
		for _, p := range provider.All() {
			if !p.On() {
				continue
			}
			name := cmp.Or(p.Name, p.ID)
			if !listed[p.ID] {
				empty = append(empty, keyModelJSON{ID: p.ID + "/*", Name: name, Provider: p.ID, ProviderName: name})
			}
			// an account by its stable id, kept through renames, shown by the
			// name the user gave it, else who is signed in; a key by its
			// fingerprint
			for _, a := range p.AccountIDs() {
				accounts = append(accounts, keyModelJSON{ID: p.ID + "/" + a.ID, Name: a.Shown(p), Provider: p.ID, ProviderName: name, Account: !a.Key, Key: a.Key, Plan: a.Plan})
			}
		}
		// the account groups a key's accounts may name, "@<id>" or "!@<id>"
		// (LeiWang_2000 on X), each with its accounts
		writeJSON(w, map[string]any{"models": out, "accounts": accounts, "providers": empty, "groups": accountGroupsJSON()})
	})
	// the MCP servers and tools a key may be held to (wakaka on Discord):
	// magpie's own, then the Library's, each with its tools by name
	mux.HandleFunc("GET /api/caller-keys/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := []keyMCPJSON{}
		for _, o := range gateway.OwnMCPTools(r.Context()) {
			m := keyMCPJSON{Name: o.Name, Title: o.Title, Tools: o.Tools, Own: true}
			if m.Tools == nil {
				m.Tools = []string{}
			}
			out = append(out, m)
		}
		for _, s := range library.KeyMCPServers() {
			m := keyMCPJSON{Name: s.Name, Title: s.Title, Tools: []string{}}
			for _, t := range s.Tools {
				m.Tools = append(m.Tools, t.Name)
			}
			out = append(out, m)
		}
		writeJSON(w, map[string]any{"servers": out})
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
		writeJSON(w, map[string]any{"keys": withLimits(keys), "accountNames": provider.AccountNames(), "accountGroups": accountGroupsJSON(), "secret": secret})
	})
	// a provider's side of the keys' models (justforlxz on X): the keys
	// that may use a model, each with how far it reaches the provider, and
	// ticking or unticking some of them, written to each key's own models
	mux.HandleFunc("GET /api/provider-keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pid := r.URL.Query().Get("provider")
		keys, err := access.ProviderKeys(pid, groupRefs(pid))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"keys": keys})
	})
	mux.HandleFunc("POST /api/provider-keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string   `json:"provider"`
			Add      []string `json:"add"`
			Remove   []string `json:"remove"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		access.MigrateLegacyLANKeyBestEffort()
		// a key on every model, unticked, keeps every other provider
		var ids []string
		for _, p := range provider.All() {
			ids = append(ids, p.ID)
		}
		groups := groupRefs(in.Provider)
		if err := access.SetProviderKeys(in.Provider, in.Add, in.Remove, ids, groups); err != nil {
			fail(w, err)
			return
		}
		keys, err := access.ProviderKeys(in.Provider, groups)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"keys": keys})
	})
	// the account groups a key's accounts may name, "@<id>" or "!@<id>"
	// (LeiWang_2000 on X): save adds one (no id) or changes it, remove
	// takes one off unless a key names it
	mux.HandleFunc("GET /api/account-groups", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		gs, err := access.AccountGroups()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"groups": gs})
	})
	mux.HandleFunc("POST /api/account-groups/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in access.AccountGroup
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "save":
			_, err = access.SaveAccountGroup(in)
		case "remove":
			err = access.RemoveAccountGroup(in.ID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		gs, err := access.AccountGroups()
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"groups": gs})
	})
}

// accountGroupsJSON are the account groups for the keys' rows, none when
// they can't be read (a key naming one still holds: identityOf).
func accountGroupsJSON() []access.AccountGroup {
	gs, err := access.AccountGroups()
	if err != nil || gs == nil {
		return []access.AccountGroup{}
	}
	return gs
}

// groupRefs are the routing groups as they reach provider pid, for its
// side of the keys: a key that names a group is sent to every member of
// it, so one that may have a member of pid's reaches pid through it. A
// group reaches pid when a member it names is pid's, a group in it does,
// a pattern of its may match a model of pid's (a regular expression may
// match any), or a member it resolves to now is pid's.
func groupRefs(pid string) []access.GroupRef {
	pid = strings.TrimSpace(pid)
	all := provider.Groups()
	byID := map[string]provider.Group{}
	for _, g := range all {
		byID[strings.ToLower(g.ID)] = g
	}
	find := provider.GroupFinder()
	var reaches func(g provider.Group, seen map[string]bool) bool
	reaches = func(g provider.Group, seen map[string]bool) bool {
		if seen[strings.ToLower(g.ID)] {
			return false
		}
		seen[strings.ToLower(g.ID)] = true
		for _, m := range slices.Concat(g.Members, g.Matched) {
			if id, ok := strings.CutPrefix(m, provider.GroupPrefix); ok {
				if in, ok := byID[strings.ToLower(id)]; ok && reaches(in, seen) {
					return true
				}
				continue
			}
			if head, _, _ := strings.Cut(m, "/"); strings.EqualFold(strings.TrimSpace(head), pid) {
				return true
			}
		}
		for _, p := range g.Match {
			head, _, ok := strings.Cut(p, "/")
			if access.IsPattern(p) || !ok || access.ModelAllowed([]string{head}, pid) {
				return true
			}
		}
		if _, ms, ok := find(provider.GroupPrefix + g.ID); ok {
			for _, m := range ms {
				if strings.EqualFold(m.Provider.ID, pid) {
					return true
				}
			}
		}
		return false
	}
	out := make([]access.GroupRef, 0, len(all))
	for _, g := range all {
		refs := []string{provider.GroupPrefix + g.ID}
		if ref := g.Ref(); ref != refs[0] {
			refs = append(refs, ref)
		}
		out = append(out, access.GroupRef{Refs: refs, Found: g.Auto || strings.HasPrefix(g.ID, "auto-"), Reaches: reaches(g, map[string]bool{})})
	}
	return out
}
