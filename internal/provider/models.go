package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/probelog"
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
	// by the ids the user lists them by (SetModelID)
	ms := p.ids().list(p.available())
	if p.Decides() {
		// a gateway's Jev among its chat models, with its window and input
		ms = slices.Clone(ms)
		for i, m := range ms {
			if p.isDecision(m) {
				ms[i] = withDecideFacts([]catalog.Model{m})[0]
			}
		}
	}
	return ms
}

func (p Provider) available() []catalog.Model {
	if p.ListsOff() {
		// the ids added by hand alone (Exposed)
		return nil
	}
	signedIn := p.Account != nil && p.Account.models != nil
	var known []catalog.Model
	if signedIn {
		known = p.Account.models()
	} else {
		// read only below: every way out of here returns a new list
		// (Decorate's, or the one without previews) or none of known
		known = catalogsOf(p.Catalogs())
	}
	if live, _, ok := p.live(); ok && (p.Account == nil || !p.Account.magpieList) {
		if p.Account != nil && p.Account.unusable != nil {
			// a model the list offers that the account was refused
			// (Copilot's, copilot_refused.go)
			// unless another of its accounts on lists it (copilot_pool.go),
			// read only once a model was refused
			var others map[string]bool
			live = slices.DeleteFunc(slices.Clone(live), func(m catalog.Model) bool {
				if !p.Account.unusable(m.ID) {
					return false
				}
				if others == nil && p.Account.Agent == "copilot" {
					others = copilotOthersList(p.Account.User)
				}
				return !others[m.ID]
			})
		}
		switch p.ID {
		case "cursor":
			live = withoutCursorCapacity(collapseCursorModels(withCursorContexts(live)))
		case "devin":
			live = withDevinContexts(devinCollapse(live, devinCached(), p.Models))
		case WorkBuddyAIID:
			// a list saved before its GLM models were known to drop
			// images says they take them (wbSite.textOnly)
			live = wbAI.textOnly(live)
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
	n := 0
	for _, m := range known {
		if released(m) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	out := make([]catalog.Model, 0, n)
	for _, m := range known {
		if released(m) {
			out = append(out, m)
		}
	}
	return out
}

// released is a catalog's model that is not an experiment or a preview.
func released(m catalog.Model) bool {
	return !strings.Contains(m.ID, "-exp") && !strings.Contains(m.ID, "preview")
}

// catalogsOf are the models of these models.dev catalogs, the first's of
// an id kept. One catalog's is its shared list, with no copy: the caller
// only reads it.
func catalogsOf(ids []string) []catalog.Model {
	if len(ids) == 1 {
		return catalog.ProviderShared(ids[0])
	}
	var known []catalog.Model
	seen := map[string]bool{}
	for _, id := range ids {
		for _, m := range catalog.ProviderShared(id) {
			if !seen[m.ID] {
				seen[m.ID] = true
				known = append(known, m)
			}
		}
	}
	return known
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
		// one look at the plugins' list a request, not one a provider
		type listed struct {
			at time.Time
			ok bool
		}
		l := heldOf("plugin-listed", func() listed { at, ok := plugin.ListedAt(); return listed{at, ok} })
		return l.at, l.ok
	}
	return p.Fetched()
}

// live is the list last fetched from the vendor. A plugin's provider has
// none: its models are what the plugin lists now, and a built-in moved
// onto it left its own last list under the same id. A preset with no list
// ignores old fetched models too, unless a region or explicit URL lists them.
func (p Provider) live() ([]catalog.Model, time.Time, bool) {
	if !p.lists() {
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

// liveByID are the models of live with this id: what a look at one
// model, asked for each of a list's, reads instead of the whole list.
// They are the list read, not a copy: a caller only reads them.
func (p Provider) liveByID(model string) []catalog.Model {
	if !p.lists() {
		return nil
	}
	// a model listed by an id the user gave it is in the vendor's list
	// under its own (SetModelID)
	own := p.OwnModelID(model)
	ms := catalog.LiveByIDShared(p.ID, own)
	if own == model {
		return ms
	}
	out := slices.Clone(ms)
	for i := range out {
		out[i].ID = model
	}
	return out
}

// lists says whether the provider's fetched list is its own to read: not
// a plugin's, nor a preset's that lists nothing (Preset.NoList).
func (p Provider) lists() bool {
	if p.IsPlugin() || p.ListsOff() {
		return false
	}
	pr := Preset(p.Preset)
	// keys on plans of their own each have a list (fetchPerKey), the
	// plans' own and the pay as you go's
	return p.Account != nil || pr == nil || !pr.NoList || strings.TrimSpace(p.ModelsURL) != "" || p.listRegion(pr) || p.plansMixed()
}

// ListsOff is ListOff on a key's provider: a subscription's account and a
// plugin's provider list their models as their sign-in does.
func (p Provider) ListsOff() bool {
	return p.ListOff && p.Account == nil && !p.IsPlugin()
}

// ErrListOff is what a Fetch of a provider whose list is off answers,
// with no request made.
var ErrListOff = errors.New("its model list is off: add each model by its id")

// listsRead counts liveAll's reads (ListsRead).
var listsRead atomic.Int64

// ListsRead is how many times a provider's saved lists have been asked
// for whole in this process: a test counts them to say a request reads
// them once, not once a key (@NetRozee on X).
func ListsRead() int64 { return listsRead.Load() }

// liveAll is live with the models that draw or make videos too
// (catalog.LiveAll): which of its keys serves one is said there alone.
func (p Provider) liveAll() ([]catalog.Model, bool) {
	listsRead.Add(1)
	if !p.lists() {
		return nil, false
	}
	type live struct {
		ms []catalog.Model
		ok bool
	}
	l := heldOf("fetched-all:"+p.ID, func() live { ms, ok := catalog.LiveAll(p.ID); return live{ms, ok} })
	return l.ms, l.ok
}

// Listed is a provider RefreshLists asked, and how many models it listed.
type Listed struct {
	ID     string
	Models int
}

// refreshEach is how long RefreshLists waits for each provider's list.
var refreshEach = 8 * time.Second

// RefreshLists asks every provider that is ready, and not switched off,
// for its model list, each for at most refreshEach, and answers those that
// answered, in order: the Refresh of every list (the GUI's, `magpie
// sync`). One switched off is asked nothing and keeps the list it had
// (ChangQiu on Discord: Off means no request goes to it).
func RefreshLists(ctx context.Context) []Listed {
	var out []Listed
	for _, p := range All() {
		if !p.Ready() || p.Off || p.ListsOff() {
			continue
		}
		c, cancel := context.WithTimeout(ctx, refreshEach)
		ms, err := p.Fetch(c)
		cancel()
		if err == nil {
			out = append(out, Listed{p.ID, len(ms)})
		}
	}
	return out
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
	ms, dropped, _, err := p.RefetchTaking(ctx)
	return ms, dropped, err
}

// RefetchTaking is Refetch, and says which models the provider's
// AutoInclude rules took into its picks: those the list fetched now has,
// the one before didn't, and a rule matches (takeMatching, #2135).
func (p Provider) RefetchTaking(ctx context.Context) (ms []catalog.Model, dropped, taken []string, err error) {
	ms, dropped, before, err := p.refetch(ctx)
	if err != nil || !p.ownList() || len(p.AutoInclude) == 0 {
		return ms, dropped, nil, err
	}
	taken, terr := takeMatching(p.ID, func(id string) bool { return !before[id] })
	if terr != nil {
		log.Println(p.ID + ": " + terr.Error())
	}
	return ms, dropped, taken, nil
}

// refetch is Refetch, with the ids the provider's list had before it
// (none for an account's, a plugin's or a decision-only provider's).
func (p Provider) refetch(ctx context.Context) ([]catalog.Model, []string, map[string]bool, error) {
	ctx = p.ownRequests(ctx, probelog.KindList)
	if p.IsPlugin() {
		// its plugin's list, read again: a pick it had and has no more
		// goes as a built-in's does (plugin_relist.go)
		before := plugin.Cached()
		ms, err := p.fetch(ctx)
		if err != nil {
			return ms, nil, nil, err
		}
		return ms, dropGonePluginPicks(before, plugin.Cached())[p.ID], nil, nil
	}
	if p.Account != nil || p.DecideOnly() {
		ms, err := p.fetch(ctx)
		return ms, nil, nil, err
	}
	before := liveIDs(p.ID)
	marks := keyMarks(p.ID)
	ms, err := p.fetch(ctx)
	if err == nil {
		// each key's own models follow what its list gained and lost
		// (key_models_refresh.go)
		refreshKeyModels(p.ID, marks, keyMarks(p.ID))
	}
	if err == nil && p.listsDecisions() {
		// OpenRouter's decision models, listed apart from its chat ones
		if _, derr := p.fetchDecide(p.Via(ctx)); derr != nil {
			log.Println(p.ID + ": " + derr.Error())
		}
	}
	if err != nil {
		return ms, nil, before, err
	}
	if len(before) == 0 {
		// nothing to drop against, but a pick set aside comes back
		if now := liveIDs(p.ID); len(now) > 0 && len(p.Delisted) > 0 {
			if _, derr := dropGonePicks(p.ID, nil, now); derr != nil {
				log.Println(p.ID + ": " + derr.Error())
			}
		}
		return ms, nil, before, nil
	}
	now := liveIDs(p.ID)
	if len(now) == 0 {
		return ms, nil, before, nil
	}
	dropped, derr := dropGonePicks(p.ID, before, now)
	if derr != nil {
		log.Println(p.ID + ": " + derr.Error())
	}
	return ms, dropped, before, nil
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
	// picks are of the ids the user lists them by (SetModelID)
	return modelIDsOf(id).ids(out)
}

// dropGonePicks takes out of the saved provider's picks those before had
// and now doesn't, and answers them. They are set aside in its Delisted,
// not forgotten, and those of its Delisted now has go back among the picks
// (relistPicks): a list that shrank for a while gives back what it took.
func dropGonePicks(id string, before, now map[string]bool) ([]string, error) {
	f, err := read()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(f.Providers, func(q Provider) bool { return q.ID == id })
	if i < 0 {
		return nil, nil
	}
	q := &f.Providers[i]
	var kept, dropped []string
	for _, m := range q.Models {
		if before[m] && !now[m] {
			dropped = append(dropped, m)
		} else {
			kept = append(kept, m)
		}
	}
	back := relistPicks(q, now)
	if len(dropped) == 0 && !back {
		return nil, nil
	}
	if len(dropped) > 0 {
		q.Models = kept
		for _, m := range dropped {
			if !slices.Contains(q.Delisted, m) {
				q.Delisted = append(q.Delisted, m)
			}
		}
	}
	switch {
	case len(q.Models) > 0 || len(q.Delisted) == 0:
		q.DelistedAt = time.Time{}
	case len(dropped) > 0 && q.DelistedAt.IsZero():
		// the last pick left now: the wait for one starts
		q.DelistedAt = time.Now()
	}
	return dropped, store(f)
}

// relistPicks puts back among q's picks those of its Delisted the list now
// has, and says whether it put any back.
func relistPicks(q *Provider, now map[string]bool) bool {
	if len(q.Delisted) == 0 || len(now) == 0 {
		return false
	}
	var still, back []string
	for _, m := range q.Delisted {
		if now[m] {
			back = append(back, m)
		} else {
			still = append(still, m)
		}
	}
	if len(back) == 0 {
		return false
	}
	for _, m := range back {
		if !slices.Contains(q.Models, m) {
			q.Models = append(q.Models, m)
		}
	}
	q.Delisted = still
	return true
}

// fetch is Fetch without the picks looked after. A Remote magpie's list
// also tells whether it is this magpie (noteSelf), and that magpie is
// asked whether it serves its MCP router (noteRemoteHub).
func (p Provider) fetch(ctx context.Context) ([]catalog.Model, error) {
	ms, err := p.fetchList(ctx)
	if err != nil && p.IsRemoteMagpie() {
		// one that took another address on its network is asked there
		// (remote_moved.go)
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seekFor)
		defer cancel()
		if q, ok := SeekMoved(sctx, p, err); ok {
			p, ctx = q, sctx
			ms, err = p.fetchList(ctx)
		}
	}
	if err == nil {
		noteSelf(p, ms)
		if len(ms) > 0 {
			noteRemoteHub(ctx, p)
		}
	}
	return ms, err
}

func (p Provider) fetchList(ctx context.Context) ([]catalog.Model, error) {
	if p.ListsOff() {
		return nil, ErrListOff
	}
	ctx = p.ownRequests(p.Via(ctx), probelog.KindList)
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
	if pr := Preset(p.Preset); pr != nil && pr.NoList && strings.TrimSpace(p.ModelsURL) == "" && !p.listRegion(pr) && !p.plansMixed() {
		return catalog.Chat(p.planModels(nil)), nil
	}
	// keys on plans of their own (KeyAccount.Plan): each key's plan's
	// list, at its own endpoints, merged and marked with the keys that
	// see each model, as keys of one plan whose lists differ are
	if p.plansMixed() {
		return p.fetchPerKey(ctx, p.KeysOn())
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
	if err != nil && p.IsOpenCode() && p.Key != "" {
		// OpenCode's lists are public: the vendor answers its /models
		// without a key, so a list the key is refused is asked again
		// bare, and the picker says what OpenCode serves, not what
		// models.dev remembers of it (#4900)
		bare := p
		bare.Key = ""
		if b, berr := bare.fetchOne(ctx); berr == nil {
			l, err = b, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return catalog.Chat(l.models), catalog.SaveLiveSides(p.ID, l.base, l.models, l.sides)
}

// List asks the vendor which models it serves, as Fetch does, and keeps
// nothing: a provider still being added (#578, the add form's Fetch models)
// is shown its vendor's list to pick from before it is saved.
func (p Provider) List(ctx context.Context) ([]catalog.Model, error) {
	ms, err := p.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.Chat(ms), nil
}

// ListAll is List with the models served on an API of their own kept:
// embedding and rerank models, tagged Serves.
func (p Provider) ListAll(ctx context.Context) ([]catalog.Model, error) {
	if p.ListsOff() {
		return nil, ErrListOff
	}
	ctx = p.ownRequests(p.Via(ctx), probelog.KindList)
	if pr := Preset(p.Preset); pr != nil && pr.NoList && strings.TrimSpace(p.ModelsURL) == "" && !p.listRegion(pr) {
		// with its embedding and rerank models, marked so (Voyage AI's)
		return append(p.planModels(nil), p.PlanRetrieval("")...), nil
	}
	if p.IsCline() && strings.TrimSpace(p.ModelsURL) == "" {
		if ms, _, err := p.clineFeed(ctx); err == nil {
			return ms, nil
		}
	}
	if p.IsKilo() && strings.TrimSpace(p.ModelsURL) == "" {
		ms, _, err := p.kiloModels(ctx)
		if err != nil {
			return nil, err
		}
		return ms, nil
	}
	l, err := p.fetchOne(ctx)
	if err != nil {
		return nil, err
	}
	return l.models, nil
}

// ListAsks says List asks a vendor over the network, with p's own key,
// what p serves: a key's provider, not a subscription's account, a
// plugin's or a decision API, nor a preset with no list to ask, whose List
// answers the preset's models without asking anyone. The gateway asks it
// of a key resting after an outage, to tell whether it is back
// (gateway/rest_probe.go).
func (p Provider) ListAsks() bool {
	if p.Account != nil || p.DecideOnly() || p.ListsOff() {
		return false
	}
	pr := Preset(p.Preset)
	return pr == nil || !pr.NoList || strings.TrimSpace(p.ModelsURL) != "" || p.listRegion(pr)
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

// fetchingNew is set while a FetchNew started in the background
// (FetchNewSoon, FetchNewBehind) picks the accounts due or runs; newSoonAt
// is when the last one started.
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
	// the panel reads nothing first, and /api/state?last=1 answers at once
	// (#1519): even the accounts are listed behind it, told by fetchingNew
	// until FetchNew counts itself
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
	fetchNewGo(timeout)
}

// fetchNewGo is FetchNew in the background, counted in newRunning before
// it returns when there is work, so a page reading FetchingNew straight
// after is told; with none nothing is started or counted. Which accounts
// are due is known at once: no CLI is asked who is signed in for a list
// FetchNew fetches, since every built-in that asked one is retired and its
// plugin lists its own accounts.
func fetchNewGo(timeout time.Duration) {
	run := func(fetch func()) {
		newRunning.Add(1)
		go func() {
			defer newRunning.Add(-1)
			defer fetchingNew.Store(false)
			fetch()
		}()
	}
	if !newFetches.TryLock() {
		// behind a FetchNew under way, which the page doesn't wait on
		run(func() { FetchNew(timeout) })
		return
	}
	due, _ := newDue(All())
	if len(due) == 0 {
		newFetches.Unlock()
		fetchingNew.Store(false)
		return
	}
	run(func() {
		defer newFetches.Unlock()
		fetchDue(due, timeout)
	})
}

// newRunning counts the FetchNew calls under way, those started in the
// background included.
var newRunning atomic.Int32

// FetchingNew reports whether a FetchNew is under way (start-up's, or one
// started behind a page): accounts' lists may be on their way still. One
// started behind a page counts from its start, while it still picks the
// accounts due: a page whose FetchNewBehind found it so returned at once,
// and was told none was on its way until the one picking counted itself.
func FetchingNew() bool { return newRunning.Load() > 0 || fetchingNew.Load() }

// FetchNew asks each signed-in account whose vendor list magpie hasn't
// fetched yet for it, each for at most timeout. Start-up does this for the
// accounts there then; an account signed in while magpie runs (in magpie or
// in the agent's own app) otherwise showed magpie's built-in list, fewer
// models than the vendor serves, until Refresh was clicked (#204). One that
// fails is asked again after newFetchRetry, not each time. It reports
// whether an account is left without its list (one that failed, or that
// failed less than newFetchRetry ago), for a caller with no page to ask
// again from (the terminal, magpie serve) to try once more later.
func FetchNew(timeout time.Duration) (missing bool) {
	newRunning.Add(1)
	defer newRunning.Add(-1)
	newFetches.Lock()
	defer newFetches.Unlock()
	return fetchNewHeld(timeout)
}

// fetchNewHeld is FetchNew with newFetches held.
func fetchNewHeld(timeout time.Duration) (missing bool) {
	due, skipped := newDue(All())
	if fetchDue(due, timeout) {
		missing = true
	}
	// one waiting to be asked again, unless another's fetch listed it
	for _, f := range skipped {
		if _, ok := f.p.Listed(); !ok || f.decide {
			missing = true
		}
	}
	return missing
}

// newFetch is a provider FetchNew asks for its list, and whether for its
// decision models.
type newFetch struct {
	p      Provider
	decide bool
}

// newDue is those of ps FetchNew asks for their lists now, and those
// skipped as they failed less than newFetchRetry ago; newFetches held.
func newDue(ps []Provider) (due, skipped []newFetch) {
	for _, p := range ps {
		// a list of decision models fetched before magpie kept their
		// windows and input, or not fetched yet (ARNO on Discord)
		// one switched off isn't asked (leftAlone): a signed-in
		// agent's list asks its CLI or the vendor
		if p.Off {
			continue
		}
		decide := p.decideListDue()
		if !decide && (p.Account == nil || !p.Ready()) {
			continue
		}
		// a plugin's accounts were listed with the plugin's providers
		if _, ok := p.Listed(); ok && !decide {
			continue
		}
		if t, ok := newFetches.m[p.ID]; ok && time.Since(t) < newFetchRetry {
			skipped = append(skipped, newFetch{p, decide})
			continue
		}
		due = append(due, newFetch{p, decide})
	}
	return due, skipped
}

// fetchDue asks each of due for its list, each for at most timeout, and
// reports whether one failed; newFetches held.
func fetchDue(due []newFetch, timeout time.Duration) (failed bool) {
	for _, f := range due {
		p := f.p
		// listed since by another's fetch, as a plugin's accounts are
		if _, ok := p.Listed(); ok && !f.decide {
			continue
		}
		newFetches.m[p.ID] = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		var err error
		if f.decide && p.Account == nil {
			_, err = p.fetchDecide(p.ownRequests(p.Via(ctx), probelog.KindList))
		} else {
			_, err = p.Fetch(ctx)
		}
		if err != nil {
			log.Println(p.ID + ": " + err.Error())
			failed = true
		}
		cancel()
	}
	return failed
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
	ctx = p.ownRequests(ctx, probelog.KindList) // a detection's list too
	if u := strings.TrimSpace(p.ModelsURL); u != "" {
		// asked where the user said, and nowhere else: the base URLs
		// list nothing, or the wrong thing
		anthropic := p.Chat == "" && p.Responses == ""
		if anthropic {
			ctx = p.listAuth(ctx)
		}
		ms, err := catalog.FetchURL(ctx, u, p.Key, anthropic, p.listHeadersAt(u))
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
	// the bases this fetch reached, whether or not their list was one
	// another base of the same provider had already listed (#1006):
	// the dedup is by the URL a list answered at and how it was asked,
	// which is catalog.FetchAtListed's, and a base string is not a list
	asked := map[string]bool{}
	answered := map[string]bool{} // of the bases that answered, not those kept from
	got := map[string]bool{}      // of the list URLs merged in, by the URL each answered at
	// the lists already fetched for this key, by the URL each answered at:
	// two of the provider's bases that are one list are one request (#1006)
	listed := catalog.NewListed()
	codexAt := "" // the list URL the Responses base answered at
	for _, proto := range p.Speaks() {
		protoBase := p.Base(proto)
		if proto == Gemini {
			if p.Gemini == "" {
				continue // Factory's, whose list is its sign-in's
			}
			if p.AIStudioPair() {
				// AI Studio's OpenAI-compatible list is its Gemini API's
				// list, asked once
				continue
			}
			if p.IsOpenCode() {
				// OpenCode's Gemini models are in the list its other
				// APIs share, asked at its chat URL; it has no Gemini
				// list of its own
				continue
			}
			// listed as the Gemini API lists them, the key in its header,
			// and merged like any other protocol's list (#1346)
			asked[protoBase] = true
			ms, _, err := catalog.FetchGemini(ctx, protoBase, p.Key, p.listHeadersAt(protoBase))
			if err != nil {
				if !slices.Contains(errs, err.Error()) {
					errs = append(errs, err.Error())
				}
				// a base that could not be asked keeps the models it listed
				// last time, as any other protocol's does (#904)
				dead = append(dead, protoBase)
				continue
			}
			if base == "" {
				// the base it answered at, as it will be asked next time
				base = protoBase
			}
			// what it listed is a side like any other protocol's, so that a
			// fetch finding this base down keeps its own models instead of
			// reading a picked Gemini model as gone (#904, yetone's review
			// of #1006)
			sides = append(sides, side{protoBase, ms})
			answered[protoBase] = true
			for _, m := range ms {
				add(m)
			}
			continue
		}
		lctx := ctx
		if proto == Anthropic {
			lctx = p.listAuth(ctx) // a vendor that takes one key header alone
		}
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
		asked[protoBase] = true
		ms, u, err := catalog.FetchAtListed(lctx, protoBase, p.Key, proto == Anthropic, p.listHeadersAt(protoBase), listed)
		if err == nil && proto == Anthropic && HostOf(protoBase) == openRouterHost {
			// the pages of the list are followed to its end, and what
			// comes back is still not a list of models to serve
			err = openRouterNamespacedErr(protoBase)
		}
		if err != nil {
			if errors.Is(err, catalog.ErrAsked) {
				// every list this base would ask was asked already, and
				// none of them answered: the base says nothing new, which
				// is not a refusal of it and is not reported as one
				continue
			}
			if !slices.Contains(errs, err.Error()) {
				errs = append(errs, err.Error())
			}
			dead = append(dead, protoBase)
			continue
		}
		if proto == Responses {
			codexAt = u // asked again as Codex asks it (codexEntries)
		}
		if got[u] {
			// one list, already asked and merged under another base's
			// spelling of the same URL: not a second endpoint (#1006)
			continue
		}
		sideBase := protoBase
		if base == "" {
			// the first protocol that answered keeps the base: the drawer
			// list is fetched beside it, and it is saved with the models
			base = protoBase
			if proto != Anthropic {
				base = p.fixV1(ctx, base, u)
			}
			// the base as it will be asked next time, /v1 and all, so
			// this side is found again by the fetch after this one
			sideBase = base
		}
		sides = append(sides, side{sideBase, ms})
		answered[sideBase] = true
		got[u] = true
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
	if codexAt != "" {
		out = p.codexEntries(ctx, codexAt, out)
	}
	// the image models its list leaves out (AIHubMix's gpt-image-2)
	// the parts are kept only where several bases were asked: a single one
	// that fails is the fetch that fails, and its list is kept as it is
	var parts map[string][]catalog.Model
	if len(asked) > 1 {
		parts = sidesOf(sides)
	}
	out = p.withOllamaWindows(ctx, base, out)
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

// planOnly is the models of the plan p is at, when it is one of a preset
// that lists nothing at a region that doesn't either: its models are the
// plan's, and no list is asked for.
func (p Provider) planOnly() ([]catalog.Model, bool) {
	pr := Preset(p.Preset)
	if pr == nil || !pr.NoList || strings.TrimSpace(p.ModelsURL) != "" || p.listRegion(pr) {
		return nil, false
	}
	return p.planModels(nil), true
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
// or one of Volcengine's plans) is a plan with those.
func (p Provider) planModels(ms []catalog.Model) []catalog.Model {
	pr := Preset(p.Preset)
	if pr == nil {
		return ms
	}
	models := pr.Models
	if r := p.regionOf(pr); r != nil && r.Models != nil {
		models = r.Models
	}
	if p.IsVertex() && p.Vertex != nil {
		models = vertexModels(p.Vertex.Location) // each location serves its own
	}
	if p.VertexExpress() {
		models = vertexExpressModels()
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
		var images map[string]bool
		if r := p.regionOf(pr); r != nil {
			images = r.Images
		}
		for _, id := range models {
			m := catalog.Model{ID: id, Name: id}
			if v, ok := images[id]; ok {
				m.ImageInput, m.Images = &v, v
			}
			out = append(out, m)
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
		if catalog.Window(m.Context) == 0 {
			out[i].Context = catalog.ContextOf(id)
		}
		if m.Output == 0 {
			out[i].Output = catalog.OutputOf(id)
		}
	}
	return out
}

// PlanEmbeddings are the embedding models explicitly included in the preset
// region's plan. They are resolved by the embeddings API but kept out of
// agents' chat-model lists.
func (p Provider) PlanEmbeddings() []catalog.Model {
	return p.planMedia(func(r *Region) []string { return r.Embeddings }, false, false, catalog.ServesEmbedding)
}

// PlanRerankers are the rerank models the preset names, as PlanEmbeddings
// are its embedding models.
func (p Provider) PlanRerankers() []catalog.Model {
	return p.planMedia(func(r *Region) []string { return r.Rerankers }, false, false, catalog.ServesRerank)
}

// PlanRetrieval are the preset's models of kind's (catalog.ServesEmbedding
// or ServesRerank), or both for "".
func (p Provider) PlanRetrieval(kind string) []catalog.Model {
	switch kind {
	case catalog.ServesEmbedding:
		return p.PlanEmbeddings()
	case catalog.ServesRerank:
		return p.PlanRerankers()
	case "":
		return append(p.PlanEmbeddings(), p.PlanRerankers()...)
	}
	return nil
}

// PlanDrawers are the image-generation models explicitly included in the
// preset region's plan. They are separate from its chat models.
func (p Provider) PlanDrawers() []catalog.Model {
	return p.planMedia(func(r *Region) []string { return r.Drawers }, true, false, "")
}

// PlanVideos are the video-generation models explicitly included in the
// preset region's plan.
func (p Provider) PlanVideos() []catalog.Model {
	return p.planMedia(func(r *Region) []string { return r.Videos }, false, true, "")
}

func (p Provider) planMedia(ids func(*Region) []string, draws, films bool, serves string) []catalog.Model {
	pr := Preset(p.Preset)
	if pr == nil {
		return nil
	}
	// a preset with no regions names its own (Voyage AI's)
	own := &Region{Embeddings: pr.Embeddings, Rerankers: pr.Rerankers}
	// keys on plans of their own (KeyAccount.Plan): each plan's, once
	views := []Provider{p}
	if p.plansMixed() {
		views = planViews(views)
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, v := range views {
		r := v.regionOf(pr)
		if r == nil {
			r = own
		}
		for _, id := range ids(r) {
			if !seen[id] {
				seen[id] = true
				out = append(out, catalog.Model{ID: id, Name: id, Provider: p.ID, Draws: draws, Films: films, Serves: serves})
			}
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
func (p Provider) fixV1(ctx context.Context, base, at string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || at != base+"/v1/models" && at != catalog.RelayOpenAIList(base) || catalog.Versioned(base) {
		return base
	}
	if p.servesAtBase(ctx, base) {
		// a URL the user typed is kept as typed: a vendor that lists its
		// models under /v1 but serves chat at the base has no /v1 to add
		// (JNDliuliuM on X)
		return base
	}
	return p.setV1(base)
}

// servesAtBase reports whether p's OpenAI URL that is base has its
// endpoint there: a POST of an empty body to base/chat/completions (or
// /responses) that is answered with anything but 404 or 405 — a 400 for the
// missing model, a 401 for the key — names a route that exists. A request
// that fails to connect says nothing, and is taken as the URL being right.
func (p Provider) servesAtBase(ctx context.Context, base string) bool {
	for _, e := range []struct{ at, end string }{{p.Chat, "/chat/completions"}, {p.Responses, "/responses"}} {
		if strings.TrimRight(strings.TrimSpace(e.at), "/") != base {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+e.end, strings.NewReader("{}"))
		if err != nil {
			cancel()
			return true
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range AuthHeaders(p, Chat) {
			req.Header.Set(k, v)
		}
		res, err := p.Do(http.DefaultClient, req)
		if err != nil {
			cancel()
			return true
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		cancel()
		if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed {
			return true
		}
	}
	return false
}

// setV1 is base with the /v1 it was given without, set on p's OpenAI
// URLs that were base, as saved; base when p can't be saved.
func (p Provider) setV1(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
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
	old = append(old, catalog.LiveServing(p.ID, "")...)
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
	// each model's keys as a set beside its list, and the models of the
	// list before that each key saw: with a thousand keys, looking along
	// the lists for every key and model took seconds (@NetRozee on X)
	var has []map[string]bool
	add := func(m catalog.Model, id string) int {
		i, ok := at[m.ID]
		if !ok {
			m.Keys = nil
			at[m.ID], i = len(out), len(out)
			out = append(out, m)
			from = append(from, nil)
			has = append(has, map[string]bool{})
		} else {
			out[i].ImageInput = sharedImageInput(out[i].ImageInput, m.ImageInput)
			out[i].Images = out[i].Images && m.Images
		}
		if !has[i][id] {
			has[i][id] = true
			out[i].Keys = append(out[i].Keys, id)
		}
		return i
	}
	var oldOf map[string][]int // a key's models in old, made when one fails
	keptOld := map[int]bool{}  // the models of old kept for a key that failed
	var base string
	var lastErr error
	for _, k := range keys {
		id := keyID(k.Key)
		q := p.WithKey(k)
		var l fetched
		var err error
		if ms, ok := q.planOnly(); ok {
			// a key at a plan with no list to ask (Volcengine's Agent
			// Plan beside its pay as you go): the plan's models, unasked
			l = fetched{models: ms, base: cmp.Or(q.Base(Chat), q.Base(Anthropic))}
		} else {
			l, err = q.fetchOne(ctx)
		}
		if err != nil {
			lastErr = err
			if oldOf == nil {
				oldOf = map[string][]int{}
				for j, m := range old {
					for _, kid := range m.Keys {
						if js := oldOf[kid]; len(js) == 0 || js[len(js)-1] != j {
							oldOf[kid] = append(js, j)
						}
					}
				}
			}
			for _, j := range oldOf[id] {
				i := add(old[j], id)
				// each model kept once: its bases are worked out from
				// its keys, the same whichever of them failed
				if !keptOld[j] {
					keptOld[j] = true
					deadKept = append(deadKept, deadModel{i, old[j].Keys})
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
// A key no list marks — its own wasn't answered, or it was added since —
// is unknown, and serves: a failed list is not an empty one (#1598, the
// first of five keys left out of every request). The lists are read whole,
// the models that draw or make videos among them: a text key never says
// it serves an image model only another key lists (#1657).
func (p Provider) Serves(k KeyAccount, model string) bool { return p.Serving(model)(k) }

// Serving is Serves for model, for each of the provider's keys in turn:
// the lists are read once, not once a key. Read for every key of every
// request, a provider of a thousand keys had its list's file looked at a
// thousand times, and each model's thousand keys looked along, before a
// request went out: a tenth of a second a request, seconds for five
// thousand keys (@NetRozee on X).
func (p Provider) Serving(model string) func(KeyAccount) bool {
	x, ok := p.keyLists()
	if !ok || len(x.listed) == 0 {
		// no key is listed to any model: every key serves it, and the
		// model's own id needn't be looked up
		return func(KeyAccount) bool { return true }
	}
	// the vendor's lists name the model by its own id (SetModelID)
	keys, found := x.of[p.OwnModelID(model)]
	return func(k KeyAccount) bool {
		id := keyID(k.Key)
		return !found || len(keys) == 0 || !x.listed[id] || keys[id]
	}
}

// keyLists is which keys the provider's lists name: every key any model
// is listed to, and each model's own, as sets. The last of a model listed
// twice is its.
type keyLists struct {
	from   []catalog.Model // the lists they were made from
	listed map[string]bool
	of     map[string]map[string]bool
}

// keyListsMade is each provider's keyLists, made again when its lists are
// read anew: a list read once is the same list until its file changes
// (filememo), and is held here, so a new one is never at its address.
var keyListsMade = struct {
	sync.Mutex
	m map[string]*keyLists
}{m: map[string]*keyLists{}}

func (p Provider) keyLists() (*keyLists, bool) {
	live, ok := p.liveAll()
	if !ok || len(live) == 0 {
		return nil, false
	}
	keyListsMade.Lock()
	defer keyListsMade.Unlock()
	if x := keyListsMade.m[p.ID]; x != nil && len(x.from) == len(live) && &x.from[0] == &live[0] {
		return x, true
	}
	x := &keyLists{from: live, listed: map[string]bool{}, of: make(map[string]map[string]bool, len(live))}
	for _, m := range live {
		set := make(map[string]bool, len(m.Keys))
		for _, id := range m.Keys {
			set[id] = true
			x.listed[id] = true
		}
		x.of[m.ID] = set
	}
	keyListsMade.m[p.ID] = x
	return x, true
}

// ServesNone is the user's Select none (NoModels) with nothing picked:
// none of its models are served, its balance and usage still read.
func (p Provider) ServesNone() bool { return p.NoModels && len(p.Models) == 0 }

// waitsForPicks is a provider whose every pick its list stopped listing
// (Delisted, none left in Models) less than pickWait ago: none are served
// until one comes back, not the vendor's whole list as for a provider
// nothing was picked of. Past pickWait, or set aside with no time kept,
// the picks are taken as gone for good and the list is served as if none
// were picked; they still come back with a list that has them.
func (p Provider) waitsForPicks() bool {
	return len(p.Models) == 0 && len(p.Delisted) > 0 && !p.DelistedAt.IsZero() && time.Since(p.DelistedAt) < pickWait
}

// pickWait is how long a provider whose every pick left its list waits for
// one to come back. A list short for its plugin's restart has them again
// within minutes (relistSoon asks again in 5); a model its vendor took off
// (WorkBuddy AI's deepseek-v4.1-flash) never comes back, and an hour with
// none of the provider's models served is as long as the user is left
// without them.
const pickWait = time.Hour

// Exposed lists the models magpie offers to agents for this provider: the
// user's picks; else the preset's; else everything, when that is few, the
// newest of each family only under Best in family; none after Select none
// (ServesNone).
func (p Provider) Exposed() []catalog.Model {
	if p.ServesNone() || p.waitsForPicks() {
		return nil
	}
	return p.ExposedOf(p.Available())
}

// ExposedOf is Exposed of avail, p.Available() as the caller has already
// read it: an editor's look at a provider read the vendor's list four
// times over, each built afresh with its names and levels.
func (p Provider) ExposedOf(avail []catalog.Model) []catalog.Model {
	if p.ServesNone() || p.waitsForPicks() {
		return nil
	}
	byID := make(map[string]catalog.Model, len(avail))
	for _, m := range avail {
		byID[m.ID] = m
	}
	pick := func(ids []string) []catalog.Model {
		var unlisted []catalog.Model
		for _, id := range ids {
			if _, ok := byID[id]; !ok {
				// with the levels the gateway fits an effort to (Known),
				// not the none effortsOf takes a vendor's word for: the
				// vendor's list doesn't have it, so it gave no word (#597)
				m := catalog.Model{ID: id, Name: id, Provider: p.firstCatalog(), Efforts: p.knownElsewhere(id)}
				unlisted = append(unlisted, m)
			}
		}
		if !p.IsAzure() {
			// Available has already named the listed models. Reserve their
			// names and ids without refilling ones it left as ids.
			unlisted = catalog.Named(unlisted, avail...)
		}
		for _, m := range unlisted {
			byID[m.ID] = m
		}
		out := make([]catalog.Model, 0, len(ids))
		for _, id := range ids {
			out = append(out, byID[id])
		}
		return out
	}
	picks := p.Models
	if _, _, ok := p.live(); ok && p.Account != nil && (p.Account.unusable != nil || p.Account.listedOnly) {
		// A Copilot account is served what its list offers it, less what
		// it was refused: a pick its list doesn't have (#371: gpt-6-luna,
		// not in a Student plan's) or that it was refused is left out;
		// with none left, as if none were picked. A ChatGPT account's
		// too: a pick made while its list had gpt-5.5 stayed offered after
		// the list dropped it, and the backend refused every request for
		// it (#4772). It comes back when the list has it again.
		picks = slices.DeleteFunc(slices.Clone(picks), func(id string) bool { _, ok := byID[id]; return !ok })
	} else if p.Account != nil && p.Account.unusable != nil {
		// with no list fetched, a pick the account can't be served (a
		// ZCode Start Plan account's GLM-5.3) is left out all the same
		picks = slices.DeleteFunc(slices.Clone(picks), p.Account.unusable)
	}
	if len(picks) > 0 {
		noAPI := p.NoAPI()
		return slices.DeleteFunc(pick(p.withNewlyListed(picks, avail)), func(m catalog.Model) bool { return noAPI(m.ID) })
	}
	// a model listed on none of the APIs the provider has a URL for is
	// left out before the first 24 are taken (#2094)
	avail = p.servable(avail)
	// Best in family: the newest of each family the list has
	if p.BestInFamily {
		avail = catalog.BestInFamily(avail)
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

// servable is avail less the models NoAPIFor leaves out.
func (p Provider) servable(avail []catalog.Model) []catalog.Model {
	noAPI := p.NoAPI()
	return slices.DeleteFunc(slices.Clone(avail), func(m catalog.Model) bool { return noAPI(m.ID) })
}

// NewestInFamily is the models Best in family would serve of avail, the
// vendor's list (Available), before the first 24 are taken: the newest of
// each family among those it can be asked for (catalog.BestInFamily),
// whichever mode is on, so the editor can draw them before it is saved.
func (p Provider) NewestInFamily(avail []catalog.Model) []catalog.Model {
	return catalog.BestInFamily(p.servable(avail))
}

// Listing is a provider's models as the Providers page draws them: the
// vendor's list (Available), what of it is exposed (ExposedOf) and the
// newest of each family (NewestInFamily).
type Listing struct{ Available, Exposed, Newest []catalog.Model }

// Listing is p's Listing, built afresh.
func (p Provider) Listing() Listing {
	avail := p.Available()
	return Listing{avail, p.ExposedOf(avail), p.NewestInFamily(avail)}
}

// KeptListing is the Listing of a provider All gave, kept across requests
// with the catalog (keptOf) while nothing it is built from changed: each
// poll of the Providers page built every provider's list and its newest
// of each family again, 9 MB a poll in a real setup. What it gives is
// shared, so read only.
func KeptListing(p Provider) Listing {
	return keptOf("listing:"+p.ID, func() Listing {
		listingsBuilt.Add(1)
		l := p.Listing()
		return Listing{slices.Clip(l.Available), slices.Clip(l.Exposed), slices.Clip(l.Newest)}
	})
}

// listingsBuilt counts the Listings KeptListing built, for a test.
var listingsBuilt atomic.Int64

// NoAPIFor says the provider has a URL for none of the APIs the vendor's
// list serves model on (or the one the user set for it): a relay's Claude
// listed on /messages alone, at a provider given only a chat URL. Agents
// aren't offered it, as no request for it could be answered (#2094). Only
// a provider of keys, whose URLs the user typed: a sign-in's APIs are
// magpie's own. A model whose list says no API, and a provider with no URL
// at all, are never left out here.
func (p Provider) NoAPIFor(model string) bool { return p.NoAPI()(model) }

// NoAPI is NoAPIFor for each model of a list: the APIs the provider has a
// URL for are found once for the list, not once for each model (they were
// most of what NoAPIFor allocated, 16 MB of twenty window polls).
func (p Provider) NoAPI() func(model string) bool {
	if p.Account != nil {
		return func(string) bool { return false }
	}
	speaks := p.Speaks()
	if len(speaks) == 0 {
		return func(string) bool { return false }
	}
	return func(model string) bool {
		apis := p.apisAmong(speaks, model)
		return len(apis) > 0 && !slices.ContainsFunc(speaks, func(s Protocol) bool { return slices.Contains(apis, s) })
	}
}

// Picks are the models the user picked, with those the vendor has listed
// since beside them (withNewlyListed): what agents are served of the
// picks, and what an editor starts its picks from. None when nothing is
// picked.
func (p Provider) Picks() []string {
	if len(p.Models) == 0 {
		return p.Models
	}
	return p.withNewlyListed(p.Models, p.Available())
}

// PicksOf is Picks of avail, p.Available() as the caller has already read
// it (ExposedOf).
func (p Provider) PicksOf(avail []catalog.Model) []string {
	if len(p.Models) == 0 {
		return p.Models
	}
	return p.withNewlyListed(p.Models, avail)
}

// withNewlyListed is picks with the models the vendor has listed since
// they were saved put first, when they held every model listed then. A
// Claude account whose user picked every model (Select all, or the TUI's
// toggles) was served the list as it was then, and never Haiku 5.5 when
// models.dev listed it (wakaka on Discord, after 9b51428e). A model that
// was listed and left unpicked stays left out, and so do new ones beside
// picks that left any out. A subscription's picks saved before PickedFrom
// was kept are taken to have held every model when they hold every one
// released up to the newest of them, and a model released after it is the
// new one. A provider of keys has no such rule: its picks without
// PickedFrom are served as they are (yuancjun: one old model picked of a
// relay's ten served all ten).
func (p Provider) withNewlyListed(picks []string, avail []catalog.Model) []string {
	if len(avail) == 0 {
		return picks
	}
	picked := make(map[string]bool, len(picks))
	for _, id := range picks {
		picked[id] = true
	}
	var isNew func(catalog.Model) bool
	if p.PickedFrom != nil {
		listed := make(map[string]bool, len(p.PickedFrom))
		for _, id := range p.PickedFrom {
			listed[id] = true
		}
		isNew = func(m catalog.Model) bool { return !listed[m.ID] }
	} else if p.Account == nil {
		// a provider of keys: which models were listed when the picks were
		// saved wasn't kept, and "released after the newest pick" read a
		// pick of one old model as "every model listed since". It served
		// the whole list, and the next Save wrote it down as the picks.
		// A subscription keeps the rule: its list is the catalog's, and
		// Select all is a state its editor offers.
		return picks
	} else {
		newest := ""
		for _, m := range avail {
			if picked[m.ID] && m.Released > newest {
				newest = m.Released
			}
		}
		if newest == "" {
			return picks
		}
		isNew = func(m catalog.Model) bool { return m.Released > newest }
	}
	var added []string
	for _, m := range avail {
		if picked[m.ID] {
			continue
		}
		if !isNew(m) {
			return picks // one the user saw and left unpicked
		}
		added = append(added, m.ID)
	}
	if len(added) == 0 {
		return picks
	}
	return append(added, picks...)
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
// or those the user gave it when it has none known (magpie model spec
// --reasoning on a custom provider's model). The gateway fits a request
// to these. Levels the user kept of a model's own only narrow what is
// offered (#92): the vendor is still asked at any level it has.
func (p Provider) Efforts(model string) []string {
	if Reasonless(p.ID, model) {
		return nil
	}
	if all := p.Known(model); len(all) > 0 {
		return all
	}
	return effortsKept(nil, settings.Shared().ModelEfforts[p.ID+"/"+model])
}

// Thinks reports whether the model reasons: it has levels, its list says
// it thinks, or models.dev says most of those serving it do (Entry's
// Reasoning).
func (p Provider) Thinks(model string) bool {
	if Reasonless(p.ID, model) {
		return false
	}
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

// PaidBesideFree says model costs its subscription's credits while the
// subscription serves other models at no cost to them (catalog.Model's
// Free: WorkBuddy's "x0.00", Qoder's is_free, Cline's free models). Its
// credits running out then says nothing of those: they draw on none
// (Creat.wu on Discord). False for a model not on the list, or where no
// model is free.
func (p Provider) PaidBesideFree(model string) bool {
	listed, paid, free := false, false, false
	for _, m := range p.Available() {
		if strings.EqualFold(m.ID, model) {
			listed, paid = true, !m.Free
		}
		free = free || m.Free
	}
	return listed && paid && free
}

// FreeModel says model costs nothing on p: its vendor serves it at no cost
// to the subscription's allowance or credits (catalog.Model's Free:
// WorkBuddy's "x0.00", Qoder's is_free, Cline's free models, an OpenCode Zen
// free one), or, with no such word from the vendor, it is listed at $0 on
// every count — a vendor's free model a catalog prices at 0, as
// mimo-v2.6-flash-free is, and a key's free model, which has no allowance
// to draw on and so is judged by its price alone.
//
// A model neither listed free nor priced is not free: nothing says it costs
// nothing, and the routes that go by this put it with the paid ones, so a
// pool without free models behaves as In order. The user's word is
// something: every model of a provider they count free is (CountFree).
func (p Provider) FreeModel(model string) bool {
	if p.CountFree {
		return true
	}
	for _, m := range p.Available() {
		if strings.EqualFold(m.ID, model) {
			if m.Free {
				return true
			}
			break
		}
	}
	pr, ok := p.ListPrice(model)
	return ok && pr.Input == 0 && pr.Output == 0 && pr.CacheRead == 0 && pr.CacheWrite == 0
}

// PricedFree says model is listed at $0 on p (ListPrice) on every count
// and at every size: a free model a catalog prices at 0 there, as
// OpenRouter's foo:free, OpenCode Zen's -free ones or NVIDIA NIM's. The
// pickers say free beside it (jiangguochao666 on X), where catalog.Model's
// Free has only the vendor's own word. It is p's price alone: the same id
// on another provider is that one's, and its maker's (makerListPrice) or
// a plan's catalog's says nothing of p's. A model nothing prices is not free,
// nor one the user set a price for that isn't 0: their word is the price
// Usage counts.
func (p Provider) PricedFree(s settings.Settings, model string) bool {
	if pr, ok := userPrice(s, p, model); ok {
		return zeroPrice(pr)
	}
	c := p.priceCatsOf()
	pr, ok, from := p.listPriceIn(c, model)
	return p.listedFree(c, model, pr, ok, from)
}

// listedFree is PricedFree for a model the user set no price for, given
// what listPriceIn found.
func (p Provider) listedFree(c priceCats, model string, pr catalog.Price, ok bool, from int) bool {
	switch {
	case !ok || !zeroPrice(pr):
		return false
	case from == pricedMaker:
		// its maker gives it away (Zhipu's glm-4.7-flash): a relay that
		// serves it may not
		return false
	case from == pricedCatalog:
		// a plan's catalog (zai-coding-plan) lists what its members pay
		// per token, nothing, not what the model costs: only a catalog
		// that is no plan's says it is free
		// copied only when a plan's is among them: every $0 model of a
		// catalog was a copy on each Providers ask
		isPlan := func(c string) bool { return strings.Contains(c, "-plan") }
		plain := c.priced
		if slices.ContainsFunc(plain, isPlan) {
			plain = slices.DeleteFunc(slices.Clone(plain), isPlan)
		}
		var buf [4]string
		for _, m := range c.priceNames(buf[:0], model) {
			if pr, ok := catalog.PricedBy(plain, m); ok {
				return zeroPrice(pr)
			}
		}
		return false
	}
	return true
}

// userPrice is the price the user set for p's model (settings'
// ModelPrices): for that model, for every model of p, then for the model
// from any provider (*/model), still the user's word, so before any list
// price.
func userPrice(s settings.Settings, p Provider, model string) (catalog.Price, bool) {
	if len(s.ModelPrices) == 0 {
		return catalog.Price{}, false
	}
	// the keys made on the stack and looked up without a copy: made as
	// strings, for every model of every provider on each Providers ask,
	// they were most of what pricing a model allocated
	var buf [256]byte
	for i := range 3 {
		k := buf[:0]
		switch i {
		case 0:
			k = append(append(append(k, p.ID...), '/'), model...)
		case 1:
			k = append(append(k, p.ID...), "/*"...)
		default:
			k = append(append(k, settings.AnyProvider...), strings.ToLower(strings.TrimSpace(model))...)
		}
		if m, ok := s.ModelPrices[string(k)]; ok {
			if pr, bad := m.Price(); bad == "" {
				// a Claude model's 1-hour cache write left out: 2× input,
				// but where Copilot bills it at the 5-minute price
				if !p.cacheWritesAlike() {
					catalog.OneHourFor(model, &pr)
				}
				return pr, true
			}
		}
	}
	return catalog.Price{}, false
}

// zeroPrice is a price of nothing on every count and every tier.
func zeroPrice(pr catalog.Price) bool {
	if pr.Input != 0 || pr.Output != 0 || pr.CacheRead != 0 || pr.CacheWrite != 0 || pr.CacheWrite1h != 0 {
		return false
	}
	for _, t := range pr.Tiers {
		if t.Input != 0 || t.Output != 0 || t.CacheRead != 0 || t.CacheWrite != 0 {
			return false
		}
	}
	return true
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
	if Reasonless(p.ID, model) {
		return true
	}
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
// none: the vendor says it has none to pick from — unless it says the
// model thinks and lists its family with levels (catalog's levels: a model
// just added, gpt-6.1-sol).
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
// providers giving any give it (a Volcengine endpoint's glm-5.3-flash),
// else, for one newer than the catalog, its maker's line's
// (catalog.KinEfforts).
func borrowedEfforts(id string) []string {
	if e, ok := catalog.ListedByBorrowed(makerCatalogs(), id); ok {
		return e
	}
	if e := catalog.EffortsOf(id); len(e) > 0 {
		return e
	}
	// one newer than this machine's catalog: its maker's line's levels
	// (gpt-6.1-sol, NextCommit on Discord)
	if e, ok := catalog.KinEfforts(makerCatalogs(), id); ok {
		return e
	}
	// a proxy's variant of a known model, named for the level or speed
	// it is served at (CLIProxyAPI's gemini-3.8-flash-high,
	// claude-opus-5-5-high, grok-3-mini-fast; nianshou5 on Discord): the
	// model's own levels, which the proxy fits a request's effort to
	if base, ok := catalog.Unsuffixed(id); ok {
		return borrowedEfforts(base)
	}
	// a dated snapshot of a known model, as OpenAI's own list names them
	// and a relay passing it on does (gpt-6.1-sol-2026-09-29): the model's
	// levels. Codex offered it low, medium and high alone, no xhigh, max
	// or Ultra (maomao_00001 on Discord)
	if base := datedSuffix.ReplaceAllString(id, ""); base != id && base != "" {
		return borrowedEfforts(base)
	}
	return nil
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
// account is at Anthropic's. A remote magpie's is what that magpie counts
// the model at, as its list says (remotePrice). A model no catalog prices
// is at its vendor's own list price where a moved built-in's plugin serves
// it (vendorListPrice), else at the price its plugin lists it at
// (pluginListPrice), else, when the
// plugin says the plan serves it free, costs nothing (pluginFree). Codex's
// auto-review through the Codex subscription costs nothing too, as OpenAI
// serves it a ChatGPT sign-in (codexReviewFree).
func (p Provider) ListPrice(model string) (catalog.Price, bool) {
	pr, ok, _ := p.listPrice(model)
	return pr, ok
}

// Where ListPrice found a price: p's own word (its catalog, its plugin,
// its vendor's code), the catalog p names, or the model's maker's
// catalog, which says nothing of what p charges.
const (
	pricedOwn = iota
	pricedCatalog
	pricedMaker
)

// Pricer prices a provider's models as ListPrice and PricedFree do, with
// what is the provider's alone (the catalogs it is priced from) worked
// out once: the Providers page prices every model of every provider on
// each ask, and worked out for each model, twice, it was 4 MB of what
// forty asks allocated with real accounts.
type Pricer struct {
	p Provider
	c priceCats
}

// Pricer is p's Pricer.
func (p Provider) Pricer() Pricer { return Pricer{p, p.priceCatsOf()} }

// Prices is model's ListPrice on the Pricer's provider, and its
// PricedFree under s.
func (r Pricer) Prices(s settings.Settings, model string) (list catalog.Price, listed, free bool) {
	pr, ok, from := r.p.listPriceIn(r.c, model)
	if up, set := userPrice(s, r.p, model); set {
		free = zeroPrice(up)
	} else {
		free = r.p.listedFree(r.c, model, pr, ok, from)
	}
	return pr, ok, free
}

// priceCats are the catalogs listPrice prices p's models from
// (priceCatalogsOf), and the same without the membership catalogs.
// kimi is p.kimiCodeMember().
type priceCats struct {
	raw, priced []string
	kimi        bool
}

func (p Provider) priceCatsOf() priceCats {
	raw := p.priceCatalogsOf()
	return priceCats{raw, priceCatalogs(raw), p.kimiCodeMember()}
}

// listPrice is ListPrice, and where it found the price.
func (p Provider) listPrice(model string) (catalog.Price, bool, int) {
	return p.listPriceIn(p.priceCatsOf(), model)
}

// listPriceIn is listPrice with p's catalogs given.
func (p Provider) listPriceIn(c priceCats, model string) (catalog.Price, bool, int) {
	if own := p.OwnModelID(model); own != model {
		// a model listed by an id the user gave it is priced as the
		// provider's own id of it first, then as the id it is listed by
		if pr, ok, from := p.listPriceIn(c, own); ok {
			return pr, true, from
		}
	}
	if pr, ok := p.remotePrice(model); ok {
		return pr, true, pricedOwn
	}
	if p.clineFreeModel(model) || p.kiloFreeModel(model) || p.codexReviewFree(model) {
		// served at no cost: not at the price of the model it is free of
		return catalog.Price{}, true, pricedOwn
	}
	if pr, ok := p.cursorFastPrice(model); ok {
		return pr, true, pricedOwn
	}
	if pr, ok := p.factoryFastPrice(model); ok {
		return pr, true, pricedOwn
	}
	if pr, ok := p.grokFastPrice(model); ok {
		return pr, true, pricedOwn
	}
	// the names on the stack: made for each model priced, they were
	// 1.5 MB of forty Providers asks
	var buf [4]string
	for _, m := range c.priceNames(buf[:0], model) {
		if pr, ok := catalog.PricedBy(c.priced, m); ok {
			return pr, true, pricedCatalog
		}
		if pr, ok := makerListPrice(m); ok {
			return pr, true, pricedMaker
		}
	}
	if slices.Contains(c.raw, "anthropic") {
		// a gated Claude model models.dev prices nowhere (Claude Mythos
		// 5.1): Anthropic's price, as it told Claude Code
		if pr, ok := claudeCodePrice(model); ok {
			return pr, true, pricedMaker
		}
	}
	if pr, ok := p.vendorListPrice(model); ok {
		return pr, true, pricedOwn
	}
	if pr, ok := p.pluginListPrice(model); ok {
		return pr, true, pricedOwn
	}
	if p.pluginFree(model) {
		return catalog.Price{}, true, pricedOwn
	}
	return catalog.Price{}, false, pricedOwn
}

// priceNames are the ids ListPrice looks model up by in a catalog,
// appended to dst.
func (c priceCats) priceNames(dst []string, model string) []string {
	names := pricedNamesTo(dst, model)
	if c.kimi {
		if id, ok := kimiCodeAPI[strings.ToLower(strings.TrimSpace(model))]; ok {
			names = append(names, id)
		}
	}
	return names
}

// priceCatalogsOf are the catalogs ListPrice prices p's models from: the
// ones it names, else the one at its endpoint (EndpointCatalog).
func (p Provider) priceCatalogsOf() []string {
	if c := p.EndpointCatalog(); c != "" {
		return []string{c}
	}
	return p.Catalogs()
}

// vendorCatalogs are the models.dev catalogs of the vendors a moved
// built-in's plugin sends its calls to, by the subscription's id: what
// the vendor's own API lists a model at, as its preset is priced
// (EndpointCatalog). A plugin's provider has no such catalog of its own.
var vendorCatalogs = map[string]string{CommandCodePlanID: "commandcode"}

// vendorListPrice is model's price in its vendor's own catalog
// (vendorCatalogs), for a model no maker's price is found for: Command
// Code's deepseek/deepseek-v4.1-flash, which models.dev's DeepSeek lists
// only as deepseek-flash, a canonical of deepseek-v4.1-flash-1017, so its
// calls on the plan were counted at no price at all (treeman on Discord).
// Only after the maker's, so a model its maker prices keeps that price.
func (p Provider) vendorListPrice(model string) (catalog.Price, bool) {
	c, ok := vendorCatalogs[p.ID]
	if !ok {
		return catalog.Price{}, false
	}
	return catalog.PricedBy([]string{c}, model)
}

// pluginListPrice is the price the plugin serving p lists model at
// (pluginPrice): Devin's own, from GetCliModelConfigs, for its SWE models
// that no catalog has. Only where no catalog prices the model, as with
// pluginFree, so no price a catalog gave changes.
func (p Provider) pluginListPrice(model string) (catalog.Price, bool) {
	pp, ok := PluginOf(p.ID)
	if !ok {
		return catalog.Price{}, false
	}
	m, ok := pluginModel(pp, model)
	if !ok {
		return catalog.Price{}, false
	}
	return pluginPrice(m)
}

// pluginFree is whether the plugin serving p lists model as one its plan
// serves at no cost (plugin.Model.Free): Devin's SWE-2, which Devin gives
// the "Free" cost tier and no allowance counts, Qoder's and WorkBuddy's
// x0 models. Such a model is the vendor's own, sold per token nowhere, so
// no catalog prices it, and without this its calls showed no cost at all
// (太陽と向日葵 on Discord, on SWE-2 after Devin's quota was used up).
// Only where no list price is known: a free model a catalog prices keeps
// that price, as it did.
func (p Provider) pluginFree(model string) bool {
	pp, ok := PluginOf(p.ID)
	if !ok {
		return false
	}
	m, ok := pluginModel(pp, model)
	return ok && m.Free
}

// EndpointCatalog is the models.dev catalog a provider the user added by
// its endpoint alone is priced from: the one models.dev says serves its
// models at that address, when exactly one does (#2052, mengyianx: a
// provider at AMD's own https://developer.amd.com.cn/radeon/api/v1 had no
// price for Qwen3.8-27B, which models.dev's amd lists at $0 there, while
// its maker sells no such model). Only a key's provider with no catalog
// named counts: a catalog the user or a preset gave stands, and a
// subscription's account is priced as its own code says. A plan's catalog
// is never one: it lists what its membership charges per token, $0, not
// what the model costs (membershipCatalogs).
func (p Provider) EndpointCatalog() string {
	if p.Account != nil || len(p.Catalogs()) > 0 {
		return ""
	}
	var found []string
	// the Anthropic base is kept as its root (normalize), where models.dev
	// writes an Anthropic API's with its /v1
	urls := []string{p.Chat, p.Responses, p.Anthropic, p.Gemini}
	if p.Anthropic != "" {
		urls = append(urls, p.Anthropic+"/v1")
	}
	for _, u := range urls {
		for _, id := range catalog.AtEndpoint(u) {
			if !slices.Contains(found, id) {
				found = append(found, id)
			}
		}
	}
	if len(found) != 1 || strings.Contains(found[0], "-plan") || slices.Contains(membershipCatalogs, found[0]) {
		return ""
	}
	return found[0]
}

// cursorFast is a Cursor id of a Grok model in its fast mode, at an effort
// or not, at a window or not: grok-4.7-fast, grok-4.7-low-fast,
// grok-4.7-xhigh-fast@500k. m[1] is the model it is fast of.
var cursorFast = lazyre.New(`^((?:.*/)?grok-[0-9][^/@]*?)(?:-(?:minimal|low|medium|high|xhigh|extra-high))?-fast(?:@[0-9]+k)?$`)

// cursorFastPrices are what Cursor bills a Grok model in its fast mode
// per million tokens, as its own model list says
// (https://cursor.com/docs/models, read 2026-10-12): Grok 4.7 (Fast) $4
// input, $1 cache read, $12 output ("Fast mode is available at 2x
// pricing"), and "Fast mode for long context (>256k) is billed at 3x
// standard rates" ($6, $1.5, $18). No catalog lists these ids, so a call
// on Cursor's grok-4.7-fast, or on a routing group of it, had no cost at
// all (TradingLife on Discord).
var cursorFastPrices = map[string]catalog.Price{
	"grok-4.7": {Input: 4, CacheRead: 1, Output: 12,
		Tiers: []catalog.Tier{{Above: 256000, Input: 6, CacheRead: 1.5, Output: 18}}},
}

// servedByCursor is whether p is a Cursor account: the built-in's or the
// Cursor plugin's.
func (p Provider) servedByCursor() bool {
	if p.Account == nil {
		return false
	}
	return p.Account.Agent == "cursor" || p.PluginProvider() == "cursor"
}

// cursorFastPrice is Cursor's price for its fast id of a Grok model
// (cursorFastPrices), on a Cursor account only: elsewhere a "-fast" Grok
// id is a model of its own (xAI's grok-4-fast is $0.2 / $0.5), not this
// mode, and Grok Build's grok-4.7-build-fast has no published price.
func (p Provider) cursorFastPrice(model string) (catalog.Price, bool) {
	if !p.servedByCursor() {
		return catalog.Price{}, false
	}
	m := cursorFast.FindStringSubmatch(strings.ToLower(strings.TrimSpace(model)))
	if m == nil {
		return catalog.Price{}, false
	}
	base := m[1]
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	pr, ok := cursorFastPrices[base]
	if ok {
		pr.Tiers = slices.Clone(pr.Tiers)
	}
	return pr, ok
}

// grokBuildFast is Grok Build's id of a Grok model's fast variant, as its
// own model list offers it (grok-4.7-build-fast, and at a window of its
// own, grok-4.7-build-fast@500k). m[1] is the model it is fast of.
var grokBuildFast = lazyre.New(`^(grok-[0-9][0-9.]*)-build-fast(?:@[0-9]+k?)?$`)

// grokFastPrice is a Grok account's (the built-in's or the Grok plugin's)
// price for its fast variant of a Grok model: twice the model's own, every
// tier with it, as xAI says it sells it ("We also serve a fast variant
// with twice the output speed at twice the price",
// https://x.ai/news/grok-4-7, read 2026-10-15). No catalog lists
// grok-4.7-build-fast, so every call a routing group of Cursor's and
// Grok's Grok 4.7 fast sent to Grok was unpriced, and the group's cost
// left out half its calls (TradingLife on Discord, after cbc96b599 priced
// Cursor's half). Only on a Grok account: no other vendor serves the id.
func (p Provider) grokFastPrice(model string) (catalog.Price, bool) {
	if p.Account == nil || p.Account.Agent != "grok" && p.PluginProvider() != "grok" {
		return catalog.Price{}, false
	}
	base, ok := GrokBuildFast(model)
	if !ok {
		return catalog.Price{}, false
	}
	pr, ok := p.ListPrice(base)
	if !ok {
		return catalog.Price{}, false
	}
	return pr.Times(GrokFastTimes), true
}

// GrokFastTimes is how many times its model's price xAI sells a Grok
// model's fast variant at (grokFastPrice).
const GrokFastTimes = 2

// GrokBuildFast is the Grok model id is Grok Build's fast variant of
// (grok-4.7-build-fast is grok-4.7's), and whether it is one: for a Grok
// CLI session, which names it bare, priced as grokFastPrice prices it.
func GrokBuildFast(id string) (string, bool) {
	m := grokBuildFast.FindStringSubmatch(strings.ToLower(strings.TrimSpace(id)))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// factoryFastTimes is how many times its base model's list price each
// Fast Mode model Factory offers is billed at by the model's maker
// (droid's ids, the factory plugin's MODELS): Anthropic's fast mode is 2x
// on Opus 5.5 ($8 / $40), Opus 5 and Opus 4.8 ($10 / $50), cache prices
// with it; OpenAI's priority tier is 2.5x on GPT-5.5 and 2x on GPT-5.4 and
// GPT-5.3-Codex, cache with it (models.dev's vercel gpt-5.5-fast $12.5 /
// $75 / $1.25 cached, gpt-5.4-fast $5 / $30, gpt-5.3-codex-fast $3.5 /
// $28). No catalog lists Factory's -fast ids, so a call on one was priced
// at its served model's standard price, Factory naming the base model in
// its reply (plugins#119, psylch).
var factoryFastTimes = map[string]float64{
	"claude-opus-5-5": 2, "claude-opus-5": 2, "claude-opus-4-8": 2,
	"gpt-5.5": 2.5, "gpt-5.4": 2, "gpt-5.3-codex": 2,
}

// servedByFactory is whether p is a Factory account: the built-in's or the
// Factory plugin's.
func (p Provider) servedByFactory() bool {
	if p.Account == nil {
		return false
	}
	return p.Account.Agent == "factory" || p.PluginProvider() == "factory"
}

// factoryFastPrice is a Factory Fast Mode model's list price: its base
// model's at factoryFastTimes, tiers and cache included, on a Factory
// account only. Elsewhere a "-fast" id is a model of its own.
func (p Provider) factoryFastPrice(model string) (catalog.Price, bool) {
	if !p.servedByFactory() {
		return catalog.Price{}, false
	}
	base, ok := strings.CutSuffix(strings.ToLower(strings.TrimSpace(model)), "-fast")
	times := factoryFastTimes[base]
	if !ok || times == 0 {
		return catalog.Price{}, false
	}
	pr, ok := p.ListPrice(base)
	if !ok {
		return catalog.Price{}, false
	}
	return pr.Times(times), true
}

// membershipCatalogs are models.dev catalogs of a membership's own
// endpoints that list every model at $0: what the membership charges per
// token, not what the model costs (#1370, maicent: the Usage page counted
// 937K tokens of Kimi Code's kimi-for-coding at ¥0.000). No price is read
// from them, so a call there is priced at its model's API price, as a
// Claude or Codex account's is, or left unpriced when it has none. Xiaomi
// MiMo's three Token Plan catalogs list all nine of its models at $0 too,
// and stood before xiaomi's own prices, so a pay-as-you-go call was
// counted at $0 (#1873, mengyianx).
var membershipCatalogs = append(slices.Clone(kimiCodePlans), "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams")

// kimiCodePlans are Kimi Code's membership catalogs, whose models are
// priced as the Kimi API's (kimiCodeAPI).
var kimiCodePlans = []string{"kimi-code-plan-global", "kimi-code-plan-cn"}

// priceCatalogs are cs without the membershipCatalogs.
func priceCatalogs(cs []string) []string {
	return slices.DeleteFunc(slices.Clone(cs), func(c string) bool { return slices.Contains(membershipCatalogs, c) })
}

var makerPriceCatalogs = sync.OnceValue(func() []string { return priceCatalogs(makerCatalogs()) })

// kimiCodeMember is whether p is served at a Kimi Code membership's
// endpoints, by its catalog.
func (p Provider) kimiCodeMember() bool {
	return slices.ContainsFunc(p.Catalogs(), func(c string) bool { return slices.Contains(kimiCodePlans, c) })
}

// kimiCodeAPI is the Kimi API model a Kimi Code model id is, as Kimi
// Code's docs name it (https://www.kimi.com/code/docs/en/, 2026-11-01):
// k3 is K3, k3-256k "K3 256K context version … the same results as K3",
// k3.1-pro "K3.1 Pro", k3.1-pro-highspeed "K3.1 Pro Highspeed", and
// kimi-for-coding-highspeed "K2.7 Code HighSpeed". Kimi's API prices them
// per token (https://platform.kimi.ai/docs/pricing/chat, as models.dev's
// moonshotai entry lists them, read 2026-10-31: kimi-k3 $3 in, $15 out,
// $0.30 cached; kimi-k3.1-pro $2, $10, $0.20; kimi-k3.1-pro-highspeed
// $4, $20, $0.20; kimi-k2.7-code-highspeed $1.90, $8, $0.38).
// kimi-for-coding is "K2.8 Preview", which the API doesn't sell, so it has
// no API price and stays unpriced.
var kimiCodeAPI = map[string]string{
	"k3":                        "kimi-k3",
	"k3-256k":                   "kimi-k3",
	"k3.1-pro":                  "kimi-k3.1-pro",
	"k3.1-pro-highspeed":        "kimi-k3.1-pro-highspeed",
	"kimi-for-coding-highspeed": "kimi-k2.7-code-highspeed",
}

// MakerPrice is a model's list price as the first vendor among the presets
// that makes the models it serves lists it; for a call whose provider has
// gone since.
func MakerPrice(model string) (catalog.Price, bool) {
	for _, m := range pricedNames(model) {
		if pr, ok := makerListPrice(m); ok {
			return pr, true
		}
	}
	return catalog.Price{}, false
}

// makerListPrice is a model id's list price at its maker: the price of
// the models.dev provider that lists it as its own model
// (catalog.MakerPriced), else the first maker preset's that lists it. The
// lab's own comes first, so a model a maker preset resells (Mistral's
// DeepSeek V4.1 Flash) is costed at what its lab charges, the official
// price a subscription's call is estimated at (bestony on Discord, on
// Command Code).
func makerListPrice(m string) (catalog.Price, bool) {
	if pr, ok := catalog.MakerPriced(makerPriceCatalogs(), m); ok {
		return pr, true
	}
	return catalog.PricedBy(makerPriceCatalogs(), m)
}

// SamePriceIn is the maker's list price of the model the user said
// providerID's model is the same as (settings' ModelSameAs, #2344): for an
// id of the provider's own that no list prices, as WorkBuddy's kimi-k3-1
// or a plugin's qfmodel. It is the last place a price is looked for, after
// the user's own, the provider's list and the model's maker's by its own
// id, and is never another provider's tariff. MakerPrice reads the id as
// any vendor spells it, with its prefix ("moonshotai/kimi-k3") or not.
func SamePriceIn(s settings.Settings, providerID, model string) (catalog.Price, bool) {
	same := strings.TrimSpace(s.ModelSameAs[providerID+"/"+model])
	if same == "" {
		return catalog.Price{}, false
	}
	return MakerPrice(same)
}

// EffectivePrice is what a call to a provider's model costs the user: the
// price they set for that model, or for every model of that provider, or for
// that model from any provider (settings' ModelPrices), else the provider's own list price, else its
// maker's, else the maker's of the model the user said it is the same as
// (SamePriceIn), each at the provider's price rate (PriceRate), and again
// at the rate of the time-of-day window the call falls in (RateWindows,
// #2463) — the window's rate on a price the user set as well, the price
// rate alone leaving it as set. The second return is false only when no
// price is known at all,
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
	return EffectivePriceAt(s, providerID, model, time.Now())
}

// EffectivePriceAt is EffectivePriceIn at the time given: a record's own
// time when one is priced, now for a price shown before a call. A caller
// pricing many records takes BasePriceIn once per model and multiplies
// each record's window rate on itself, so one read of the prices serves
// every time of day.
func EffectivePriceAt(s settings.Settings, providerID, model string, at time.Time) (catalog.Price, bool) {
	// A price is keyed by the id the provider has now, so one written before
	// a rename is read under the id it has. Only an id, or one the provider
	// was renamed from, resolves; anything else is looked up as given, so a
	// key under a display name counts only for a caller naming that name
	// too. Nothing writes such a key — SetModelPrice re-keys the way
	// SetModelName does — which is what keeps the two from drifting.
	return priceIn(s, All(), providerID, model, at)
}

// priceIn is EffectivePriceIn with the providers read already, for a
// caller pricing many models at once: All() copies every provider each
// time it is read, so a list read it once a model, 30 providers' copies
// for each of hundreds of models (EntryPricer).
func priceIn(s settings.Settings, all []Provider, providerID, model string, at time.Time) (catalog.Price, bool) {
	p, known := byIDOrWasIn(all, providerID)
	if !known {
		p = Provider{ID: providerID}
	}
	return priceOf(s, p, known, model, at)
}

// BasePriceIn is EffectivePriceAt without a rate window's rate: what the
// provider bills outside every window, and the third return is its
// windows, for RateAt to read each record's time against. A caller
// pricing many records holds both and multiplies the rates itself, as
// the usage ledger's Pricer does, so one read serves every time of day.
func BasePriceIn(s settings.Settings, providerID, model string) (catalog.Price, bool, []RateWindow) {
	p, known := byIDOrWas(providerID)
	if !known {
		p = Provider{ID: providerID}
	}
	pr, ok := priceOf(s, p, known, model, time.Time{})
	return pr, ok, p.RateWindows
}

// UserBasePriceIn is BasePriceIn without the catalogs' word: the price the
// user set for a provider's model themselves, a zero included — the model's
// own, else every model of that provider's, else every model's (settings'
// ModelPrices) — with the peak a vendor bills by the hour marked on it and
// the provider's windows as the third return, as BasePriceIn hands them.
// It is the first place BasePriceIn looks, alone for a caller that asks for
// the user's word before the catalogs' (#5340).
func UserBasePriceIn(s settings.Settings, providerID, model string) (catalog.Price, bool, []RateWindow) {
	p, known := byIDOrWas(providerID)
	if !known {
		p = Provider{ID: providerID}
	}
	pr, ok := userPrice(s, p, model)
	if !ok {
		return catalog.Price{}, false, nil
	}
	pr.Peak = p.PeakOf(model)
	return pr, true, p.RateWindows
}

// priceOf is EffectivePriceAt for a provider already looked up, for a
// caller pricing many models at once: byIDOrWas reads every provider's
// accounts each time. The window's rate scales every price the call is
// counted at, a price the user set for the model included (#2463); the
// price rate alone leaves that price as set. A zero at prices without a
// rate window's rate, as BasePriceIn asks for.
func priceOf(s settings.Settings, p Provider, known bool, model string, at time.Time) (catalog.Price, bool) {
	pr, ok := tariffOf(s, p, known, model)
	if ok && !at.IsZero() {
		if w := p.RateAt(at); w > 0 && w != 1 {
			pr = pr.Times(w)
		}
	}
	return pr, ok
}

// PriceAt is what a call to model at p costs when made at at, as Usage
// counts one made then: EffectivePriceAt for p as the caller holds it (a
// routing group's member), its Peak still set, for the caller to apply
// with Price.In and tell whether it did. A model served at no cost there
// (FreeModel) costs nothing.
func (p Provider) PriceAt(s settings.Settings, model string, at time.Time) (catalog.Price, bool) {
	if p.FreeModel(model) {
		return catalog.Price{}, true
	}
	return priceOf(s, p, true, model, at)
}

// tariffOf is a price without a rate window's rate: what the user set
// for the model as they set it, else the provider's list price, else its
// maker's, each at the price rate. Either is doubled in the peak hours of
// a vendor that bills by the hour (PeakOf).
func tariffOf(s settings.Settings, p Provider, known bool, model string) (catalog.Price, bool) {
	id := p.ID
	if pr, ok := userPrice(s, p, model); ok {
		// a vendor that bills by the hour doubles any price in its peak
		// hours: the user's is the off-peak one, as its list price is, so
		// setting one keeps the split (treeman on Discord)
		pr.Peak = p.PeakOf(model)
		return pr, true
	}
	if !known {
		if p.codexReviewFree(model) { // signed out since: still a ChatGPT sign-in's
			return catalog.Price{}, true
		}
		pr, ok := MakerPrice(model)
		if ok {
			if k := p.PeakOf(model); k != "" {
				pr.Peak = k
			}
		} else {
			pr, ok = SamePriceIn(s, id, model)
		}
		// a call Copilot served before it was signed out is billed as
		// Copilot bills it still
		if ok && p.cacheWritesAlike() {
			pr = withoutOneHour(pr)
		}
		return pr, ok
	}
	pr, ok := p.ListPrice(model)
	if !ok {
		pr, ok = MakerPrice(model)
	}
	if !ok {
		pr, ok = SamePriceIn(s, id, model)
	} else if k := p.PeakOf(model); k != "" {
		// billed by the hour it is made at (PeakOf), as a price the user
		// set above is, never the one of the model they said it is
		pr.Peak = k
	}
	// what the provider bills against it (#819)
	if ok && p.PriceRate > 0 {
		pr = pr.Times(p.PriceRate)
	}
	if ok && p.cacheWritesAlike() {
		pr = withoutOneHour(pr)
	}
	return pr, ok
}

// cacheWritesAlike reports whether p bills a cache write at one price however
// long it is kept. Copilot does: its copilot_usage counts a 1-hour write as
// the cache_write a 5-minute one is, at the same price (#3435:
// claude-opus-5.5's 15,671-token 1-hour write cost 7,844,300,000 nano-AIU,
// $5 per million, where Anthropic's 2× input would be $8).
func (p Provider) cacheWritesAlike() bool { return p.ID == "copilot" }

// withoutOneHour is a price with no 1-hour cache write of its own, so an
// hour's write is billed as a 5-minute one (catalog.Price.OneHour): the 2×
// input catalog.OneHourFor gave a Claude model's list price is Anthropic's,
// not every vendor's. The tiers are copied, as they may be the catalog's.
func withoutOneHour(pr catalog.Price) catalog.Price {
	pr.CacheWrite1h = 0
	if len(pr.Tiers) > 0 {
		pr.Tiers = slices.Clone(pr.Tiers)
		for i := range pr.Tiers {
			pr.Tiers[i].CacheWrite1h = 0
		}
	}
	return pr
}

// EntryPriceIn is what a call to an entry of the catalog costs the user,
// as the usage pages count it: its model's EffectivePriceIn, or a group's
// when every member costs the same, none in a fast mode, since which of
// them answers isn't known beforehand. find is GroupFinder's.
func EntryPriceIn(st settings.Settings, find func(string) (Group, []Member, bool), e Entry) (catalog.Price, bool) {
	return EntryPricer(st, find)(e)
}

// EntryPricer is EntryPriceIn for a whole list of entries, at the one
// read of the providers it makes: what a /v1/models or an agent's model
// file prices row by row.
func EntryPricer(st settings.Settings, find func(string) (Group, []Member, bool)) func(Entry) (catalog.Price, bool) {
	// one time for the whole list, as the providers are one read too:
	// a rate window (#2463) holds or not for every row alike
	return entryPricerAt(st, find, time.Now())
}

// entryPricerAt is EntryPricer with a provider's rate window taken at
// now, or left out for a zero now.
func entryPricerAt(st settings.Settings, find func(string) (Group, []Member, bool), now time.Time) func(Entry) (catalog.Price, bool) {
	all := sharedAll() // priceIn only reads them
	return func(e Entry) (catalog.Price, bool) {
		if e.Group == "" {
			return priceIn(st, all, e.Provider.ID, e.Model, now)
		}
		_, ms, ok := find(e.ID)
		if !ok || len(ms) == 0 {
			return catalog.Price{}, false
		}
		var first catalog.Price
		for i, m := range ms {
			pr, ok := priceIn(st, all, m.Provider.ID, m.Model, now)
			if !ok || m.Fast || i > 0 && !pr.Same(first) {
				return catalog.Price{}, false
			}
			first = pr
		}
		return first, true
	}
}

// AgentPricer is EntryPricer for the model list magpie writes into an
// agent's own config (Pi's, omp's, OpenCode's cost): a group whose
// members cost the same is priced as EntryPricer prices it, and one whose
// members differ at the member it sends to first — a manual group's pick,
// else its first member not switched off — since that is the member a
// request goes to while it answers, and an agent that prices calls by its
// config (omp's custom provider, taotao7 on Discord) otherwise counts
// every call to the group at 0. A first member in a fast mode, or with no
// known price, leaves the group unpriced, as before. The list is read
// afresh at each Sync, so the price follows the group's members.
//
// A provider's rate window (#2463) is left out: the file holds whatever
// was written at the last Sync, which nothing rewrites when a window
// opens or closes, so a window's price taken then would stay on through
// the hours outside it. magpie's own count (Usage, a key's budget) takes
// each call's window.
func AgentPricer(st settings.Settings, find func(string) (Group, []Member, bool)) func(Entry) (catalog.Price, bool) {
	var base time.Time
	same, all := entryPricerAt(st, find, base), sharedAll()
	return func(e Entry) (catalog.Price, bool) {
		if pr, ok := same(e); ok || e.Group == "" {
			return pr, ok
		}
		_, ms, ok := find(e.ID)
		if !ok || len(ms) == 0 || ms[0].Fast {
			return catalog.Price{}, false
		}
		return priceIn(st, all, ms[0].Provider.ID, ms[0].Model, base)
	}
}

// FoundOnce is find answering each group once: an agent's list asked a
// group's members for its compaction, its price twice and its protocols
// and tiers twice, each walking the group's members and resolving each
// (~8 MB in 2 minutes of an idle magpie's list rebuilds). What it gives
// is shared between the calls: only for reading.
func FoundOnce(find func(string) (Group, []Member, bool)) func(string) (Group, []Member, bool) {
	type found struct {
		g  Group
		ms []Member
		ok bool
	}
	var (
		mu sync.Mutex
		of = map[string]found{}
	)
	return func(id string) (Group, []Member, bool) {
		mu.Lock()
		f, ok := of[id]
		mu.Unlock()
		if !ok {
			f.g, f.ms, f.ok = find(id)
			mu.Lock()
			of[id] = f
			mu.Unlock()
		}
		return f.g, f.ms, f.ok
	}
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
//
// It looks among the shared providers and copies only the one it found:
// through All every price of every model of a Sessions poll copied every
// provider's keys, headers and models (~10 MB per 40 polls on a real
// history, #2278).
func byIDOrWas(id string) (Provider, bool) {
	p, ok := byIDOrWasIn(sharedAll(), id)
	if ok {
		p = p.clone() // the caller's to change, as All's are
	}
	return p, ok
}

// byIDOrWasIn is byIDOrWas among the providers given.
func byIDOrWasIn(all []Provider, id string) (Provider, bool) {
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
var grokEffort = lazyre.New(`^((?:.*/)?grok-[0-9][^/]*?)-(?:minimal|low|medium|high|xhigh|extra-high)$`)

// grokWindow is a Grok id offered at a context window of its own
// ("grok-4.7@500k", the grok plugin's windowsOf): the same model, asked for
// at a longer window. A window no multiple of 1000 the id names without the
// k.
var grokWindow = lazyre.New(`^((?:.*/)?grok-[0-9][^/@]*)@[0-9]+k?$`)

// modelWindow is an id a plugin offers a model by at one context window
// of its own, the window in thousands or millions of tokens after an @:
// the Cursor plugin's claude-opus-5-5@300k, claude-opus-5-5@1m,
// gpt-5.5@272k (one entry per size Cursor's picker has), the grok
// plugin's grok-4.7@500k. It is the same model asked for at that window,
// at the same price per token; a long prompt's own rate is the price's
// tier (Price.At), read from the call's tokens, not from the window.
var modelWindow = lazyre.New(`^([^@]+)@[0-9]+[km]$`)

// pricedNames are the ids a model is priced by, in order: its own, then,
// for an id at a window (claude-opus-5-5@300k, grok-4.7@500k), the model
// it is that window of (modelWindow), so a Cursor account's sized ids are
// priced as their model and not left without a cost (RyuJeXu on X); for a
// Grok id named at an effort, the model it is that effort of — the same
// model, at the same price (#224). Codex Auto Review uses GPT-5.6 Luna
// according to OpenAI's rate card (2026-09-30):
// https://help.openai.com/en/articles/11481834-chatgpt-rate-card-business-enterpriseedu-credit-based-pricing
// This is a list-price estimate, not evidence of a response's served model.
// Other names without catalog prices stay unpriced here (grok-4.7-build,
// grok-4.7-mini, grok-4.7-fast); a Cursor account's fast ids are priced at
// Cursor's own fast rate (Provider.cursorFastPrice), and a Grok account's
// grok-4.7-build-fast at twice grok-4.7's (Provider.grokFastPrice).
func pricedNames(model string) []string { return pricedNamesTo(nil, model) }

// pricedNamesTo is pricedNames appended to dst.
func pricedNamesTo(dst []string, model string) []string {
	out := append(dst, model)
	if model == "codex-auto-review" {
		out = append(out, "gpt-5.6-luna")
	}
	l := strings.ToLower(strings.TrimSpace(model))
	// a regexp only for an id with an @: every model of every provider's
	// list is priced on each Providers poll
	if strings.Contains(l, "@") {
		if m := modelWindow.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	// both patterns name grok-, for the same reason
	if !strings.Contains(l, "grok-") {
		return out
	}
	// a Grok window the id names without a k (grok-4.7@131072)
	if m := grokWindow.FindStringSubmatch(l); m != nil && !slices.Contains(out, m[1]) {
		out = append(out, m[1])
	}
	if m := grokEffort.FindStringSubmatch(l); m != nil {
		out = append(out, m[1])
	}
	return out
}

// PricedName is the catalog ID used for a list-price estimate. Prefer a
// directly listed model before falling back to a documented alias.
func PricedName(model string) string {
	n := pricedNames(model)
	for _, name := range n {
		if _, ok := makerListPrice(name); ok {
			return name
		}
	}
	return n[len(n)-1]
}

// PricedNameFor is PricedName for a call to that provider: a Kimi Code
// membership's k3 is priced as the API's kimi-k3 (kimiCodeAPI).
func PricedNameFor(providerID, model string) string {
	p, ok := byIDOrWas(providerID)
	if !ok {
		p = Provider{ID: providerID}
	}
	if p.codexReviewFree(model) {
		return model // free, not priced as another model (codexReviewFree)
	}
	if ok && p.kimiCodeMember() {
		if id, ok := kimiCodeAPI[strings.ToLower(strings.TrimSpace(model))]; ok {
			return id
		}
	}
	return PricedName(model)
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
	ID      string `json:"id"`                // what the agent sends magpie
	Model   string `json:"model"`             // what magpie sends the vendor
	Name    string `json:"name"`              // the user's name for it, when they gave one (SetModelName)
	Default string `json:"default,omitempty"` // the model's own name, when the user gave it another
	// Plain is a remote magpie's model's name there alone, when Name is
	// its label there with that magpie's provider after it (catalog.Model's)
	Plain string `json:"-"`
	// By is the provider a remote magpie's list names as serving the
	// model there, the first in a chain of magpies (catalog.Model's)
	By         string    `json:"-"`
	Efforts    []string  `json:"efforts,omitempty"`
	Provider   *Provider `json:"-"`                // a group's: its first member's; shared, read only
	Group      string    `json:"group,omitempty"`  // set on a routing group (group.go)
	Named      bool      `json:"-"`                // a routing group the user made or changed: its name is theirs (Labels)
	Icons      []string  `json:"-"`                // a group's: its providers' icons, one per provider
	Images     bool      `json:"images,omitempty"` // takes images as input (a group's: a member does)
	ImageInput *bool     `json:"-"`                // explicit answer, nil when unknown
	// Inputs and Outputs are what the model's source listed it takes and
	// gives (catalog.Model's InputModalities, OutputModalities); see
	// Modalities
	Inputs, Outputs []string `json:"-"`
	// Context is the tokens a prompt may hold, when known (a group's: the
	// least of its members')
	Context int `json:"context,omitempty"`
	// WindowSet is set when Context is a window the user typed: on the
	// provider for the model or for all of its models (ContextOf), or on
	// the group (Group.Context, a size). Codex and Claude Code are then
	// told it whole rather than magpie's default working window
	// (CompactFor, #1809).
	WindowSet bool `json:"-"`
	// MaxContext is the most a prompt may hold when asked for, above
	// Context, as the provider's list says it (catalog.Model's: a Codex
	// entry's max_context_window, which Codex is handed and may work to)
	MaxContext int `json:"-"`
	// Output is the most tokens a reply may hold, when known (a group's:
	// the least of its members')
	Output int `json:"output,omitempty"`
	// OutputSet is set when Output is a Max output the user typed on the
	// provider, for the model or for all of its models (outputOf). An
	// agent's own cap on a vendor's figure gives way to it (#2064).
	OutputSet bool `json:"-"`
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
	// Reasonless is set on a model the user said takes no reasoning ask
	// (SetModelReasonless): it is sent none, and as a group's member it
	// leaves the group the levels the others have
	Reasonless bool `json:"-"`
	// AgentsV2 is set on a model offering Codex's Ultra that no ChatGPT
	// account answers for (a group's: none of its members): Codex is told
	// multi-agent V2 for it, so Ultra hands work to its agents, whose
	// tasks a magpie-served lead writes as text.
	AgentsV2 bool `json:"-"`
	// Tiers are the service tiers its list offers Codex on the model:
	// another magpie's, those it offers its own Codex (#1234); a provider
	// plugin's, those it says its vendor serves the model at (#2514)
	Tiers []string `json:"-"`
	// Released is the model's release day (YYYY-MM-DD) as its list or
	// models.dev says it, "" when neither does: what a group's template
	// weighs a newer model by (grouptemplate.go)
	Released string `json:"-"`
	// Codex is the entry the provider's own list gives the model in
	// Codex's shape (catalog.Model's), handed to Codex as it is
	Codex json.RawMessage `json:"-"`
	// AlwaysThinks, DynamicTools and DefaultEffort are what the provider's
	// own list says of the model (catalog.Model's): it always thinks, it
	// loads tools mid-conversation, it thinks at this effort unasked.
	// Kimi Code's list says them, and Kimi Code is told them. A group
	// has each one its members all share (groupSharedKimi)
	AlwaysThinks  bool   `json:"-"`
	DynamicTools  bool   `json:"-"`
	DefaultEffort string `json:"-"`
}

// Catalog lists the routing groups, then every exposed model of every ready
// provider not kept unlisted. A provider switched off has none in it.
func Catalog() []Entry { return catalogKeeping(nil) }

// catalogOf is the catalog agent's lists start from: Catalog, and for
// Codex the ChatGPT accounts' own models as well when they are kept out
// of the lists, the codex provider used only through routing groups
// (Provider.Unlisted) or a group listing alone (Group.Unlists). Codex
// reaches those on its own sign-in, its app lists them from the ChatGPT
// backend, and keeping them for routing groups is about magpie's offer to
// the other agents and /v1/models: Codex's list kept no Codex section, so
// the app lost GPT while Codex's picker still offered it (士奇 on
// Discord). The user takes them out of Codex's list there (HiddenModels,
// PickedModels), which the gateway follows (CodexNativeHidden).
//
// Every other agent is given Catalog: a group listing alone keeps a
// signed-in subscription's models out of its list too, as the option
// says. 88e6fb9cf kept them for every agent, so for a group made of
// subscription models, as most are, listing the group alone changed nothing
// an agent was listed (shaoyang. on Discord, 11-03).
func catalogOf(agent string) []Entry {
	if !strings.EqualFold(agent, "codex") {
		return Catalog()
	}
	return catalogKeeping(CodexOwn)
}

// CatalogOf is the catalog agent's lists start from, before its
// visibility and picks narrow it (CatalogFor): every model that agent may
// be given by name.
func CatalogOf(agent string) []Entry { return catalogOf(agent) }

// catalogKeeping is Catalog with the unlisted models keep says, too.
func catalogKeeping(keep func(Entry) bool) []Entry {
	entries := providerEntries()
	groups := groupEntries(entries) // held: copied into the caller's own
	// made once with room for every entry: cloned at the groups' size and
	// grown by append for the providers' models, it was 29 MB of what forty
	// polls of the Providers page allocated with real accounts
	out := append(make([]Entry, 0, len(groups)+len(entries)), groups...)
	kept := unlistedByGroups(entries)
	for _, e := range entries {
		if !e.Provider.Unlisted && len(kept[e.ID]) == 0 || keep != nil && keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// CatalogGroups is Catalog's routing groups, the caller's own, and how
// many entries Catalog has, without copying the providers' models: the
// Providers page reads only these, on every poll, and a whole Catalog
// was ~0.3 MB of each poll's garbage with real accounts.
func CatalogGroups() ([]Entry, int) {
	entries := providerEntries()
	groups := slices.Clone(groupEntries(entries))
	n := len(groups)
	kept := unlistedByGroups(entries)
	for _, e := range entries {
		if !e.Provider.Unlisted && len(kept[e.ID]) == 0 {
			n++
		}
	}
	return groups, n
}

// unlistedByGroups are the models, by "provider/model", that a group
// keeps out of the lists (Group.Unlists), with the names of the groups
// that do: those it names, a member fixed at an effort counting as its
// model, and those its patterns match.
func unlistedByGroups(entries []Entry) map[string][]string {
	by := groupsKeeping(entries)
	if by == nil {
		return nil
	}
	out := make(map[string][]string, len(by))
	for id, gs := range by {
		for _, g := range gs {
			if n := cmp.Or(g.Name, g.ID); !slices.Contains(out[id], n) {
				out[id] = append(out[id], n)
			}
		}
	}
	return out
}

// groupsKeeping is unlistedByGroups with the groups themselves, by the
// models, "provider/model", they keep out of the lists.
func groupsKeeping(entries []Entry) map[string][]Group {
	// read only: the file parsed once until it changes (storedFile)
	if !slices.ContainsFunc(storedFile().Groups, func(g Group) bool { return g.Unlists }) {
		return nil
	}
	out := map[string][]Group{}
	for _, g := range groupsIn(entries) {
		if !g.Unlists || g.Hidden || g.Disabled {
			continue
		}
		for _, id := range g.Members {
			if strings.HasPrefix(id, GroupPrefix) {
				continue
			}
			model, _ := memberEffortIn(entries, id)
			if p, m, ok := resolveIn(entries, model); ok && !slices.ContainsFunc(out[p.ID+"/"+m], func(o Group) bool { return o.ID == g.ID }) {
				out[p.ID+"/"+m] = append(out[p.ID+"/"+m], g)
			}
		}
	}
	return out
}

// GroupStandIn is a model a routing group listing alone (Group.Unlists)
// keeps out of the lists, with the catalog ids ("group/<id>") of the
// groups that stand in for it there.
type GroupStandIn struct {
	Entry  Entry
	Groups []string
}

// GroupStandIns are the models groups listing alone keep out of the
// lists, in the catalog's order: a caller the groups are not given (a
// gateway key that doesn't name them) is listed these in their place,
// those it may use, as no group stands in for them for it (luckymanjc on
// Discord: a key held to every Codex model was listed only the one no
// group of his listed alone). A provider kept for routing groups alone
// (Provider.Unlisted) is not among them: the user kept all of it out.
func GroupStandIns() []GroupStandIn {
	entries := providerEntries()
	by := groupsKeeping(entries)
	if len(by) == 0 {
		return nil
	}
	var out []GroupStandIn
	for _, e := range entries {
		gs := by[e.ID]
		if len(gs) == 0 || e.Provider.Unlisted {
			continue
		}
		ids := make([]string, len(gs))
		for i, g := range gs {
			ids[i] = GroupPrefix + g.ID
		}
		out = append(out, GroupStandIn{Entry: e, Groups: ids})
	}
	return out
}

// GroupKept is the models, by "provider/model", that a group listing
// alone (Group.Unlists) keeps out of agents' lists, with those groups'
// names: a provider's editor names them, as a model its own switch put
// back for agents is still left out by them.
// Held as MemberGroups is: shared, only for reading.
func GroupKept() map[string][]string {
	return heldFrom("group-kept", providerEntries(), groupKeptIn)
}

func groupKeptIn(entries []Entry) map[string][]string {
	kept := unlistedByGroups(entries)
	if len(kept) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, e := range entries {
		if gs := kept[e.ID]; len(gs) > 0 {
			out[e.ID] = slices.Clone(gs)
		}
	}
	return out
}

// Served is the catalog with the unlisted providers' models as well: every
// model a routing group can be made of, or a request can name. Like
// providerEntries it is shared, only for reading: a request asks it for
// each model it may go to, and a copy each time was ~380KB with 12
// providers of 20 models (#3725).
func Served() []Entry {
	return heldFrom("served", providerEntries(), func(entries []Entry) []Entry {
		return slices.Clip(append(slices.Clone(groupEntries(entries)), entries...))
	})
}

// Unlisted are the models Served has and agents aren't offered: those of
// the providers kept for routing groups (Provider.Unlisted), and those a
// group keeps out of the lists (Group.Unlists).
func Unlisted() []Entry {
	var out []Entry
	entries := providerEntries()
	kept := unlistedByGroups(entries)
	for _, e := range entries {
		if e.Provider.Unlisted || len(kept[e.ID]) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// providerEntries is the catalog without its groups.
func providerEntries() []Entry {
	return heldEntries(buildEntries)
}

func buildEntries() []Entry { return entriesOff(disabledNow()) }

// entriesOff is the providers' catalog less the models off reports.
func entriesOff(off func(pid, model string) bool) []Entry {
	return entriesOf(Provider.On, off)
}

// heldOffEntries are the models of the providers the user switched off,
// which the groups magpie finds keep (foundFrom). One without a key, a
// plugin not signed in, finds no group.
func heldOffEntries() []Entry {
	return heldOf("off entries", func() []Entry {
		return keptOf("off entries", func() []Entry {
			return slices.Clip(entriesOf(func(p Provider) bool { return p.Ready() && (p.Off || p.KeptToAgent()) }, disabledNow()))
		})
	})
}

// entriesOf is the catalog of the providers of, less the models off reports.
func entriesOf(of func(Provider) bool, off func(pid, model string) bool) []Entry {
	var out []Entry
	s := settings.Load()
	gone := retiredNow() // its vendor said it is retired (retired.go)
	for _, p := range All() {
		if !of(p) || p.DecideOnly() { // a dedicated decision API only routes
			continue
		}
		for _, m := range p.Exposed() {
			if off(p.ID, m.ID) { // the user turned it off (#1867)
				continue
			}
			if !p.isDecision(m) && !gone[p.ID+"/"+m.ID] {
				out = append(out, sharedEntry(&p, m, s))
			}
		}
	}
	return out
}

// entryFor is one of a provider's models as the catalog carries it: the
// window and the reply limit a request on it is routed and metered against,
// with what the user set taken over the vendor's list and models.dev.
func entryFor(p Provider, m catalog.Model, s settings.Settings) Entry {
	return sharedEntry(&p, m, s)
}

// sharedEntry is entryFor that keeps p itself in the entry, so every model of
// one provider shares one Provider (1.3KB each) instead of a copy a model.
func sharedEntry(p *Provider, m catalog.Model, s settings.Settings) Entry {
	ctx := p.windowIn(m, s)
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
		Images: images, ImageInput: imageInput, Context: ctx, WindowSet: p.ContextOf(m.ID) > 0, MaxContext: m.MaxContext, Output: output, OutputSet: outputOf(s, p.ID, m.ID) > 0, Free: m.Free, Rate: m.Rate, RateWas: m.RateWas, Tiers: m.Tiers, Released: m.Released,
		Inputs: m.InputModalities, Outputs: m.OutputModalities, Codex: m.Codex,
		AlwaysThinks: m.AlwaysThinks, DynamicTools: m.DynamicTools, DefaultEffort: m.DefaultEffort}
	if lv, ok := catalog.CodexLevels(m.Codex); ok {
		// the levels the vendor lists the model at in Codex's shape are
		// its word, over models.dev's for the same id (Decorate)
		e.Efforts = lv
	}
	if n, ok := modelNameIn(s.ModelNames, p.ID, m.ID); ok {
		e.Name, e.Default = n, name
	} else {
		e.Plain = m.Plain
	}
	if p.IsRemoteMagpie() {
		e.By = m.By
	}
	// a model that thinks still does with the levels the user kept or
	// none at all; one its source says nothing of thinks as most of the
	// providers serving it say (#402)
	e.Reasoning = m.Reasoning || m.AlwaysThinks || len(e.Efforts) > 0 || catalog.Thinks(m.ID)
	e.Efforts = effortsKept(e.Efforts, s.ModelEfforts[e.ID])
	// levels the user gave a model with none known say it thinks too, so
	// an agent told its levels isn't told it doesn't reason
	e.Reasoning = e.Reasoning || len(e.Efforts) > 0
	// Codex's Ultra on a model OpenAI offers it on, served by another
	// vendor (Copilot's gpt-6.1-sol, #656): a ChatGPT account's list says it
	// already. Its subagents take their tasks as text from a magpie-served
	// lead, so Codex is told V2 for it, where Ultra hands work to them
	// (codexcat.Entries).
	if (p.Account == nil || p.Account.Agent != "codex") && len(m.Codex) == 0 {
		e.Efforts = withUltra(m.ID, e.Efforts)
		e.AgentsV2 = slices.Contains(e.Efforts, "ultra")
	}
	// one the user said takes no reasoning ask offers none and is sent
	// none (SetModelReasonless), whatever its list says
	if s.ModelReasonless[e.ID] {
		e.Efforts, e.Reasoning, e.AgentsV2, e.Reasonless = nil, false, false, true
		e.AlwaysThinks, e.DefaultEffort = false, ""
	}
	return e
}

// Modalities are what the model takes and gives, as an agent's model list
// says them: every kind its source listed (a plugin's audio, video and pdf
// too, #1595), with image as Images has it, the user's say included. With
// no list, the input is text and image or text alone when whether it
// takes images is known, and nil when it isn't; output is nil unless said.
func (e Entry) Modalities() (in, out []string) {
	if e.Inputs != nil {
		in = slices.DeleteFunc(slices.Clone(e.Inputs), func(k string) bool { return k == "image" })
		if e.Images {
			at := 0
			if len(in) > 0 && in[0] == "text" {
				at = 1
			}
			in = slices.Insert(in, at, "image")
		}
	} else if e.Images {
		in = []string{"text", "image"}
	} else if e.ImageInput != nil {
		in = []string{"text"}
	}
	return in, slices.Clone(e.Outputs)
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
	if gid, ok := GroupIDOf(id); ok { // a found group by either of its ids
		for _, e := range entries {
			if e.Group == gid {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// Resolve maps an id an agent sent to a provider and the vendor's model id.
// It accepts catalog ids, "provider/model" for any model (exposed or not),
// and a bare model id: the first provider in the Providers order (All)
// that exposes it, else the one provider that lists it unexposed. The
// gateway asks GroupFor first, so with found groups on a bare id several
// providers serve is that group, its members in the same order (MOMO on
// Discord: glm-5.3 under opencode-go and a6api). magpie writes only
// provider/model ids into agents' configs, so a bare one is the user's.
// A group's id resolves to its first member.
func Resolve(id string) (Provider, string, bool) {
	// Claude Code's mark for a model with a 1M window; it drops it before
	// asking, but a value in its settings still has it
	id = ThroughSelf(strings.TrimSuffix(strings.TrimSpace(id), "[1m]"))
	if IsGroupRef(id) {
		// the groups' entries first: Catalog copies every entry, and was
		// built for each group an agent's settings name (the window's drift
		// poll, every 15s)
		entries := providerEntries()
		if e, ok := entryIn(groupEntries(entries), id); ok {
			return *e.Provider, e.Model, true
		}
		if e, ok := entryIn(Catalog(), id); ok {
			return *e.Provider, e.Model, true
		}
		// a group renamed from it (Former) answers for it, as routing does
		if gid, ok := GroupIDOf(id); ok {
			if g, ok := routedGroupOf(sharedGroupsIn(entries), gid); ok && g.ID != gid {
				if e, ok := entryIn(groupEntries(entries), "group/"+g.ID); ok {
					return *e.Provider, e.Model, true
				}
			}
		}
		return Provider{}, "", false
	}
	return resolveIn(providerEntries(), id)
}

func resolveIn(entries []Entry, id string) (Provider, string, bool) {
	// by index: an Entry, its Provider in it, is large, and a copy of each
	// one looked past was most of a group's members resolved (membersIn)
	for i := range entries {
		if e := &entries[i]; e.ID == id {
			return *e.Provider, e.Model, true
		}
	}
	if pid, model, ok := strings.Cut(id, "/"); ok {
		// the providers a request holds (All): an agent's value of a
		// model not listed lands here
		if p := lookIn(sharedAll(), pid); p != nil && p.On() && !p.ServesNone() {
			return p.clone(), model, true
		}
	}
	for i := range entries {
		if e := &entries[i]; e.Model == id {
			return *e.Provider, e.Model, true // the first that serves it
		}
	}
	// not exposed, but some provider lists it
	var found []Provider
	// kept across requests, as the catalog is: built again for each model
	// value no entry has (an agent's bare "gpt-5.5"), every provider's whole
	// list was 835 MB of a live magpie's 33 GB in 53 minutes
	for _, p := range heldOf("listed", func() map[string][]*Provider { return keptOf("listed", listedBy) })[id] {
		if !p.DecidesModel(id) {
			found = append(found, *p)
		}
	}
	if len(found) == 1 {
		return found[0], id, true
	}
	return Provider{}, "", false
}

// listedBy is the providers on that list each model, in order, each once.
// Every model of a provider points at one copy of it: a copy a model was
// 1.3KB each, 32 MB allocated at start-up on a real account's lists.
func listedBy() map[string][]*Provider {
	out := map[string][]*Provider{}
	for _, p := range All() {
		if !p.On() || p.ServesNone() {
			continue
		}
		pp := &p
		for _, m := range p.Available() {
			if l := out[m.ID]; len(l) == 0 || l[len(l)-1] != pp { // each once
				out[m.ID] = append(l, pp)
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
// A window the vendor lists under catalog.LeastWindow (512, H20 on Discord)
// is no window: models.dev's answers, else the one the id's name says
// ("qwen-3.8-27b-128k", catalog.NameWindow), else none, and the agent's
// default.
func ListedWindow(m catalog.Model) int {
	if n := catalog.Window(m.Context); n > 0 {
		return n
	}
	if n := catalog.ContextOf(m.ID); n > 0 {
		return n
	}
	// last, what the name says ("qwen-3.8-27b-128k")
	return catalog.NameWindow(m.ID)
}

// WindowOf is the window one of the provider's models takes, as agents are
// told it and requests are routed on it: the one the user set for the
// model over ListedWindow. The one set for all of the provider's models
// ("*") is no larger than a model's own, where the provider says it (the
// window its list or its models.dev catalog gives, or the most it may be
// asked for, MaxContext): set to 512K to spend less, it told opencode-go's
// cat-jam-free, which takes 262K, 512K (#2112). The window models.dev
// gives the id under other vendors (catalog.ContextOf) is not the
// provider's word but magpie's guess, and never caps what the user set: a
// custom OpenAI-compatible relay set to 1M told its claude-sonnet-4-5
// 200K (MisakaNatsuxun on Discord, 10-23). One set for the model itself
// is the user's word on it, larger or not. The catalog's entries and the
// Providers page's model lists both read it, so the page shows the window
// agents are told.
func (p Provider) WindowOf(m catalog.Model) int { return p.windowIn(m, settings.Shared()) }

// WindowIn is WindowOf with the settings s already read.
func (p Provider) WindowIn(m catalog.Model, s settings.Settings) int { return p.windowIn(m, s) }

// windowIn is WindowOf under settings s: a window the user typed, for the
// model or for all the provider's (which is no larger than the model's own
// where it says), is theirs as it is, or held to the longest context too
// when the user chose that (settings.ContextCapAll); any other is held to
// the longest context the user set for every model (settings.ContextCap).
func (p Provider) windowIn(m catalog.Model, s settings.Settings) int {
	if n := p.Contexts[m.ID]; n > 0 {
		return s.CappedOwn(n)
	}
	if n := p.Contexts["*"]; n > 0 {
		if most := max(catalog.Window(m.Context), m.MaxContext); most > 0 && most < n {
			return s.CappedOwn(most)
		}
		return s.CappedOwn(n)
	}
	return s.Capped(ListedWindow(m))
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

// OutputWithin is a reply limit as it is published beside a window: kept
// within it. models.dev lists some models' output above the window their
// vendor's own list gives (Grok's grok-4.7: 500000 against the 256000 its
// backend says, #1438; deepseek-chat's 384000 against 128000, #338), and a
// client that reads both sizes its replies past what the window holds. An
// unknown window or output is left as it is. Every place that hands out the
// pair uses it — the agents' files, the gateway's model lists, the GUI and
// the CLI — while a request is still lowered only to the model's own limit
// (Entry.Output, withMaxOutput).
func OutputWithin(window, output int) int {
	if window > 0 && output > window {
		return window
	}
	return output
}

// PublishedOutput is e's reply limit as it is handed out beside its window
// (OutputWithin).
func (e Entry) PublishedOutput() int { return OutputWithin(e.Context, e.Output) }

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
	for k, n := range settings.Shared().ModelOutputs {
		if model, ok := strings.CutPrefix(k, providerID+"/"); ok && model != "" && n > 0 {
			out[model] = n
		}
	}
	return out
}

// SetModelOutputs makes a provider's reply limits outs, by model id, "*"
// for all of them, as its editor's Max output says (ARNO on Discord: a
// model's maxTokens was wrong, and only the context window could be set in
// the editor): those it leaves out are taken away, and every one set or
// changed has to be a model the provider serves, as for SetModelOutput.
// One the user set for a model the provider has since stopped listing,
// sent back as it is, is kept and not refused: the editor is given it
// (OutputsOf) and sends it again on every Save, so refusing it stopped
// each Save of that provider, whatever else changed, and the model may be
// listed again at the next refresh (#3305, LoKi). Taking it out of Max
// output clears it. Nothing is saved, nor the agents told, when nothing
// changed.
func SetModelOutputs(providerID string, outs map[string]int) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	s := settings.Load()
	for model, n := range outs {
		if n < 0 {
			return errorf("an output limit is a number of tokens, not %d", n)
		}
		if err := settings.CheckModelKey("an output limit", p.ID+"/"+model); err != nil {
			return err
		}
		if model != "*" && n > 0 && s.ModelOutputs[p.ID+"/"+model] != n && !p.serves(model) {
			return errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
	}
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
	if IsGroupRef(r) {
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
