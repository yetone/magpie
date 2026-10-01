package provider

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
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
	if live, _, ok := p.live(); ok {
		if p.Account != nil && p.Account.unusable != nil {
			// a model the list offers that the account was refused
			// (Copilot's, copilot_refused.go)
			live = slices.DeleteFunc(slices.Clone(live), func(m catalog.Model) bool { return p.Account.unusable(m.ID) })
		}
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
	if p.IsAzure() {
		// an Azure resource serves its deployments alone, named as the
		// user likes: the catalog only names the ones its list gave
		return nil
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
	_, t, ok := p.live()
	return t, ok
}

// Listed is when the models listed now were had from the vendor: fetched
// for a built-in, listed by its plugin for a plugin's.
func (p Provider) Listed() (time.Time, bool) {
	if p.IsPlugin() {
		return plugin.ListedAt()
	}
	return p.Fetched()
}

// live is the list last fetched from the vendor. A plugin's provider has
// none: its models are what the plugin lists now, and a built-in moved
// onto it left its own last list under the same id.
func (p Provider) live() ([]catalog.Model, time.Time, bool) {
	if p.IsPlugin() {
		return nil, time.Time{}, false
	}
	return catalog.Live(p.ID)
}

// Fetch asks the vendor which models it serves and remembers the answer.
func (p Provider) Fetch(ctx context.Context) ([]catalog.Model, error) {
	ctx = p.Via(ctx)
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
// couldn't get before asking again. Short: until it has its list the
// account offers magpie's fallback (Kiro's Auto alone), and one try that
// failed — the first after an update, cut short by a page's 8 seconds, or
// made before the network was up — left it so for ten minutes, while only
// the Providers page asked again (#422: Auto alone until magpie was
// restarted by hand).
var newFetchRetry = time.Minute

// fetchingNew is set while a FetchNewSoon runs; newSoonAt is when the last
// one started.
var (
	fetchingNew atomic.Bool
	newSoonAt   atomic.Int64
)

// newSoonEvery is how often FetchNewSoon starts at most.
var newSoonEvery = 15 * time.Second

// FetchNewSoon is FetchNew in the background, for a page that shouldn't
// wait on vendors (the panel, whose model picker otherwise kept an
// account's fallback list until the Providers page was opened). It does
// nothing while one runs or within newSoonEvery of the last.
func FetchNewSoon(timeout time.Duration) {
	now := time.Now().UnixNano()
	if now-newSoonAt.Load() < int64(newSoonEvery) || !fetchingNew.CompareAndSwap(false, true) {
		return
	}
	newSoonAt.Store(now)
	go func() {
		defer fetchingNew.Store(false)
		FetchNew(timeout)
	}()
}

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
		// a plugin's accounts were listed with the plugin's providers
		if _, ok := p.Listed(); ok {
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
		ms, err := catalog.FetchURL(ctx, u, p.Key, p.Chat == "" && p.Responses == "", p.listHeaders())
		if err != nil {
			return nil, u, err
		}
		return catalog.WithDrawers(p.planModels(ms), catalog.PublicDrawers(ctx, u)), u, nil
	}
	if p.IsAzure() && (p.Chat != "" || p.Responses != "") {
		// its deployments, asked with the key in api-key
		return p.azureModels(ctx)
	}
	var errs []string
	for _, proto := range p.Speaks() {
		base := p.Base(proto)
		ms, at, err := catalog.FetchAt(ctx, base, p.Key, proto == Anthropic, p.listHeaders())
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

// planModels leaves out the models a vendor serves its own client alone
// (PresetDef.Drop), and keeps a plan's own models of a vendor's list (PresetDef.Only),
// or gives the plan's when the list has none; a plan with models but no
// Only gives them only when there is no list. Any other provider's list is
// as it came.
func (p Provider) planModels(ms []catalog.Model) []catalog.Model {
	pr := Preset(p.Preset)
	if pr != nil && pr.Drop != "" {
		kept := ms[:0:0]
		for _, m := range ms {
			if !strings.HasSuffix(m.ID, pr.Drop) {
				kept = append(kept, m)
			}
		}
		ms = kept
	}
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
	f, err := read()
	if err != nil {
		return base
	}
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
	picks := p.Models
	if _, _, ok := p.live(); ok && p.Account != nil && p.Account.unusable != nil {
		// A Copilot account is served what its list offers it, less what
		// it was refused: a pick its list doesn't have (#371: gpt-6-luna,
		// not in a Student plan's) or that it was refused is left out;
		// with none left, as if none were picked.
		picks = slices.DeleteFunc(slices.Clone(picks), func(id string) bool { _, ok := byID[id]; return !ok })
	}
	if len(picks) > 0 {
		return pick(picks)
	}
	// another magpie's list is already the models its user exposed
	if len(avail) <= manyModels || p.IsRemoteMagpie() {
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

// ListPrice is a model's list price as its vendor's models.dev entry gives
// it, else as its maker's does (#224): a subscription (Codex's ChatGPT
// account, Copilot) or a relay with no models.dev id of its own is priced
// at gpt-6-astra's or gemini-3.8-flash's maker's price, as a Claude
// account is at Anthropic's.
func (p Provider) ListPrice(model string) (catalog.Price, bool) {
	for _, m := range pricedNames(model) {
		if pr, ok := catalog.PricedBy(p.Catalogs(), m); ok {
			return pr, true
		}
		if pr, ok := catalog.PricedBy(makerCatalogs(), m); ok {
			return pr, true
		}
	}
	return catalog.Price{}, false
}

// MakerPrice is a model's list price as the first vendor among the presets
// that makes the models it serves lists it; for a call whose provider has
// gone since.
func MakerPrice(model string) (catalog.Price, bool) {
	for _, m := range pricedNames(model) {
		if pr, ok := catalog.PricedBy(makerCatalogs(), m); ok {
			return pr, true
		}
	}
	return catalog.Price{}, false
}

// EffectivePrice is what a call to a provider's model costs the user: the
// price they set for that model, or for every model of that provider, or for
// that model from any provider (settings' ModelPrices), else the provider's own list price, else its
// maker's. The second return is false only when no price is known at all,
// which is not the same as a price of zero: that one is set, deliberately.
//
// A price here is the provider's tariff, not the model's: the same model
// through two relays is two prices, and neither is what models.dev lists.
func EffectivePrice(providerID, model string) (catalog.Price, bool) {
	return EffectivePriceIn(settings.Load(), providerID, model)
}

// EffectivePriceIn is EffectivePrice at the settings given, for a caller
// pricing a whole list of models at one read of the file rather than one
// read per model: every model of that list is priced at the same copy, so
// the list is one snapshot of the prices rather than a reading per row, and
// a price changed while it is read takes effect in the next call.
func EffectivePriceIn(s settings.Settings, providerID, model string) (catalog.Price, bool) {
	// A price is keyed by the id the provider has now, so one written before
	// a rename is read under the id it has. Only an id, or one the provider
	// was renamed from, resolves; anything else is looked up as given, so a
	// key under a display name counts only for a caller naming that name
	// too. Nothing writes such a key — SetModelPrice re-keys the way
	// SetModelName does — which is what keeps the two from drifting.
	id := providerID
	p, known := byIDOrWas(providerID)
	if known {
		id = p.ID
	}
	// then what they said the model costs from any provider (*/model):
	// still the user's word, so before any list price
	for _, key := range [...]string{id + "/" + model, id + "/*", AnyPriceKey(model)} {
		if m, ok := s.ModelPrices[key]; ok {
			if pr, bad := m.Price(); bad == "" {
				return pr, true
			}
		}
	}
	if known {
		if pr, ok := p.ListPrice(model); ok {
			return pr, true
		}
	}
	return MakerPrice(model)
}

// byIDOrWas is the provider with that id, else the one it was renamed from.
// Find would answer a display name as well, which is not how a price or a
// reply limit is keyed: both are stored under the id the provider has now, and
// both outlive the provider they were set for, so the spelling the user typed
// is the one that has to be checked before a name is resolved at all.
func byIDOrWas(id string) (Provider, bool) {
	all := All()
	if p, ok := find(all, id); ok {
		return p, true
	}
	for _, p := range all {
		if slices.Contains(p.Was, id) {
			return p, true
		}
	}
	return Provider{}, false
}

// grokEffort is a reasoning effort a Grok id is named at ("grok-4.7-low",
// "grok-4.7-xhigh"): the levels Grok's own model list gives grok-4.7, the
// words Cursor spells its ids in. A fast one ("grok-4.7-low-fast") doesn't
// match: fast is a model of its own (cursor_models.go), and no catalog
// prices it.
var grokEffort = regexp.MustCompile(`^((?:.*/)?grok-[0-9][^/]*?)-(?:minimal|low|medium|high|xhigh|extra-high)$`)

// pricedNames are the ids a model is priced by, in order: its own, then,
// for a Grok id named at an effort, the model it is that effort of — the
// same model, at the same price (#224). Codex Auto Review uses GPT-5.6 Luna
// according to OpenAI's rate card (2026-09-30):
// https://help.openai.com/en/articles/11481834-chatgpt-rate-card-business-enterpriseedu-credit-based-pricing
// This is a list-price estimate, not evidence of a response's served model.
// Other names without catalog prices stay unpriced (grok-4.7-build, grok-4.7-mini,
// grok-4.7-fast).
func pricedNames(model string) []string {
	out := []string{model}
	if model == "codex-auto-review" {
		out = append(out, "gpt-5.6-luna")
	}
	if m := grokEffort.FindStringSubmatch(strings.ToLower(strings.TrimSpace(model))); m != nil {
		out = append(out, m[1])
	}
	return out
}

// PricedName is the catalog ID used for a list-price estimate. Prefer a
// directly listed model before falling back to a documented alias.
func PricedName(model string) string {
	n := pricedNames(model)
	for _, name := range n {
		if _, ok := catalog.PricedBy(makerCatalogs(), name); ok {
			return name
		}
	}
	return n[len(n)-1]
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
	// Free is set on a model its subscription serves at no cost to it.
	Free bool `json:"free,omitempty"`
	// Shared are a group's levels its members have in common: its Efforts,
	// unless the group names its own (Group.Levels).
	Shared []string `json:"-"`
	// Reasoning is set on a model that thinks, levels or not: one with a
	// thinking switch alone has it and no Efforts (a group's: every
	// member thinks).
	Reasoning bool `json:"reasoning,omitempty"`
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

// Unlisted are the models Served has and Catalog doesn't: those of the
// providers kept for routing groups (Provider.Unlisted), which agents
// aren't offered.
func Unlisted() []Entry {
	var out []Entry
	for _, e := range providerEntries() {
		if e.Provider.Unlisted {
			out = append(out, e)
		}
	}
	return out
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
			out = append(out, entryFor(p, m, s))
		}
	}
	return out
}

// entryFor is one of a provider's models as the catalog carries it: the
// window and the reply limit a request on it is routed and metered against,
// with what the user set taken over the vendor's list and models.dev.
func entryFor(p Provider, m catalog.Model, s settings.Settings) Entry {
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
	if n := outputOf(s, p.ID, m.ID); n > 0 {
		output = n
	}
	// an agent asks for the reply limit it is told, and Command Code
	// refuses one above its own
	if p.ID == CommandCodePlanID && output > CommandCodeMaxOutput {
		output = CommandCodeMaxOutput
	}
	images := m.Images || catalog.SeesImages(m.ID)
	if m.ImageInput != nil {
		images = *m.ImageInput
	}
	images, imageInput := ApplyImage(p.ID, m.ID, images, m.ImageInput)
	e := Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Family: p.Family, Name: m.Name, Efforts: effortsOf(m), Provider: p,
		Images: images, ImageInput: imageInput, Context: ctx, Output: output, Free: m.Free}
	if n, ok := modelNameIn(s.ModelNames, p.ID, m.ID); ok {
		e.Name, e.Default = n, m.Name
	}
	// a model that thinks still does with the levels the user kept or
	// none at all; one its source says nothing of thinks as most of the
	// providers serving it say (#402)
	e.Reasoning = m.Reasoning || len(e.Efforts) > 0 || catalog.Thinks(m.ID)
	e.Efforts = effortsKept(e.Efforts, s.ModelEfforts[e.ID])
	return e
}

// EntryOf is the model as the agents see it, by the id they ask for. It is
// the catalog's own entry, so the window and reply limit it carries are the
// ones a request is routed and metered against, with whatever the user set
// already applied.
func EntryOf(id string) (Entry, bool) {
	return entryIn(Catalog(), id)
}

// ServedEntryOf is the model a limit may be set on, by the id the user
// typed: any model its provider serves, not only the catalog's own entry.
// The setters ask serves — the provider's own models deciding, switched off
// or not — so a provider kept unlisted, still served through a routing
// group, takes a window and a reply limit all the same, and a provider
// switched off is served again as soon as it is on, keeping what it was
// given. Both keep it in the entry they answer with, so a query that said
// "is not a model this provider serves" about such an id would contradict
// the command run right after it, which stores one.
func ServedEntryOf(id string) (Entry, bool) {
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return Entry{}, false
	}
	p, err := Find(pid)
	if err != nil || !p.serves(model) {
		return Entry{}, false
	}
	s := settings.Load()
	for _, ms := range [][]catalog.Model{p.Available(), p.Exposed()} {
		for _, m := range ms {
			if m.ID == model {
				return entryFor(*p, m, s), true
			}
		}
	}
	return Entry{}, false
}

func entryIn(entries []Entry, id string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
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

// outputOf is the most a reply of a model may hold, as the user said it: the
// model's own, else the one given for every model of its provider, else 0 for
// the vendor's own list and models.dev to answer. It mirrors ContextOf, which
// the user sets on the provider itself.
func outputOf(s settings.Settings, providerID, model string) int {
	if n := s.ModelOutputs[providerID+"/"+model]; n > 0 {
		return n
	}
	return s.ModelOutputs[providerID+"/*"]
}

// SetModelOutput is the most a reply of a provider's model may hold, in
// tokens, over what the vendor's list and models.dev say; 0 takes the user's
// away, which DropModelOutput does. The id is spelled "provider/model", or
// "provider/*" for every model of that provider; setting one has to name a
// model the provider serves, as one no lookup would ever match reads as set
// and is not. A removal may name a model that has since gone, or a provider
// that has: that is the entry left to be cleared.
//
// The agents are told of it (catalog.Touched), as for any change of the
// catalog: a reply limit is a number they keep in files of their own — Pi's
// maxTokens, OpenCode's limit — and read at start-up, so one only the
// gateway knew would leave every agent a reply limit behind.
func SetModelOutput(id string, n int) error {
	if n < 0 {
		return errorf("an output limit is a number of tokens, not %d", n)
	}
	if n == 0 {
		// a removal is not this function's own work: it has to reach an
		// entry whose provider is gone, and resolving the ref is the very
		// step that refuses one
		_, _, err := DropModelOutput(id)
		return err
	}
	p, model, err := splitRef(id)
	if err != nil {
		return err
	}
	// the id given is not always the provider's own: an agent still
	// running on a config written before a rename names its provider by
	// the id the provider had, and Find takes that id, so a key under it
	// is a reply limit no lookup will ever match — every read asks for the
	// id the provider has now (outputOf, entryFor, the gateway's /models).
	// It would sit in the settings reading as set and answer no reply ever,
	// and a removal would clear an entry nobody reads instead of the one in
	// force. The key is the provider's id as it is now, as every other
	// per-model setter's is.
	key := p.ID + "/" + model
	if err := settings.CheckModelKey("an output limit", key); err != nil {
		return err
	}

	// a limit for a model the provider does not serve is one that never
	// applies and nothing later says so; the same refusal `magpie model
	// name` makes for the same id.
	if model != "*" && !p.serves(model) {
		return errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	s := settings.Load()
	if s.ModelOutputs == nil {
		s.ModelOutputs = map[string]int{}
	}
	s.ModelOutputs[key] = n
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// DropModelOutput takes a user's reply limit away under the key it is
// stored at, and hands that key back, so a caller left without a provider
// to name it by can still say which entry it cleared. It does not ask
// whether the provider is still there: a reply limit outlives the provider
// it was set for, and that provider can be deleted — the entry then sits in
// the settings answering for a model nothing serves, and a removal that
// resolved the provider first would leave the user no way to take it away,
// the file being the only other place it is in. Which key that is comes
// from the spelling as given before anything is looked up (outputKeyOf),
// so that a provider which has since taken the name of one that is gone is
// not the one the entry is cleared under. A key holding no limit is no
// error: there is nothing there to take away, which is the state the entry is
// left in either way, and writing it out again would rewrite the settings and
// every agent's model lists over a limit that did not change.
//
// It reports whether there was a limit there to take away, as DropModelPrice
// does, and by neither saving nor telling the agents when there was none:
// only the caller can put that to the user, and a ✓ over a limit that was
// never set says the model had one.
func DropModelOutput(id string) (string, bool, error) {
	key, err := outputKeyOf(id)
	if err != nil {
		return "", false, err
	}
	if err := settings.CheckModelKey("an output limit", key); err != nil {
		return "", false, err
	}
	s := settings.Load()
	if _, ok := s.ModelOutputs[key]; !ok {
		return key, false, nil
	}
	delete(s.ModelOutputs, key)
	if err := settings.Save(s); err != nil {
		return "", false, err
	}
	// the agents are told as they are of a limit being set: the number they
	// keep in files of their own goes back to the vendor's list only if
	// they hear of it
	catalog.Touched()
	return key, true, nil
}

// outputKeyOf is the "provider/model" a caller spelled, as the key the reply
// limits are written under, and what the spelling has to be for it to name
// a model at all. It is half of splitRef, which goes on to resolve the
// provider's own id: a limit set by a name the provider is listed under
// needs that, and one removed by the same name has nothing left to resolve
// it against, so there the key as given is the one the entry is under.
//
// The spelling that was given comes first, because a reply limit outlives
// the provider it was set for and nothing rewrites its key when that
// provider goes: "b/sol" can still be where a limit is held while a
// provider of some other id has since been given the display name "b".
// Resolving that name first would take the second provider's key away
// instead — a limit of its own is stored under it, so the user is told the
// entry they asked for has gone, and the entry they meant stays in the
// settings to go on answering with. Only where nothing is stored under the
// spelling is the provider looked for, by the id it has or the one it was
// renamed from: that is where SetModelOutput keeps a limit set through a
// display name, and the same resolution outputOf reads it back under.
func outputKeyOf(id string) (string, error) {
	r := strings.TrimPrefix(strings.TrimSpace(id), "magpie/")
	if strings.HasPrefix(r, GroupPrefix) {
		return "", errorf("that is a routing group, not a provider's model")
	}
	pid, model, ok := strings.Cut(r, "/")
	if !ok || pid == "" || model == "" {
		return "", errorf("name a model as provider/model, not %q", id)
	}
	key := pid + "/" + model
	if _, under := settings.Load().ModelOutputs[key]; under {
		return key, nil
	}
	if p, ok := byIDOrWas(pid); ok {
		return p.ID + "/" + model, nil
	}
	// and last a display name, which is what a provider of the user's is
	// often asked by. Nothing writes a reply limit under one —
	// SetModelOutput re-keys it as it does a name — but the spelling above
	// is the only thing that could have pointed at another provider's key,
	// and a key nothing is stored under reaches no limit at all without
	// this.
	if p, err := Find(pid); err == nil {
		return p.ID + "/" + model, nil
	}
	return key, nil
}

// SetContext is how long a request one of a provider's models takes, in
// tokens, over what the vendor's list and models.dev say; 0 takes the user's
// away, which DropContext does, and "*" is every model of that provider. As
// with a reply limit, setting one has to name a model the provider serves, and
// a removal may name one it no longer does.
//
// This is the provider's own Contexts, kept as the one value both the gateway
// advertises and the routing rules read — a context is a routing input, not
// only a number agents are shown: at 95% of a held member's window a request
// moves to a member that takes more (internal/gateway/rules.go). Saving it
// tells the agents (catalog.Touched), as for any change of the catalog: a
// window is a number they keep in files of their own — Pi's contextWindow,
// OpenCode's limit — and read at start-up, so a session already running
// keeps the window it began with while the gateway's own /models is right at
// once.
func SetContext(p Provider, model string, n int) error {
	if n < 0 {
		return errorf("a window is a number of tokens, not %d", n)
	}

	// a window for a model the provider does not serve is one that never
	// applies and nothing later says so. A removal is exempt, so an entry
	// left for a model that has since gone can still be taken away.
	if n > 0 && model != "*" && !p.serves(model) {
		return fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
	}
	if n > 0 {
		if p.Contexts == nil {
			p.Contexts = map[string]int{}
		}
		p.Contexts[model] = n
		return Save(p)
	}
	// a removal goes the way DropModelOutput's does: the provider is not
	// saved over a window that is not there, so nothing is written and the
	// agents are not told over a limit that did not change
	_, err := DropContext(p, model)
	return err
}

// DropContext takes a window off one of a provider's models and reports
// whether there was one there to take away, as DropModelPrice and
// DropModelOutput do: a removal over a window that is not there leaves the
// provider in the state it was already in, but only the caller can tell the
// user so, and a ✓ over a window that was never set says there was one.
// Nothing is saved in that case, and so the agents are not told either — a
// provider saved over a window that did not change rewrites every agent's
// model lists for nothing, which is the one round the removal itself earns.
//
// Save is what tells the agents here (store), so it is the save, and not a
// Touched of its own, that has to happen exactly once.
func DropContext(p Provider, model string) (bool, error) {
	if _, ok := p.Contexts[model]; !ok {
		return false, nil
	}
	delete(p.Contexts, model)
	if err := Save(p); err != nil {
		return false, err
	}
	return true, nil
}
