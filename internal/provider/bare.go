package provider

import (
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Model ids without the provider (settings.BareModelIDs; love1sbug on X):
// a client moved from another proxy (CLIProxyAPI) asks for gpt-5.6-sol,
// not codex/gpt-5.6-sol, and picks its model from /v1/models. The gateway
// has always taken a bare id (GroupFor, then Resolve); with the setting on,
// /v1/models lists each model by its bare id as well, after the
// provider/model ids, which stay as they were. Off by default: a list
// nobody asked to grow keeps its ids.
//
// A bare id is listed only where a request for it goes, as the gateway
// routes it: two providers' gpt-5.6-sol is one row, the routing group magpie
// found for it (its members in the Providers order, failed over between),
// or with found groups off the first provider in the Providers order. A
// bare id that is a catalog id already, or that the gateway would take to
// a model the list doesn't show (a gateway key held to other models, an
// id like anthropic/claude-x that names a provider of the user's), isn't
// listed by it.

// BareModelIDs says whether /v1/models lists models by their bare ids too.
func BareModelIDs() bool { return settings.Shared().BareModelIDs }

// SetBareModelIDs turns settings.BareModelIDs on or off. The list is built
// for each request, so the next one has the bare ids, or goes without
// them; no agent's file names a model by one, so none is written again.
func SetBareModelIDs(on bool) error {
	s := settings.Load()
	if s.BareModelIDs == on {
		return nil
	}
	s.BareModelIDs = on
	return settings.Save(s)
}

// Bare is a row a list adds for a bare id: ID, and Of, the index in the
// list of the entry a request for it reaches.
type Bare struct {
	ID string
	Of int
}

// BareEntries are the rows a list of shown adds for the bare ids, in the
// order the models first appear in shown, each once.
func BareEntries(shown []Entry) []Bare {
	defer Hold()()
	ids := make(map[string]bool, len(shown))
	for _, e := range shown {
		ids[e.ID] = true
	}
	entries := providerEntries()
	all := groupsIn(entries)
	var out []Bare
	seen := map[string]bool{}
	for _, e := range shown {
		m := e.Model
		if e.Group != "" || m == "" || seen[m] || ids[m] {
			continue
		}
		seen[m] = true
		if i := bareTarget(shown, all, entries, m); i >= 0 {
			out = append(out, Bare{ID: m, Of: i})
		}
	}
	return out
}

// bareTarget is the index in shown of the entry a request for the bare id
// reaches: the routing group GroupFor gives it, else the provider's model
// Resolve does; -1 when shown has neither.
func bareTarget(shown []Entry, all []Group, entries []Entry, id string) int {
	if !strings.Contains(id, "/") {
		if gid, ok := groupForIn(all, id); ok {
			for i, e := range shown {
				if e.ID == gid || e.Group != "" && GroupPrefix+e.Group == gid {
					return i
				}
			}
			return -1
		}
	}
	p, model, ok := resolveIn(entries, id)
	if !ok {
		return -1
	}
	for i, e := range shown {
		if e.Group == "" && e.Provider.ID == p.ID && e.Model == model {
			return i
		}
	}
	return -1
}

// BareEntry is the entry of shown a client asking for a model by its bare
// id is told of (GET /v1/models/{id}), its ID the bare id.
func BareEntry(shown []Entry, id string) (Entry, bool) {
	for _, b := range BareEntries(shown) {
		if b.ID == id {
			e := shown[b.Of]
			e.ID = id
			return e, true
		}
	}
	return Entry{}, false
}
