package provider

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

// manyModels is where "expose everything" stops being helpful.
const manyModels = 24

// Available lists every model the vendor is known to serve: the list fetched
// from the vendor itself when there is one, over the models.dev catalog (or,
// for an account, whatever the agent's own sign-in can see).
func (p Provider) Available() []catalog.Model {
	if p.Decides() {
		return p.decideModels()
	}
	signedIn := p.Account != nil && p.Account.models != nil
	var known []catalog.Model
	if signedIn {
		known = p.Account.models()
	} else {
		seen := map[string]bool{}
		for _, id := range p.Catalogs() {
			for _, m := range catalog.Provider(id) {
				if !seen[m.ID] {
					seen[m.ID] = true
					known = append(known, m)
				}
			}
		}
	}
	if live, _, ok := catalog.Live(p.ID); ok {
		if p.ID == "cursor" {
			live = withCursorContexts(live)
		}
		return catalog.Decorate(live, known)
	}
	if signedIn {
		return known
	}
	var out []catalog.Model
	for _, m := range known {
		if !strings.Contains(m.ID, "-exp") && !strings.Contains(m.ID, "preview") {
			out = append(out, m)
		}
	}
	return out
}

func (p Provider) firstCatalog() string {
	if cs := p.Catalogs(); len(cs) > 0 {
		return cs[0]
	}
	return ""
}

// Fetched reports when the vendor's own list was last fetched.
func (p Provider) Fetched() (time.Time, bool) {
	_, t, ok := catalog.Live(p.ID)
	return t, ok
}

// Fetch asks the vendor which models it serves and remembers the answer.
func (p Provider) Fetch(ctx context.Context) ([]catalog.Model, error) {
	if p.Decides() {
		return p.fetchDecide(ctx)
	}
	if p.Account != nil && p.Account.fetch != nil {
		return p.Account.fetch(ctx)
	}
	// Only keys in use. An off key is not asked, and its list does not
	// join the catalog or take capabilities off a key that is on.
	if keys := p.KeysOn(); len(keys) > 1 {
		return p.fetchPerKey(ctx, keys)
	}
	ms, base, err := p.fetchOne(ctx)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(p.ID, base, ms)
}

// fetchOne asks the first endpoint that answers, with p's key.
func (p Provider) fetchOne(ctx context.Context) ([]catalog.Model, string, error) {
	if u := strings.TrimSpace(p.ModelsURL); u != "" {
		// asked where the user said, and nowhere else: the base URLs
		// list nothing, or the wrong thing
		ms, err := catalog.FetchURL(ctx, u, p.Key, p.Chat == "" && p.Responses == "", p.Headers)
		return ms, u, err
	}
	var lastErr error
	for _, proto := range p.Speaks() {
		base := p.Base(proto)
		ms, at, err := catalog.FetchAt(ctx, base, p.Key, proto == Anthropic, p.Headers)
		if err == nil {
			if proto != Anthropic {
				base = p.fixV1(base, at)
			}
			return ms, base, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errorf("%s has no endpoint to ask", p.Name)
	}
	return nil, "", lastErr
}

// fixV1 adds the /v1 an OpenAI-style base URL was given without, when
// the models were found only under it: base/models didn't answer and
// base/v1/models did. The list is asked for at both, but a request goes
// to the base as written, so base/chat/completions would miss what
// base/v1/chat/completions serves. Both OpenAI URLs that were base are
// set right, and the base the models are at is returned.
func (p Provider) fixV1(base, at string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || at != base+"/v1/models" {
		return base
	}
	fixed := base + "/v1"
	f := load()
	for i := range f.Providers {
		q := &f.Providers[i]
		if q.ID != p.ID {
			continue
		}
		changed := false
		for _, u := range []*string{&q.Chat, &q.Responses} {
			if strings.TrimRight(strings.TrimSpace(*u), "/") == base {
				*u, changed = fixed, true
			}
		}
		if changed {
			if err := store(f); err != nil {
				return base
			}
		}
		return fixed
	}
	return base
}

// fetchPerKey asks with each key in turn, at the endpoint it is made for: a
// relay that hands out a key per group lists each group's models to its key
// only. The lists are merged, each model marking the keys that see it. A
// key that can't be asked now keeps the models it saw last time.
func (p Provider) fetchPerKey(ctx context.Context, keys []KeyAccount) ([]catalog.Model, error) {
	old, _, _ := catalog.Live(p.ID)
	var out []catalog.Model
	at := map[string]int{}
	add := func(m catalog.Model, id string) {
		i, ok := at[m.ID]
		if !ok {
			m.Keys = nil
			at[m.ID], i = len(out), len(out)
			out = append(out, m)
		} else {
			out[i].ImageInput = sharedImageInput(out[i].ImageInput, m.ImageInput)
			out[i].Images = out[i].Images && m.Images
		}
		if !slices.Contains(out[i].Keys, id) {
			out[i].Keys = append(out[i].Keys, id)
		}
	}
	var base string
	var lastErr error
	for _, k := range keys {
		id := keyID(k.Key)
		q := p.WithKey(k)
		ms, b, err := q.fetchOne(ctx)
		if err != nil {
			lastErr = err
			for _, m := range old {
				if slices.Contains(m.Keys, id) {
					add(m, id)
				}
			}
			continue
		}
		if base == "" {
			base = b
		}
		for _, m := range ms {
			add(m, id)
		}
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, catalog.SaveLive(p.ID, base, out)
}

// An explicit text-only answer wins. Without one, an unknown answer stays
// unknown; the caller can still use the catalog's image capability estimate.
func sharedImageInput(a, b *bool) *bool {
	if a != nil && !*a {
		return a
	}
	if b != nil && !*b {
		return b
	}
	if a == nil || b == nil {
		return nil
	}
	return a
}

// Serves reports whether key k can be asked for model: false only when the
// vendor's lists say another of the provider's keys sees it and k doesn't.
func (p Provider) Serves(k KeyAccount, model string) bool {
	live, _, ok := catalog.Live(p.ID)
	if !ok {
		return true
	}
	for _, m := range live {
		if m.ID == model {
			return len(m.Keys) == 0 || slices.Contains(m.Keys, keyID(k.Key))
		}
	}
	return true
}

// Exposed lists the models magpie offers to agents for this provider: the
// user's picks; else the preset's; else everything, when that is few.
func (p Provider) Exposed() []catalog.Model {
	avail := p.Available()
	byID := make(map[string]catalog.Model, len(avail))
	for _, m := range avail {
		byID[m.ID] = m
	}
	pick := func(ids []string) []catalog.Model {
		out := make([]catalog.Model, 0, len(ids))
		for _, id := range ids {
			if m, ok := byID[id]; ok {
				out = append(out, m)
			} else {
				out = append(out, catalog.Model{ID: id, Name: id, Provider: p.firstCatalog()})
			}
		}
		return out
	}
	if len(p.Models) > 0 {
		return pick(p.Models)
	}
	if len(avail) <= manyModels {
		return avail
	}
	// More than an agent's picker wants. Show the first slice of the
	// vendor's own list — theirs run newest first — and let the user pick
	// from the rest; nothing here is compiled in.
	return avail[:manyModels]
}

// RejectsTemperature reports whether the model is known to refuse
// temperature and top_p. The answer comes from the catalog — models.dev
// plus the vendor's own list — so a model released after this binary was
// built is handled without a code change.
func (p Provider) RejectsTemperature(model string) bool {
	for _, m := range p.Available() {
		if m.ID == model && m.Temperature != nil {
			return !*m.Temperature
		}
	}
	return false
}

// Efforts are the reasoning levels the model takes, when known.
func (p Provider) Efforts(model string) []string {
	for _, m := range p.Available() {
		if m.ID == model {
			return effortsOf(m)
		}
	}
	return catalog.EffortsOf(model)
}

// effortsOf is a model's reasoning levels: its vendor's, as models.dev
// lists them, or — for a vendor models.dev doesn't list the model under (a
// custom provider, a proxy) — the ones the others serving it give. A model
// models.dev lists for this vendor without levels takes none: the vendor
// says it has none to pick from.
func effortsOf(m catalog.Model) []string {
	if len(m.Efforts) > 0 || m.Provider != "" {
		return m.Efforts
	}
	return catalog.EffortsOf(m.ID)
}

// Chosen reports whether a model is exposed.
func (p Provider) Chosen(id string) bool {
	for _, m := range p.Exposed() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ---- the magpie catalog ------------------------------------------------------
//
// Agents see one flat list of models across every provider, each spelled
// "provider/model" so nothing ever clashes. The bare model id works too
// when only one provider serves it.

// Entry is one model as the agents see it.
type Entry struct {
	ID         string   `json:"id"`                // what the agent sends magpie
	Model      string   `json:"model"`             // what magpie sends the vendor
	Name       string   `json:"name"`              // the user's name for it, when they gave one (SetModelName)
	Default    string   `json:"default,omitempty"` // the model's own name, when the user gave it another
	Efforts    []string `json:"efforts,omitempty"`
	Provider   Provider `json:"-"`                // a group's: its first member's
	Group      string   `json:"group,omitempty"`  // set on a routing group (group.go)
	Icons      []string `json:"-"`                // a group's: its providers' icons, one per provider
	Images     bool     `json:"images,omitempty"` // takes images as input (a group's: every member does)
	ImageInput *bool    `json:"-"`                // explicit answer, nil when unknown
	// Context is the tokens a prompt may hold, when known (a group's: the
	// least of its members')
	Context int `json:"context,omitempty"`
	// Output is the most tokens a reply may hold, when known (a group's:
	// the least of its members')
	Output int `json:"output,omitempty"`
	// Family is the provider's or group's tag (see Visible).
	Family string `json:"family,omitempty"`
}

// Catalog lists the routing groups, then every exposed model of every ready
// provider not kept unlisted.
func Catalog() []Entry {
	entries := providerEntries()
	out := groupEntries(entries)
	for _, e := range entries {
		if !e.Provider.Unlisted {
			out = append(out, e)
		}
	}
	return out
}

// Served is the catalog with the unlisted providers' models as well: every
// model a routing group can be made of, or a request can name.
func Served() []Entry {
	entries := providerEntries()
	return append(groupEntries(entries), entries...)
}

// providerEntries is the catalog without its groups.
func providerEntries() []Entry {
	var out []Entry
	s := settings.Load()
	for _, p := range All() {
		if !p.Ready() || p.Decides() { // a decision API only routes
			continue
		}
		for _, m := range p.Exposed() {
			// a vendor models.dev doesn't list (a custom provider, a proxy)
			// serves models it knows from others
			ctx := m.Context
			if ctx == 0 {
				ctx = catalog.ContextOf(m.ID)
			}
			if n := p.ContextOf(m.ID); n > 0 {
				ctx = n
			}
			output := m.Output
			if output == 0 {
				output = catalog.OutputOf(m.ID)
			}
			images := m.Images || catalog.SeesImages(m.ID)
			if m.ImageInput != nil {
				images = *m.ImageInput
			}
			e := Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Family: p.Family, Name: m.Name, Efforts: effortsOf(m), Provider: p,
				Images: images, ImageInput: m.ImageInput, Context: ctx, Output: output}
			if n, ok := modelNameIn(s.ModelNames, p.ID, m.ID); ok {
				e.Name, e.Default = n, m.Name
			}
			e.Efforts = effortsKept(e.Efforts, s.ModelEfforts[e.ID])
			out = append(out, e)
		}
	}
	return out
}

// Resolve maps an id an agent sent to a provider and the vendor's model id.
// It accepts catalog ids, "provider/model" for any model (exposed or not),
// and the bare model id when exactly one provider serves it.
// A group's id resolves to its first member.
func Resolve(id string) (Provider, string, bool) {
	// Claude Code's mark for a model with a 1M window; it drops it before
	// asking, but a value in its settings still has it
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if strings.HasPrefix(id, GroupPrefix) {
		for _, e := range Catalog() {
			if e.ID == id {
				return e.Provider, e.Model, true
			}
		}
		return Provider{}, "", false
	}
	return resolveIn(providerEntries(), id)
}

func resolveIn(entries []Entry, id string) (Provider, string, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e.Provider, e.Model, true
		}
	}
	if pid, model, ok := strings.Cut(id, "/"); ok {
		if p, err := Find(pid); err == nil && p.Ready() {
			return *p, model, true
		}
	}
	var hits []Entry
	for _, e := range entries {
		if e.Model == id {
			hits = append(hits, e)
		}
	}
	if len(hits) >= 1 {
		return hits[0].Provider, hits[0].Model, true
	}
	// not exposed, but some provider lists it
	var found []Provider
	for _, p := range All() {
		if !p.Ready() || p.Decides() {
			continue
		}
		for _, m := range p.Available() {
			if m.ID == id {
				found = append(found, p)
				break
			}
		}
	}
	if len(found) == 1 {
		return found[0], id, true
	}
	return Provider{}, "", false
}

// IDs lists the catalog ids, for error messages.
func IDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}

// ContextOf is the context the user set for the provider's model: its own,
// else the provider's "*"; 0 when none is set.
func (p Provider) ContextOf(model string) int {
	if n := p.Contexts[model]; n > 0 {
		return n
	}
	return p.Contexts["*"]
}
