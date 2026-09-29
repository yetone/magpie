package provider

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
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
		switch p.ID {
		case "cursor":
			live = withoutCursorCapacity(collapseCursorModels(withCursorContexts(live)))
		case "devin":
			live = withDevinContexts(devinCollapse(live, devinCached(), p.Models))
		case "antigravity":
			// after its names are filled in, and so that a family's
			// levels aren't taken off by a known model of its id
			return collapseAntigravityModels(catalog.Decorate(live, known))
		}
		return catalog.Decorate(live, known)
	}
	if signedIn {
		if p.ID == "devin" { // with the variants the user picked
			return withDevinContexts(devinCollapse(known, nil, p.Models))
		}
		return known
	}
	if len(known) == 0 {
		// a plan's models, before its list was fetched
		if ms := p.planModels(nil); len(ms) > 0 {
			return ms
		}
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
	if p.Account != nil && p.Account.models != nil {
		// a plan with no list to ask (ZCode's, WorkBuddy's): its models
		// are the ones it has, and its endpoint's /models isn't one
		return p.Account.models(), nil
	}
	// a vendor with no list to ask (Bedrock's runtime): the preset's
	// models are it, unless the user said where one is or the provider
	// sits at a region that serves one after all
	if pr := Preset(p.Preset); pr != nil && pr.NoList && strings.TrimSpace(p.ModelsURL) == "" && !p.listRegion(pr) {
		return catalog.Chat(p.planModels(nil)), nil
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
	return catalog.Chat(ms), catalog.SaveLive(p.ID, base, ms)
}

// newFetches is when each account with no list from its vendor yet was
// last asked for one by FetchNew.
var newFetches = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// newFetchRetry is how long FetchNew leaves an account whose list it
// couldn't get before asking again.
var newFetchRetry = 10 * time.Minute

// FetchNew asks each signed-in account whose vendor list magpie hasn't
// fetched yet for it, each for at most timeout. Start-up does this for the
// accounts there then; an account signed in while magpie runs (in magpie or
// in the agent's own app) otherwise showed magpie's built-in list, fewer
// models than the vendor serves, until Refresh was clicked (#204). One that
// fails is asked again after newFetchRetry, not each time.
func FetchNew(timeout time.Duration) {
	newFetches.Lock()
	defer newFetches.Unlock()
	for _, p := range All() {
		if p.Account == nil || !p.Ready() {
			continue
		}
		if _, ok := p.Fetched(); ok {
			continue
		}
		if t, ok := newFetches.m[p.ID]; ok && time.Since(t) < newFetchRetry {
			continue
		}
		newFetches.m[p.ID] = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		if _, err := p.Fetch(ctx); err != nil {
			log.Println(p.ID + ": " + err.Error())
		}
		cancel()
	}
}

// fetchOne asks the first endpoint that answers, with p's key.
func (p Provider) fetchOne(ctx context.Context) ([]catalog.Model, string, error) {
	if u := strings.TrimSpace(p.ModelsURL); u != "" {
		// asked where the user said, and nowhere else: the base URLs
		// list nothing, or the wrong thing
		ms, err := catalog.FetchURL(ctx, u, p.Key, p.Chat == "" && p.Responses == "", p.Headers)
		if err != nil {
			return nil, u, err
		}
		return catalog.WithDrawers(p.planModels(ms), catalog.PublicDrawers(ctx, u)), u, nil
	}
	var errs []string
	for _, proto := range p.Speaks() {
		base := p.Base(proto)
		ms, at, err := catalog.FetchAt(ctx, base, p.Key, proto == Anthropic, p.Headers)
		if err == nil {
			if proto != Anthropic {
				base = p.fixV1(base, at)
			}
			// the image models its list leaves out (AIHubMix's gpt-image-2)
			return catalog.WithDrawers(p.planModels(ms), catalog.PublicDrawers(ctx, base)), base, nil
		}
		// Chat and Responses at one base say the same thing
		if !slices.Contains(errs, err.Error()) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) == 0 {
		return nil, "", errorf("%s has no endpoint to ask", p.Name)
	}
	// the endpoints are kept as they were: a vendor with no list (or one
	// that wants what the key can't give) still serves the models typed in
	return nil, "", errorf("%s — type its model ids in by hand, or give the URL its list is at", strings.Join(errs, "; "))
}

// listRegion reports whether the provider sits at one of its preset's
// regions that serves a model list although the preset as a whole has
// none (Region.Lists): Qianfan's pay as you go at the v2 root answers
// /v2/models, while the plans' /tokenplan/ endpoints answer nothing.
func (p Provider) listRegion(pr *PresetDef) bool {
	for _, r := range pr.Regions {
		if r.Lists && p.atRegion(r) {
			return true
		}
	}
	return false
}

// atRegion reports whether the provider sits at a region's endpoints,
// by path — the host may be another (a mirror, a test).
func (p Provider) atRegion(r Region) bool {
	for _, a := range []string{p.Chat, p.Responses, p.Anthropic} {
		for _, b := range []string{r.Chat, r.Responses, r.Anthropic} {
			if a != "" && b != "" && basePath(a) == basePath(b) {
				return true
			}
		}
	}
	return false
}

// basePath is a base URL's path, without scheme or host.
func basePath(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexAny(raw, "/#?"); i >= 0 {
		return raw[i:]
	}
	return ""
}

// planModels keeps a plan's own models of a vendor's list (PresetDef.Only),
// or gives the plan's when the list has none; a plan with models but no
// Only gives them only when there is no list. Any other provider's list is
// as it came.
func (p Provider) planModels(ms []catalog.Model) []catalog.Model {
	pr := Preset(p.Preset)
	if pr == nil || pr.Only == "" && (len(pr.Models) == 0 || len(ms) > 0) {
		return ms
	}
	var out []catalog.Model
	for _, m := range ms {
		if strings.HasPrefix(m.ID, pr.Only) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		for _, id := range pr.Models {
			out = append(out, catalog.Model{ID: id, Name: id})
		}
	}
	for i, m := range out {
		// the vendor's window for its model, as models.dev has it
		if m.Context == 0 {
			out[i].Context = catalog.ContextOf(strings.TrimPrefix(m.ID, pr.Only))
		}
		if m.Output == 0 {
			out[i].Output = catalog.OutputOf(strings.TrimPrefix(m.ID, pr.Only))
		}
	}
	return out
}

// fixV1 adds the /v1 an OpenAI-style base URL was given without, when
// the models were found only under it: base/models didn't answer and
// base/v1/models did. The list is asked for at both, but a request goes
// to the base as written, so base/chat/completions would miss what
// base/v1/chat/completions serves. Both OpenAI URLs that were base are
// set right, and the base the models are at is returned. A base with a
// version in its path (Ark's …/api/plan/v3, Zhipu's …/api/paas/v4) is
// the vendor's API as written and is never given a /v1: no list is asked
// for under one there (catalog.FetchAt), and none is added here.
func (p Provider) fixV1(base, at string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || at != base+"/v1/models" || catalog.Versioned(base) {
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
	old = append(old, catalog.LiveDrawers(p.ID)...)
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
	return catalog.Chat(out), catalog.SaveLive(p.ID, base, out)
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

// Efforts are the reasoning levels the model takes, when known: its own,
// or those the user gave it when it has none known.
func (p Provider) Efforts(model string) []string {
	if all := p.Known(model); len(all) > 0 {
		return all
	}
	return effortsKept(nil, settings.Load().ModelEfforts[p.ID+"/"+model])
}

// Known are the model's own reasoning levels, when known.
func (p Provider) Known(model string) []string {
	for _, m := range p.Available() {
		if m.ID == model {
			return effortsOf(m)
		}
	}
	// one of Devin's variants an agent was set to, which the list offers as
	// its family: the one effort its id runs at, whatever effort is asked
	if p.ID == "devin" {
		if l := devinEffortOf(model); l != "" {
			return []string{l}
		}
	}
	// one of the vendor's own its list leaves out (a preview) or typed in:
	// the vendor's word on it, before the others'
	if e, ok := catalog.ListedBy(p.Catalogs(), model); ok {
		return e
	}
	return borrowedEfforts(model)
}

// Levelless reports whether the model is known to have no reasoning levels
// to pick from, as against not known to have any: its vendor or its maker
// lists it with a thinking switch alone, or nothing (Xiaomi's
// mimo-v2.6-flash), and the user gave it none.
func (p Provider) Levelless(model string) bool {
	if len(p.Efforts(model)) > 0 {
		return false
	}
	if e, ok := catalog.ListedBy(p.Catalogs(), model); ok {
		return len(e) == 0
	}
	e, ok := catalog.ListedBy(makerCatalogs(), model)
	return ok && len(e) == 0
}

// effortsOf is a model's reasoning levels: its vendor's, as models.dev
// lists them, or — for a vendor models.dev doesn't list the model under (a
// custom provider, a proxy) — its maker's, else the ones the others serving
// it give. A model models.dev lists for this vendor without levels takes
// none: the vendor says it has none to pick from.
func effortsOf(m catalog.Model) []string {
	if len(m.Efforts) > 0 || m.Provider != "" {
		return m.Efforts
	}
	return borrowedEfforts(m.ID)
}

// borrowedEfforts are the reasoning levels of a model its provider has no
// word on: its maker's, when a vendor of magpie's presets makes it — none
// for Xiaomi's mimo-v2.6-flash, which takes a thinking switch alone and
// turns away the max resellers list for it (#214) — else those most of the
// providers giving any give it (a Volcengine endpoint's glm-5.3-flash).
func borrowedEfforts(id string) []string {
	if e, ok := catalog.ListedBy(makerCatalogs(), id); ok {
		return e
	}
	return catalog.EffortsOf(id)
}

// makerCatalogs are the models.dev ids of the vendors among the presets
// that make the models they serve, in the presets' order.
var makerCatalogs = sync.OnceValue(func() []string {
	var out []string
	for _, pr := range presets {
		if pr.Kind != KindVendor || pr.Hosts {
			continue
		}
		for _, c := range (Provider{Catalog: pr.Catalog}).Catalogs() {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
})

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
	// Free is set on a model its subscription serves at no cost to it.
	Free bool `json:"free,omitempty"`
}

// Catalog lists the routing groups, then every exposed model of every ready
// provider not kept unlisted. A provider switched off has none in it.
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
		if !p.On() || p.Decides() { // a decision API only routes
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
				Images: images, ImageInput: m.ImageInput, Context: ctx, Output: output, Free: m.Free}
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
		if p, err := Find(pid); err == nil && p.On() {
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
		if !p.On() || p.Decides() {
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
