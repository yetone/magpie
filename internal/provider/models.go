package provider

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math"
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
	if p.DecideOnly() {
		return p.decideModels()
	}
	ms := p.available()
	if p.Decides() {
		// a gateway's Jev among its chat models, with its window and input
		ms = slices.Clone(ms)
		for i, m := range ms {
			if p.DecidesModel(m.ID) {
				ms[i] = withDecideFacts([]catalog.Model{m})[0]
			}
		}
	}
	return ms
}

func (p Provider) available() []catalog.Model {
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
		if p.IsAzure() {
			// a deployment is named as the user named it
			return catalog.Decorate(live, known)
		}
		// a model the provider's catalog doesn't list reads as it does
		// under the other providers serving it (GLM-5-Turbo, not
		// glm-5-turbo, beside ZCode's)
		return catalog.Named(catalog.Decorate(live, known))
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
// onto it left its own last list under the same id. A preset with no list
// ignores old fetched models too, unless a region or explicit URL lists them.
func (p Provider) live() ([]catalog.Model, time.Time, bool) {
	if p.IsPlugin() {
		return nil, time.Time{}, false
	}
	if pr := Preset(p.Preset); p.Account == nil && pr != nil && pr.NoList && strings.TrimSpace(p.ModelsURL) == "" && !p.listRegion(pr) {
		return nil, time.Time{}, false
	}
	type live struct {
		ms []catalog.Model
		at time.Time
		ok bool
	}
	l := heldOf("fetched:"+p.ID, func() live { ms, at, ok := catalog.Live(p.ID); return live{ms, at, ok} })
	return l.ms, l.at, l.ok
}

// Fetch asks the vendor which models it serves and remembers the answer.
func (p Provider) Fetch(ctx context.Context) ([]catalog.Model, error) {
	ms, _, err := p.Refetch(ctx)
	return ms, err
}

// Refetch is Fetch, and says which of the user's picks it dropped: those
// the list fetched before had and the one fetched now doesn't. The new
// list replaces the old one, and a pick made from the old list goes with
// it — kept, it stayed among the models agents see, as if typed in by
// hand, though the vendor serves it no more (akic404 on Discord: a remote
// magpie offering far fewer models, and one of its providers renamed,
// still listed every old "ws-ba5my…/qwen3.6-max" here after a Refresh).
// A pick the old list didn't have was typed in by hand, and stays; a
// fetch that fails, or that answers no model at all, changes nothing.
func (p Provider) Refetch(ctx context.Context) ([]catalog.Model, []string, error) {
	if p.Account != nil || p.DecideOnly() {
		ms, err := p.fetch(ctx)
		return ms, nil, err
	}
	before := liveIDs(p.ID)
	ms, err := p.fetch(ctx)
	if err == nil && p.listsDecisions() {
		// OpenRouter's decision models, listed apart from its chat ones
		if _, derr := p.fetchDecide(p.Via(ctx)); derr != nil {
			log.Println(p.ID + ": " + derr.Error())
		}
	}
	if err != nil || len(before) == 0 {
		return ms, nil, err
	}
	now := liveIDs(p.ID)
	if len(now) == 0 {
		return ms, nil, nil
	}
	dropped, derr := dropGonePicks(p.ID, before, now)
	if derr != nil {
		log.Println(p.ID + ": " + derr.Error())
	}
	return ms, dropped, nil
}

// liveIDs are the ids of every model in the provider's fetched list.
func liveIDs(id string) map[string]bool {
	ms, _, _ := catalog.Live(id)
	ms = append(ms, catalog.LiveDrawers(id)...)
	ms = append(ms, catalog.LiveVideomakers(id)...)
	out := make(map[string]bool, len(ms))
	for _, m := range ms {
		out[m.ID] = true
	}
	return out
}

// dropGonePicks takes out of the saved provider's picks those before had
// and now doesn't, and answers them.
func dropGonePicks(id string, before, now map[string]bool) ([]string, error) {
	f, err := read()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(f.Providers, func(q Provider) bool { return q.ID == id })
	if i < 0 {
		return nil, nil
	}
	var kept, dropped []string
	for _, m := range f.Providers[i].Models {
		if before[m] && !now[m] {
			dropped = append(dropped, m)
		} else {
			kept = append(kept, m)
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	f.Providers[i].Models = kept
	return dropped, store(f)
}

// fetch is Fetch without the picks looked after.
func (p Provider) fetch(ctx context.Context) ([]catalog.Model, error) {
	ctx = p.Via(ctx)
	if p.DecideOnly() {
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
	// Cline's plan and free models, from the list its own clients take
	// theirs from; the API's list has neither, and is asked if that fails
	if p.IsCline() && strings.TrimSpace(p.ModelsURL) == "" {
		if ms, base, err := p.clineFeed(ctx); err == nil {
			return catalog.Chat(ms), catalog.SaveLive(p.ID, base, ms)
		}
	}
	// the Kilo Gateway's list as Kilo's clients ask it, which marks its
	// free models; with no key, those alone
	if p.IsKilo() && strings.TrimSpace(p.ModelsURL) == "" {
		ms, base, err := p.kiloModels(ctx)
		if err != nil {
			return nil, err
		}
		return catalog.Chat(ms), catalog.SaveLive(p.ID, base, ms)
	}
	// Only keys in use. An off key is not asked, and its list does not
	// join the catalog or take capabilities off a key that is on.
	if keys := p.KeysOn(); len(keys) > 1 {
		return p.fetchPerKey(ctx, keys)
	}
	l, err := p.fetchOne(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.Chat(l.models), catalog.SaveLiveSides(p.ID, l.base, l.models, l.sides)
}

// List asks the vendor which models it serves, as Fetch does, and keeps
// nothing: a provider still being added (#578, the add form's Fetch models)
// is shown its vendor's list to pick from before it is saved.
func (p Provider) List(ctx context.Context) ([]catalog.Model, error) {
	ctx = p.Via(ctx)
	if pr := Preset(p.Preset); pr != nil && pr.NoList && strings.TrimSpace(p.ModelsURL) == "" && !p.listRegion(pr) {
		return catalog.Chat(p.planModels(nil)), nil
	}
	if p.IsCline() && strings.TrimSpace(p.ModelsURL) == "" {
		if ms, _, err := p.clineFeed(ctx); err == nil {
			return catalog.Chat(ms), nil
		}
	}
	if p.IsKilo() && strings.TrimSpace(p.ModelsURL) == "" {
		ms, _, err := p.kiloModels(ctx)
		if err != nil {
			return nil, err
		}
		return catalog.Chat(ms), nil
	}
	l, err := p.fetchOne(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.Chat(l.models), nil
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

// FetchNewBehind is FetchNew in the background, started at once unless one
// started so is still running: for the Providers page, which waited on it
// (#541: ten seconds and more of placeholders, an account at a time and
// behind start-up's own run). FetchingNew says when it is done.
func FetchNewBehind(timeout time.Duration) {
	if !fetchingNew.CompareAndSwap(false, true) {
		return
	}
	newSoonAt.Store(time.Now().UnixNano())
	go func() {
		defer fetchingNew.Store(false)
		FetchNew(timeout)
	}()
}

// newRunning counts the FetchNew calls under way.
var newRunning atomic.Int32

// FetchingNew reports whether a FetchNew is under way (start-up's, or one
// started behind a page): accounts' lists may be on their way still.
func FetchingNew() bool { return newRunning.Load() > 0 }

// FetchNew asks each signed-in account whose vendor list magpie hasn't
// fetched yet for it, each for at most timeout. Start-up does this for the
// accounts there then; an account signed in while magpie runs (in magpie or
// in the agent's own app) otherwise showed magpie's built-in list, fewer
// models than the vendor serves, until Refresh was clicked (#204). One that
// fails is asked again after newFetchRetry, not each time.
func FetchNew(timeout time.Duration) {
	newRunning.Add(1)
	defer newRunning.Add(-1)
	newFetches.Lock()
	defer newFetches.Unlock()
	for _, p := range All() {
		// a list of decision models fetched before magpie kept their
		// windows and input, or not fetched yet (ARNO on Discord)
		decide := p.decideListDue()
		if !decide && (p.Account == nil || !p.Ready()) {
			continue
		}
		// a plugin's accounts were listed with the plugin's providers
		if _, ok := p.Listed(); ok && !decide {
			continue
		}
		if t, ok := newFetches.m[p.ID]; ok && time.Since(t) < newFetchRetry {
			continue
		}
		newFetches.m[p.ID] = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		var err error
		if decide && p.Account == nil {
			_, err = p.fetchDecide(p.Via(ctx))
		} else {
			_, err = p.Fetch(ctx)
		}
		if err != nil {
			log.Println(p.ID + ": " + err.Error())
		}
		cancel()
	}
}

// a fetched list: every endpoint's models merged, the base kept for
// routing, what each endpoint listed of its own (sides), and which of them
// answered at all, so that one which can't be asked keeps its own models
// next time (#904).
type fetched struct {
	models   []catalog.Model
	base     string
	sides    map[string][]catalog.Model
	answered map[string]bool
}

// fetchOne asks every endpoint the provider speaks with p's key, and merges
// the lists (#904): a vendor serves its models on several protocols, and
// each protocol's base lists its own (Kimi's Claude models at its Anthropic
// base, its GPT models at its Chat base). Keeping only the first list that
// answered left every other endpoint's models out of the catalog, and so out
// of the agents' model lists and of a model's own protocol setting.
func (p Provider) fetchOne(ctx context.Context) (fetched, error) {
	if u := strings.TrimSpace(p.ModelsURL); u != "" {
		// asked where the user said, and nowhere else: the base URLs
		// list nothing, or the wrong thing
		ms, err := catalog.FetchURL(ctx, u, p.Key, p.Chat == "" && p.Responses == "", p.listHeaders())
		if err != nil {
			return fetched{}, err
		}
		return fetched{catalog.WithDrawers(p.planModels(ms), catalog.PublicDrawers(ctx, u)), u, nil, nil}, nil
	}
	if p.IsAzure() && (p.Chat != "" || p.Responses != "") {
		// its deployments, asked with the key in api-key
		ms, base, err := p.azureModels(ctx)
		if err != nil {
			return fetched{}, err
		}
		return fetched{ms, base, nil, nil}, nil
	}
	var errs []string
	var out []catalog.Model
	byID := map[string]int{}
	add := func(m catalog.Model) {
		i, ok := byID[m.ID]
		if !ok {
			byID[m.ID], i = len(out), len(out)
			out = append(out, m)
			return
		}
		// a model of both protocols' lists is one model: its image
		// capability is merged as the keys' lists are merged
		out[i].ImageInput = sharedImageInput(out[i].ImageInput, m.ImageInput)
		out[i].Images = out[i].Images && m.Images
	}
	var base string
	var sides []side  // the bases that answered, in the order asked
	var dead []string // the bases that could not be asked this time
	asked := map[string]bool{}
	answered := map[string]bool{} // of the bases that answered, not those kept from
	for _, proto := range p.Speaks() {
		protoBase := p.Base(proto)
		// OpenRouter's Anthropic base is not asked for the list, when the
		// provider asks at another base: with the Anthropic version header
		// OpenRouter's /models answers with its own catalog again, twenty
		// newest first, every id namespaced under `anthropic/` — ids
		// OpenRouter does not serve, so merged they are entries of the
		// model picker that name no model, and a cluttered list to choose
		// from (2026-10-06, #904). What the rule reads is the base being
		// asked, not the provider's other bases: a Chat base of
		// OpenRouter's says nothing of an Anthropic base that belongs to
		// another vendor, whose models are merged as they are. One of
		// OpenRouter's asked because it is the only base there is is asked
		// after all, and refused with what it answers
		// (openRouterNamespacedErr).
		if proto == Anthropic && p.skipOpenRouterAnthropic(protoBase) {
			continue
		}
		// Chat and Responses at one base say the same thing
		if asked[protoBase] {
			continue
		}
		asked[protoBase] = true
		ms, u, err := catalog.FetchAt(ctx, protoBase, p.Key, proto == Anthropic, p.listHeaders())
		if err == nil && proto == Anthropic && HostOf(protoBase) == openRouterHost {
			// the pages of the list are followed to its end, and what
			// comes back is still not a list of models to serve
			err = openRouterNamespacedErr(protoBase)
		}
		if err != nil {
			if !slices.Contains(errs, err.Error()) {
				errs = append(errs, err.Error())
			}
			dead = append(dead, protoBase)
			continue
		}
		sideBase := protoBase
		if base == "" {
			// the first protocol that answered keeps the base: the drawer
			// list is fetched beside it, and it is saved with the models
			base = protoBase
			if proto != Anthropic {
				base = p.fixV1(base, u)
			}
			// the base as it will be asked next time, /v1 and all, so
			// this side is found again by the fetch after this one
			sideBase = base
		}
		sides = append(sides, side{sideBase, ms})
		answered[sideBase] = true
		for _, m := range ms {
			add(m)
		}
	}
	if base == "" {
		if len(errs) == 0 {
			return fetched{}, errorf("%s has no endpoint to ask", p.Name)
		}
		// the endpoints are kept as they were: a vendor with no list (or one
		// that wants what the key can't give) still serves the models typed in
		return fetched{}, errorf("%s — type its model ids in by hand, or give the URL its list is at", strings.Join(errs, "; "))
	}
	if len(dead) > 0 {
		kept, saved := p.keepDeadSides(dead, sides)
		for _, m := range kept {
			add(m)
		}
		sides = append(sides, saved...)
	}
	// the image models its list leaves out (AIHubMix's gpt-image-2)
	// the parts are kept only where several bases were asked: a single one
	// that fails is the fetch that fails, and its list is kept as it is
	var parts map[string][]catalog.Model
	if len(asked) > 1 {
		parts = sidesOf(sides)
	}
	return fetched{
		catalog.WithDrawers(p.planModels(out), catalog.PublicDrawers(ctx, base)),
		base,
		parts,
		answered,
	}, nil
}

// side is what one of a provider's base URLs listed.
type side struct {
	base   string
	models []catalog.Model
}

// openRouterHost is OpenRouter's, whose Anthropic base answers the
// Anthropic version header with a namespaced re-listing of the catalog.
const openRouterHost = "openrouter.ai"

// openRouterNamespacedErr says why the list OpenRouter's Anthropic base
// answers with is not one to serve. The header asks for Anthropic's own
// catalog, and OpenRouter answers with its catalog namespaced under
// `anthropic/` instead. Asked on 2026-10-06 that was a first page of the
// twenty newest and, under the cursor that page ended at, a second page
// with nothing after it; the two shared no id, and of all of them four
// were in the catalog the same host serves without the header. Those
// counts move with the catalog every day and are of no use to anyone: what
// does not move is the shape. Following the pages to the end of the list
// (catalog.Paged) lands deeper in the namespace rather than back at the
// catalog it was namespaced from, so a list read whole is no better a
// catalog than a page of it — the whole namespaced list, where the rule
// used to keep the twenty — and a provider whose only base is that one is
// left with the models it listed last time, as it is for any base it could
// not ask (#904).
//
// The message says what to do instead; the caller's own suffix adds the
// other way round, which is the same advice: a Chat base of OpenRouter's
// lists that very catalog under the ids OpenRouter serves.
func openRouterNamespacedErr(base string) error {
	return fmt.Errorf("%s: with the Anthropic version header this base answers with OpenRouter's own "+
		"catalog namespaced under `anthropic/`, which is not a list of models to serve; give this "+
		"provider OpenRouter's Chat base (https://openrouter.ai/api/v1), which lists those models "+
		"under the ids OpenRouter serves", base)
}

// skipOpenRouterAnthropic reports whether the Anthropic base at base is
// one of OpenRouter's, whose reply is its own catalog namespaced and of no
// use to the union, and the provider asks at a base of its own besides it
// (fetchOne). The host of the base being asked is the whole of the input:
// another vendor's Anthropic base is merged as it always was, and one of
// OpenRouter's under a provider that asks nowhere else is asked after all —
// there is then no other list to take, so it is asked rather than left
// unanswered, and refused with what it answers (openRouterNamespacedErr).
func (p Provider) skipOpenRouterAnthropic(base string) bool {
	if HostOf(base) != openRouterHost {
		return false
	}
	return strings.TrimSpace(p.Chat) != "" || strings.TrimSpace(p.Responses) != ""
}

// sidesOf is what each base listed, for the fetch to be saved with. A base
// that answered with nothing keeps its empty part: it is a base that listed
// nothing, which is what tells the next fetch not to look for its models
// among the ones another base dropped.
func sidesOf(sides []side) map[string][]catalog.Model {
	if len(sides) == 0 {
		return nil
	}
	out := make(map[string][]catalog.Model, len(sides))
	for _, s := range sides {
		out[s.base] = s.models
		if s.models == nil {
			out[s.base] = []catalog.Model{}
		}
	}
	return out
}

// keepDeadSides is what a base that could not be asked this time keeps: the
// models it listed last time, as fetchPerKey's keys keep theirs. A read that
// failed says nothing about what the vendor serves today, and a model of a
// failed endpoint read as gone is dropped from the user's picks for good,
// with no way back (#904).
//
// What is kept is that base's own models alone: a model the bases that
// answered have dropped is dropped here too, however many of them dropped
// it, and however many endpoints are down. Keeping every model of the list
// saved before would bring such a model back, which is the same fault the
// other way round (#904, yetone's review).
//
// A base in none of the saved parts is one that has never answered, and
// keeps nothing: the whole list saved before is not its own. Reading an
// absent part as the whole list kept a model its vendor had already
// dropped, and then saved that guess as what the base listed, so from then
// on the model was a base's own and could never be dropped again (#904,
// yetone's review of #1006).
//
// A list saved without its parts (before this kept them) says nothing about
// which base listed what, so the models no answering base lists are kept
// this once, and the next fetch with every base answering says which they
// are.
//
// kept are the models this fetch's list is made of, saved the parts to
// write beside it: not every base kept from is saved, only one whose part
// was read out of the file rather than stood in for.
func (p Provider) keepDeadSides(dead []string, answered []side) (kept []catalog.Model, saved []side) {
	old, last, ok := catalog.LiveSplit(p.ID)
	if !ok || len(last) == 0 {
		return nil, nil
	}
	listed := map[string]bool{}
	for _, s := range answered {
		for _, m := range s.models {
			listed[m.ID] = true
		}
	}
	// a model another of the provider's keys alone sees is not this key's
	// to keep (fetchPerKey's Keys)
	mine := func(m catalog.Model) bool {
		return p.Key == "" || len(m.Keys) == 0 || slices.Contains(m.Keys, keyID(p.Key))
	}
	var out []side
	for _, base := range dead {
		own, had := old[base]
		if !had {
			if old != nil {
				// the parts are saved and this base is in none of them: it
				// has never answered, so it has no models of its own
				continue
			}
			// no parts at all: what no answering base lists is kept, this
			// once, and not saved as this base's own
			own = last
		}
		var part []catalog.Model
		for _, m := range own {
			if (had || !listed[m.ID]) && mine(m) {
				part = append(part, m)
			}
		}
		kept = append(kept, part...)
		if had {
			out = append(out, side{base, part})
		}
	}
	return kept, out
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

// regionOf is the region of pr the provider sits at, or nil: the one most
// of its endpoints are as written first, else the one most share their
// paths with (a mirror, a test). TokenHub's China and global hosts serve
// the same paths, and a GLM Coding Plan its pay as you go's messages
// endpoint: the host and the other endpoints tell them apart.
func (p Provider) regionOf(pr *PresetDef) *Region {
	var best *Region
	top := 0
	for i, r := range pr.Regions {
		n := 0
		for _, e := range [][2]string{{p.Chat, r.Chat}, {p.Responses, r.Responses}, {p.Anthropic, r.Anthropic}} {
			switch {
			case e[0] == "" || e[1] == "":
			case strings.EqualFold(e[0], e[1]):
				n += 4
			case basePath(e[0]) == basePath(e[1]):
				n++
			}
		}
		if n > top {
			best, top = &pr.Regions[i], n
		}
	}
	return best
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
// as it came. A region with models of its own (Tencent Cloud's Token Plan,
// beside TokenHub's pay as you go) is a plan with those.
func (p Provider) planModels(ms []catalog.Model) []catalog.Model {
	pr := Preset(p.Preset)
	if pr == nil {
		return ms
	}
	models := pr.Models
	if r := p.regionOf(pr); r != nil && len(r.Models) > 0 {
		models = r.Models
	}
	if pr.Only == "" && (len(models) == 0 || len(ms) > 0) {
		return ms
	}
	var out, free []catalog.Model
	for _, m := range ms {
		switch {
		case strings.HasPrefix(m.ID, pr.Only):
			out = append(out, m)
		case p.IsCline() && (m.Free || isClineFree(m.ID)):
			// Cline's free models, served apart from the plan's quota
			m.Free = true
			free = append(free, m)
		}
	}
	if len(out) == 0 {
		for _, id := range models {
			out = append(out, catalog.Model{ID: id, Name: id})
		}
	}
	if len(free) == 0 && p.IsCline() {
		// as Cline's desktop app last listed them
		for _, m := range clineFree {
			m.Free = true
			free = append(free, m)
		}
	}
	out = append(out, free...)
	for i, m := range out {
		// the vendor's window for its model, as models.dev has it
		id := strings.TrimPrefix(m.ID, pr.Only)
		if m.Free {
			id = m.ID[strings.LastIndex(m.ID, "/")+1:]
		}
		if m.Context == 0 {
			out[i].Context = catalog.ContextOf(id)
		}
		if m.Output == 0 {
			out[i].Output = catalog.OutputOf(id)
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
// key that can't be asked now keeps the models it saw last time, and so does
// a base of its own that can't be asked (#904).
func (p Provider) fetchPerKey(ctx context.Context, keys []KeyAccount) ([]catalog.Model, error) {
	old, _, _ := catalog.Live(p.ID)
	old = append(old, catalog.LiveDrawers(p.ID)...)
	old = append(old, catalog.LiveVideomakers(p.ID)...)
	oldSides, _, _ := catalog.LiveSplit(p.ID)
	var out []catalog.Model
	var from [][]string // the bases that listed each model of out
	asked := map[string]bool{}
	// the bases each key was answered at, beside the models a key that
	// could not be asked at all keeps and the keys that saw them last time
	answeredBy := map[string]map[string]bool{}
	type deadModel struct {
		at   int
		keys []string
	}
	var deadKept []deadModel
	at := map[string]int{}
	add := func(m catalog.Model, id string) int {
		i, ok := at[m.ID]
		if !ok {
			m.Keys = nil
			at[m.ID], i = len(out), len(out)
			out = append(out, m)
			from = append(from, nil)
		} else {
			out[i].ImageInput = sharedImageInput(out[i].ImageInput, m.ImageInput)
			out[i].Images = out[i].Images && m.Images
		}
		if !slices.Contains(out[i].Keys, id) {
			out[i].Keys = append(out[i].Keys, id)
		}
		return i
	}
	var base string
	var lastErr error
	for _, k := range keys {
		id := keyID(k.Key)
		q := p.WithKey(k)
		l, err := q.fetchOne(ctx)
		if err != nil {
			lastErr = err
			for _, m := range old {
				if slices.Contains(m.Keys, id) {
					deadKept = append(deadKept, deadModel{add(m, id), m.Keys})
				}
			}
			continue
		}
		if base == "" {
			base = l.base
		}
		for _, m := range l.models {
			add(m, id)
		}
		// the bases this key's fetch asked, listed or not
		for b := range l.sides {
			asked[b] = true
		}
		if l.answered != nil {
			answeredBy[id] = l.answered
		}
		// what each base listed of its own, marked on the merged models
		// as its part, and kept out of them: a model this plan's own
		// models (PresetDef.Only) leave out is left out here too
		for b, ms := range l.sides {
			for _, m := range ms {
				if i, ok := at[m.ID]; ok && !slices.Contains(from[i], b) {
					from[i] = append(from[i], b)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	// the parts saved beside the list say which bases listed a model kept
	// for a key that could not be asked at all, so it is as its own base's
	// as one kept for that base alone (#904). A base that was asked this
	// round for one of the model's own keys, and answered it without
	// listing the model, has said what it serves today and no longer owns
	// it: sides may only say what the bases listed (yetone's review of
	// #1006).
	spokeFor := func(keys []string, b string) bool {
		for _, j := range keys {
			if answeredBy[j][b] {
				return true
			}
		}
		return false
	}
	for _, d := range deadKept {
		for _, b := range basesOf(oldSides, out[d.at].ID) {
			if spokeFor(d.keys, b) || slices.Contains(from[d.at], b) {
				continue
			}
			from[d.at] = append(from[d.at], b)
		}
	}
	// the parts, where the provider was asked at more than one of its
	// bases: a base that can't be asked keeps its own models from the list
	// saved before (#904). A base that listed nothing is a part of its own,
	// empty, which is what says it has no models rather than that its part
	// was lost.
	var sides map[string][]catalog.Model
	if len(asked) > 1 {
		sides = make(map[string][]catalog.Model, len(asked))
		for b := range asked {
			sides[b] = []catalog.Model{}
		}
		for i, m := range out {
			for _, b := range from[i] {
				sides[b] = append(sides[b], m)
			}
		}
	}
	return catalog.Chat(out), catalog.SaveLiveSides(p.ID, base, out, sides)
}

// basesOf are the saved parts that listed a model: where a model a failed
// key kept out of the list saved before came from, and so which base keeps
// it the next time that base is the one that can't be asked (#904).
func basesOf(sides map[string][]catalog.Model, id string) []string {
	var out []string
	for b, ms := range sides {
		for _, m := range ms {
			if m.ID == id {
				out = append(out, b)
				break
			}
		}
	}
	slices.Sort(out)
	return out
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

// anyImageInput is a group's answer from two of its members': it takes
// images when one of them does (the gateway sends a request with an image
// to the members that see, #756), is text-only when both are, and is
// unknown otherwise.
func anyImageInput(a, b *bool) *bool {
	if a != nil && *a {
		return a
	}
	if b != nil && *b {
		return b
	}
	if a != nil && b != nil {
		return a
	}
	return nil
}

// Serves reports whether key k can be asked for model: false only when the
// vendor's lists say another of the provider's keys sees it and k doesn't.
func (p Provider) Serves(k KeyAccount, model string) bool {
	live, _, ok := p.live()
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
				// with the levels the gateway fits an effort to (Known),
				// not the none effortsOf takes a vendor's word for: the
				// vendor's list doesn't have it, so it gave no word (#597)
				m := catalog.Model{ID: id, Name: id, Provider: p.firstCatalog(), Efforts: p.knownElsewhere(id)}
				if !p.IsAzure() {
					m = catalog.Named([]catalog.Model{m})[0]
				}
				out = append(out, m)
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
	} else if p.Account != nil && p.Account.unusable != nil {
		// with no list fetched, a pick the account can't be served (a
		// ZCode Start Plan account's GLM-5.3) is left out all the same
		picks = slices.DeleteFunc(slices.Clone(picks), p.Account.unusable)
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

// Thinks reports whether the model reasons: it has levels, its list says
// it thinks, or models.dev says most of those serving it do (Entry's
// Reasoning).
func (p Provider) Thinks(model string) bool {
	if len(p.Efforts(model)) > 0 {
		return true
	}
	for _, m := range p.Available() {
		if m.ID == model && m.Reasoning {
			return true
		}
	}
	return catalog.Thinks(model)
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
	return p.knownElsewhere(model)
}

// knownElsewhere are the reasoning levels of a model the provider's list
// doesn't have — one of the vendor's own its list leaves out (a preview),
// or typed in: the vendor's word on it, before the others'.
func (p Provider) knownElsewhere(model string) []string {
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
	if p.clineFreeModel(model) || p.kiloFreeModel(model) {
		// served at no cost: not at the price of the model it is free of
		return catalog.Price{}, true
	}
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
// maker's, either at the provider's price rate (PriceRate). The second return is false only when no price is known at all,
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
				// a Claude model's 1-hour cache write left out: 2× input
				catalog.OneHourFor(model, &pr)
				return pr, true
			}
		}
	}
	if !known {
		return MakerPrice(model)
	}
	pr, ok := p.ListPrice(model)
	if !ok {
		pr, ok = MakerPrice(model)
	}
	// what the provider bills against it (#819)
	if ok && p.PriceRate > 0 {
		pr = pr.Times(p.PriceRate)
	}
	return pr, ok
}

// PriceRateOK says what is wrong with a provider's price rate, "" when
// nothing: from 0 (none) to 1000, in steps of 0.001.
func PriceRateOK(r float64) string {
	if math.IsNaN(r) || r < 0 || r > 1000 {
		return "a price rate is from 0 to 1000"
	}
	if math.Abs(r*1000-math.Round(r*1000)) > 1e-6 {
		return "a price rate has at most three decimals, like 0.125"
	}
	return ""
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
	Named      bool     `json:"-"`                // a routing group the user made or changed: its name is theirs (Labels)
	Icons      []string `json:"-"`                // a group's: its providers' icons, one per provider
	Images     bool     `json:"images,omitempty"` // takes images as input (a group's: a member does)
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
	// Rate and RateWas are the credits a request costs its subscription,
	// as a multiple, and before a discount running now (catalog.Model's).
	Rate    float64 `json:"rate,omitempty"`
	RateWas float64 `json:"rateWas,omitempty"`
	// Shared are a group's levels its members have in common: its Efforts,
	// unless the group names its own (Group.Levels).
	Shared []string `json:"-"`
	// Reasoning is set on a model that thinks, levels or not: one with a
	// thinking switch alone has it and no Efforts (a group's: a member
	// thinks).
	Reasoning bool `json:"reasoning,omitempty"`
	// AgentsV2 is set on a model offering Codex's Ultra that no ChatGPT
	// account answers for (a group's: none of its members): Codex is told
	// multi-agent V2 for it, so Ultra hands work to its agents, whose
	// tasks a magpie-served lead writes as text.
	AgentsV2 bool `json:"-"`
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
	return heldEntries(buildEntries)
}

func buildEntries() []Entry {
	var out []Entry
	s := settings.Load()
	for _, p := range All() {
		if !p.On() || p.DecideOnly() { // a dedicated decision API only routes
			continue
		}
		for _, m := range p.Exposed() {
			if !p.DecidesModel(m.ID) {
				out = append(out, entryFor(p, m, s))
			}
		}
	}
	return out
}

// entryFor is one of a provider's models as the catalog carries it: the
// window and the reply limit a request on it is routed and metered against,
// with what the user set taken over the vendor's list and models.dev.
func entryFor(p Provider, m catalog.Model, s settings.Settings) Entry {
	ctx := p.WindowOf(m)
	output := p.replyLimit(m, s)
	images := m.Images || catalog.SeesImages(m.ID)
	if m.ImageInput != nil {
		images = *m.ImageInput
	}
	imageInput := m.ImageInput
	if override, ok := s.ModelImages[p.ID+"/"+m.ID]; ok {
		images, imageInput = override, &override
	}
	// a model its vendor lists with no name is called by its id: unnamed,
	// an agent's list showed the whole magpie/<provider>/<model> (#955)
	name := cmp.Or(m.Name, m.ID)
	e := Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Family: p.Family, Name: name, Efforts: effortsOf(m), Provider: p,
		Images: images, ImageInput: imageInput, Context: ctx, Output: output, Free: m.Free, Rate: m.Rate, RateWas: m.RateWas}
	if n, ok := modelNameIn(s.ModelNames, p.ID, m.ID); ok {
		e.Name, e.Default = n, name
	}
	// a model that thinks still does with the levels the user kept or
	// none at all; one its source says nothing of thinks as most of the
	// providers serving it say (#402)
	e.Reasoning = m.Reasoning || len(e.Efforts) > 0 || catalog.Thinks(m.ID)
	e.Efforts = effortsKept(e.Efforts, s.ModelEfforts[e.ID])
	// Codex's Ultra on a model OpenAI offers it on, served by another
	// vendor (Copilot's gpt-6.1-sol, #656): a ChatGPT account's list says it
	// already. Its subagents take their tasks as text from a magpie-served
	// lead, so Codex is told V2 for it, where Ultra hands work to them
	// (codexcat.Entries).
	if p.Account == nil || p.Account.Agent != "codex" {
		e.Efforts = withUltra(m.ID, e.Efforts)
		e.AgentsV2 = slices.Contains(e.Efforts, "ultra")
	}
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
		// the providers a request holds (All): an agent's value of a
		// model not listed lands here
		if p, err := findIn(All(), pid); err == nil && p.On() {
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
	for _, p := range heldOf("listed", listedBy)[id] {
		if !p.DecidesModel(id) {
			found = append(found, p)
		}
	}
	if len(found) == 1 {
		return found[0], id, true
	}
	return Provider{}, "", false
}

// listedBy is the providers on that list each model, in order, each once.
func listedBy() map[string][]Provider {
	out := map[string][]Provider{}
	for _, p := range All() {
		if !p.On() {
			continue
		}
		seen := map[string]bool{}
		for _, m := range p.Available() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out[m.ID] = append(out[m.ID], p)
			}
		}
	}
	return out
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

// ListedWindow is the window a provider's model takes before the user says
// one: its vendor's list's, else the one models.dev gives a model of its id.
// A vendor models.dev doesn't list (a custom provider, a proxy, a plugin
// whose list says none, as Cline's) serves models it knows from others, under
// the vendor's prefix too: cline-free/mimo-v2.6-flash is mimo-v2.6-flash.
func ListedWindow(m catalog.Model) int {
	if m.Context > 0 {
		return m.Context
	}
	return catalog.ContextOf(m.ID)
}

// WindowOf is the window one of the provider's models takes, as agents are
// told it and requests are routed on it: the one the user set (ContextOf)
// over ListedWindow. The catalog's entries and the Providers page's model
// lists both read it, so the page shows the window agents are told.
func (p Provider) WindowOf(m catalog.Model) int {
	if n := p.ContextOf(m.ID); n > 0 {
		return n
	}
	return ListedWindow(m)
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
// ReplyLimit is the most a reply of the provider's model may hold, in
// tokens, as agents are told it and the Gateway's model list shows it:
// the user's Max output, else the vendor's list's, else models.dev's.
func (p Provider) ReplyLimit(m catalog.Model) int {
	return p.replyLimit(m, settings.Load())
}

// ReplyLimitIn is ReplyLimit from settings s already read.
func (p Provider) ReplyLimitIn(m catalog.Model, s settings.Settings) int { return p.replyLimit(m, s) }

func (p Provider) replyLimit(m catalog.Model, s settings.Settings) int {
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
	return output
}

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

// OutputsOf is the reply limits the user set on a provider's models, by
// model id, "*" for all of them: what its editor's Max output shows.
func OutputsOf(providerID string) map[string]int {
	out := map[string]int{}
	for k, n := range settings.Load().ModelOutputs {
		if model, ok := strings.CutPrefix(k, providerID+"/"); ok && model != "" && n > 0 {
			out[model] = n
		}
	}
	return out
}

// SetModelOutputs makes a provider's reply limits outs, by model id, "*"
// for all of them, as its editor's Max output says (ARNO on Discord: a
// model's maxTokens was wrong, and only the context window could be set in
// the editor): those it leaves out are taken away, and every one set has
// to be a model the provider serves, as for SetModelOutput. Nothing is
// saved, nor the agents told, when nothing changed.
func SetModelOutputs(providerID string, outs map[string]int) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	for model, n := range outs {
		if n < 0 {
			return errorf("an output limit is a number of tokens, not %d", n)
		}
		if err := settings.CheckModelKey("an output limit", p.ID+"/"+model); err != nil {
			return err
		}
		if model != "*" && n > 0 && !p.serves(model) {
			return errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
	s := settings.Load()
	changed := false
	for k := range s.ModelOutputs {
		if model, ok := strings.CutPrefix(k, p.ID+"/"); ok && outs[model] <= 0 {
			delete(s.ModelOutputs, k)
			changed = true
		}
	}
	for model, n := range outs {
		if n <= 0 || s.ModelOutputs[p.ID+"/"+model] == n {
			continue
		}
		if s.ModelOutputs == nil {
			s.ModelOutputs = map[string]int{}
		}
		s.ModelOutputs[p.ID+"/"+model] = n
		changed = true
	}
	if !changed {
		return nil
	}
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
