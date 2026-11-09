package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Method is a way a plugin signs in: "oauth" (a browser, then a code
// pasted back or not) or "api" (a key).
type Method struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	// Placeholder is the hint an "api" method gives in its key's field
	// (magpie's own field: OpenCode's says "API key")
	Placeholder string `json:"placeholder,omitempty"`
	// Keyless skips personal-key entry; authorize supplies the credential.
	Keyless bool `json:"keyless,omitempty"`
}

// KeyTitle is what an "api" method's key is asked as: its label, as
// OpenCode's dialog titles it, unless that only says "API key" (or
// nothing), when it is name's API key.
func (m Method) KeyTitle(name string) string {
	l := strings.TrimSpace(m.Label)
	if l == "" || strings.EqualFold(l, "API key") {
		return name + " API key"
	}
	return l
}

// Cost is a model's price as a plugin gives it: OpenCode's cost, in USD
// per 1M tokens, 0 where it gives none.
type Cost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

// Model is a model a plugin's provider serves, as OpenCode lists it.
type Model struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	NPM       string   `json:"npm"` // the AI SDK package it is spoken to with
	URL       string   `json:"url"`
	APIID     string   `json:"apiId"`
	Context   int      `json:"context"`
	Input     int      `json:"input"`
	Output    int      `json:"output"`
	Reasoning bool     `json:"reasoning"`
	Image     bool     `json:"image"`
	Released  string   `json:"released"`
	Variants  []string `json:"variants"`
	// Free is set by the plugin on a model the plan serves at no cost to
	// its allowance (WorkBuddy's "credits": "x0.00")
	Free bool `json:"free"`
	// Rate is set by the plugin on a model whose price its vendor lists:
	// the credits a request costs, as a multiple (Qoder's price_factor
	// 0.5, WorkBuddy's "credits": "x0.03"), and RateWas the price before a
	// discount running now. The plugin gives either as a number or as the
	// vendor writes it ("x0.03"), on the model it lists (m.rate,
	// m.rateWas); 0 is none.
	Rate    float64 `json:"rate"`
	RateWas float64 `json:"rateWas"`
	// Cost is the model's price as the plugin gives it (Devin's own list,
	// 0.1.18)
	Cost *Cost `json:"cost"`
	// ImageSaid is whether the plugin (or models.dev) said if it takes
	// images: Image false without it is not known
	ImageSaid bool `json:"imageSaid"`
	// Inputs and Outputs are the kinds capabilities.input and .output say
	// the model takes and gives (text, image, audio, video, pdf), in
	// OpenCode's order: not image alone (#1595)
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
	// OutputsSaid is whether capabilities.output said yes or no to any
	// kind: an embedding's {"text": false} gives no Outputs and is still
	// said (#3279), where a model said nothing of gives none and isn't
	OutputsSaid bool `json:"outputsSaid,omitempty"`
	// Temperature is whether the model takes temperature and top_p, where
	// something said so: the config model's temperature (Factory's OpenAI
	// route, plugins#154), models.dev's own, or the models hook's. Unsaid
	// is nil, which keeps what the client sent.
	Temperature *bool `json:"temperature,omitempty"`
	// InputsSaid is whether capabilities.input is someone's word on what
	// the model takes (models.dev, the config, the plugin's own models
	// hook): the host's default for a config model said nothing of, input
	// {"text": true} alone, is nobody's word (#1595, #4436)
	InputsSaid bool `json:"inputsSaid,omitempty"`
	// Fast is set by the plugin on a model it runs in its vendor's fast
	// mode when the request says service_tier "priority" (m.fast: Cursor's
	// plugin from 0.2.2, for a model Cursor has a fast variant of)
	Fast bool `json:"fast"`
	// ServiceTiers are the service tiers the plugin says its vendor serves
	// the model at (m.serviceTiers: "priority", "ultrafast"): Codex is
	// offered them, and a request asking for one reaches the plugin's
	// fetch with its service_tier (#2514)
	ServiceTiers []string `json:"serviceTiers,omitempty"`
	// Decides is set by the plugin on a model that answers System One's
	// questions rather than a conversation (m.decides: a decision model
	// not named jev-…, Clef or a vendor's own). It counts only where the
	// provider says it serves a decision API (Provider.Decide).
	Decides bool `json:"decides,omitempty"`
	// Searches is set by the plugin on a model that searches the web by
	// itself when asked in plain words (m.searches: Trae's search model),
	// which magpie may then search with for other models.
	Searches bool `json:"searches,omitempty"`
}

// Provider is a provider a plugin signs in to.
type Provider struct {
	ID      string   `json:"id"`   // OpenCode's: google, github-copilot
	Spec    string   `json:"spec"` // the plugin
	Name    string   `json:"name"`
	NPM     string   `json:"npm"`
	API     string   `json:"api"`
	Methods []Method `json:"methods"`
	// Icon is the picture the plugin gives the provider, as it said it:
	// an https URL or a data:image URI (internal/provider keeps it)
	Icon string `json:"icon,omitempty"`
	// Usage says the plugin tells each account's allowance (auth.usage)
	Usage bool `json:"usage"`
	// Checkin says the plugin presses its vendor's daily check-in for each
	// account (auth.checkin)
	Checkin bool `json:"checkin,omitempty"`
	// Renews says the plugin renews its accounts' sign-ins through magpie
	// (auth.refresh) and lets the gateway's magpie do it in the background
	// as each comes due (auth.keepAlive not false); RenewLead is how long
	// before a token expires it is due, in milliseconds (auth.refreshLead,
	// 5 minutes when not said), as the host reckons it (RenewAt).
	Renews    bool  `json:"renews,omitempty"`
	RenewLead int64 `json:"renewLead,omitempty"`
	// Rotating is the auth hook declaring that the vendor spends a refresh
	// token once (auth.rotating): renewed, the old one is refused
	// (invalid_grant). Two computers holding a copy would race to renew it
	// and the loser is signed out for good, so its sign-ins are never
	// carried by a backup or a sync (provider.CollectSignIns, #5583).
	Rotating bool `json:"rotating,omitempty"`
	// Options are the switches the plugin declares for the provider
	// (auth.options, #2038), as the host checked them
	Options []Option `json:"options,omitempty"`
	// Decide says the plugin serves a decision API (TypeSafe's System
	// One) for this provider: its config hook set decide on it. magpie
	// posts the questions to the provider's own base, at /systemone,
	// through the plugin's fetch (#1514).
	Decide bool `json:"decide,omitempty"`
	// Searches says the provider's upstream answers a web search tool
	// offered on its Anthropic or Responses API by itself, as a custom
	// provider's Searches does (#825): its config hook set searches on it
	// (#2027), for a plugin in front of Claude's or Codex's API.
	Searches  bool    `json:"searches,omitempty"`
	SignedIn  bool    `json:"signedIn"`
	AuthType  string  `json:"authType"`
	AccountID string  `json:"accountId"`
	Models    []Model `json:"models"`
	// FellBack says the plugin's models hook couldn't fetch its vendor's
	// list and gave the default one back.
	FellBack bool `json:"fellBack,omitempty"`
	// ListError is why it fell back, as the models hook threw it or its
	// last fetch failed: the editor says so over the short list.
	ListError string `json:"listError,omitempty"`
	// Accounts are the accounts signed in to it, the one kept under its
	// own id first; SignedIn, AuthType and AccountID are that one's.
	Accounts []Account `json:"accounts"`
	// Single is the auth hook declaring that sign-in replaces one identity.
	Single bool `json:"single,omitempty"`
	// MaxConcurrency is how many requests the plugin says each of its
	// accounts takes at once (magpie's own field, which OpenCode ignores:
	// the auth hook's maxConcurrency, else package.json's
	// magpie.maxConcurrency); 0 for none said. The user's setting on the
	// provider goes over it.
	MaxConcurrency int `json:"maxConcurrency,omitempty"`
	// MaxAccounts is how many of its accounts the plugin says may take
	// requests at the same time (the auth hook's maxAccounts, else
	// package.json's magpie.maxAccounts: a vendor that blocks an address
	// asking for more accounts at once); 0 for none said. The user's
	// setting on the provider goes over it.
	MaxAccounts int `json:"maxAccounts,omitempty"`
}

// Option is a switch a plugin declares for its provider (auth.options,
// #2038): magpie shows it, keeps the user's value in settings'
// PluginOptions and hands it to the plugin (input.magpie.option). A change
// has the providers listed again (SetProviderOptions); nothing else runs.
type Option struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
	// Confirm is asked before the switch is turned on, when the plugin
	// says turning it on does something the user should agree to
	Confirm string `json:"confirm,omitempty"`
	// Labels, Descriptions and Confirms are the same words in the window's
	// other languages, by its language ("zh", "zh-TW", "ja", "de"), as a
	// page's Titles are (plugins#590); the plain ones are the fallback
	Labels       map[string]string `json:"labels,omitempty"`
	Descriptions map[string]string `json:"descriptions,omitempty"`
	Confirms     map[string]string `json:"confirms,omitempty"`
	// FiltersModels says the plugin's model list leaves models out while
	// the switch is on (WorkBuddy's "Free models only"): the editor says
	// so by the models
	FiltersModels bool `json:"filtersModels,omitempty"`
	// Placement is where the editor draws the switch: "top" under the
	// accounts, above Routing, for a switch about the whole plugin
	// (#5269); "" under the models
	Placement string `json:"placement,omitempty"`
}

// OptionOn is option o of a provider as it stands with the user's values
// in chosen (settings' PluginOptions): theirs, else the plugin's default.
func OptionOn(chosen map[string]map[string]bool, provider string, o Option) bool {
	if v, ok := chosen[provider][o.Key]; ok {
		return v
	}
	return o.Default
}

// SetProviderOptions hands the user's option values (settings' PluginOptions) to
// the host running now; a host started later reads them at its start. The
// providers kept are asked again after: a models hook may list by an
// option (WorkBuddy's "Free models only", plugins#430), and the list
// follows the switch without a restart.
func SetProviderOptions(ctx context.Context) error {
	if !Running() {
		forgetProviders()
		return nil
	}
	err := Call(ctx, "setProviderOptions", map[string]any{"options": settings.Load().PluginOptions}, nil)
	forgetProviders()
	return err
}

// Account is one account a provider is signed in to: Key is where
// plugin-auth.json keeps it (the provider's id, else id#slot).
type Account struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	AccountID string `json:"accountId"`
	// Hint tells an account with no id from another: the end of its key.
	Hint string `json:"hint,omitempty"`
	// Models are the ids of the provider's models this account has, when
	// the provider has more than one account; none, it has them all.
	Models    []string `json:"models,omitempty"`
	FellBack  bool     `json:"fellBack,omitempty"`
	ListError string   `json:"listError,omitempty"`
}

var (
	provMu      sync.Mutex
	provWriteMu sync.Mutex
	provCache   []Provider
	// Failed reads keep the old list. Cached tries once per invalidation,
	// with bounded retries, then waits for a sign-in or plugin change.
	provGood   bool
	provBusy   bool
	provTried  bool
	provCancel context.CancelFunc
	// provEpoch counts the invalidations (forgetProviders), so a refresh
	// that began before one doesn't put what it read over the newer state.
	provEpoch uint64
	// provSettling is set while Settle runs: no refresh starts, nor goes
	// on to a newer invalidation, to start a host in a folder going away.
	provSettling bool
)

func providersPath() string { return filepath.Join(settings.Dir(), "plugin-providers.json") }

// ListedAt is when the plugins last listed their providers and models, as
// a built-in's list is dated by when it was fetched.
func ListedAt() (time.Time, bool) {
	st, err := os.Stat(providersPath())
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

func forgetProviders() {
	provMu.Lock()
	provGood = false
	provTried = false
	provEpoch++
	if provCancel != nil {
		provCancel()
	}
	provMu.Unlock()
}

// Providers asks the plugins for their providers, starting the host if
// need be, and keeps the answer for Cached.
func Providers(ctx context.Context) ([]Provider, error) {
	var ps []Provider
	if err := Call(ctx, "providers", listingArgs(), &ps); err != nil {
		return nil, err
	}
	ps = commitProviders(ps, nil)
	return ps, nil
}

// commitProviders keeps ps for Cached and on disk, with a list a plugin
// fell back to replaced by the one it told last, and gives what was kept.
// epoch, when given, is the invalidation the read began under: a newer
// one that went by meanwhile stands, and ps is then only given back.
func commitProviders(ps []Provider, epoch *uint64) []Provider {
	// Serialize publishers without blocking Cached on Windows rename retries.
	provWriteMu.Lock()
	defer provWriteMu.Unlock()
	provMu.Lock()
	if epoch != nil && provEpoch != *epoch {
		provMu.Unlock()
		return ps
	}
	ps = keepListed(ps, provCache)
	ps = keepUnloaded(ps, provCache)
	was := provCache
	provCache, provGood, provTried = ps, true, true
	provMu.Unlock()
	b, err := json.Marshal(ps)
	if err == nil {
		if was == nil {
			// what an earlier run kept: a start that lists the same is no
			// change
			if kept, err := steady.ReadFile(providersPath()); err == nil {
				_ = json.Unmarshal(kept, &was)
			}
		}
		_ = writeWhole(providersPath(), b)
	}
	if err != nil || !sameList(was, b) {
		listed()
	}
	return ps
}

// sameList reports whether the providers was and the list b marshals to
// say the same.
func sameList(was []Provider, b []byte) bool {
	if was == nil {
		return false
	}
	w, err := json.Marshal(was)
	return err == nil && bytes.Equal(w, b)
}

// refreshDeadline bounds one providers RPC after host initialization: a plugin host
// that stays alive but never answers the providers call must not leave
// the refresh (and Refreshed, and Settle) waiting for ever.
var refreshDeadline = 30 * time.Second

// refreshTries is how many times one background refresh asks before it
// gives up until the next invalidation; refreshBackoff is how long it waits
// before the second ask, doubled for each one after.
var (
	refreshTries   = 3
	refreshBackoff = 250 * time.Millisecond
)

// refreshProviders is one bounded ask of the plugins for their providers,
// kept for Cached when it was answered while the invalidation it began
// under still stood. Tests replace it to stall or fail an ask without Bun.
var refreshProviders = func(ctx context.Context, epoch uint64) error {
	var ps []Provider
	if err := callWithTimeout(ctx, "providers", listingArgs(), &ps, refreshDeadline); err != nil {
		return err
	}
	commitProviders(ps, &epoch)
	return nil
}

// refreshProvidersWithRetry asks the plugins for their providers with
// a deadline, retrying a failure with backoff a bounded number of times.
// A failure keeps the providers already kept, as it keeps the ones on
// disk; a newer invalidation during the refresh is not overwritten by it.
func refreshProvidersWithRetry(ctx context.Context, epoch uint64) {
	wait := refreshBackoff
	for try := 0; try < refreshTries; try++ {
		if try > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			wait *= 2
		}
		err := refreshProviders(ctx, epoch)
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// a failure keeps what is kept already, and is retried; one that
		// began before a newer invalidation stops here rather than going
		// on with answers for a state that is gone
		provMu.Lock()
		stale := provEpoch != epoch
		provMu.Unlock()
		if stale {
			return
		}
	}
}

// Keep the same admission across intervening changes, so Refreshed waits for
// the latest state too. Invalidation cancels only the old listing, not startup.
func refreshProvidersInBackground(ctx context.Context, epoch uint64) {
	defer refreshing.Done()
	for {
		if Running() || HasBun() {
			refreshProvidersWithRetry(ctx, epoch)
		}
		provMu.Lock()
		provCancel()
		if provEpoch != epoch && !provGood && !provTried && !provSettling {
			epoch = provEpoch
			ctx, provCancel = context.WithCancel(context.Background())
			provTried = true
			provMu.Unlock()
			continue
		}
		provBusy, provCancel = false, nil
		provMu.Unlock()
		return
	}
}

// ProxyFor is the proxy choice (netproxy.With's) of the provider's
// account at key, or of the provider for key "": set by the providers
// magpie keeps, which know them.
var ProxyFor func(provider, key string) string

// ListingOff is the plugin providers (by the plugin's own id) switched off
// in magpie: set by the providers magpie keeps, which know them. A
// listing asks none of them their models (ChangQiu on Discord): Off means no request
// goes to the provider, and a models hook asks its vendor, renewing the
// sign-in first.
var ListingOff func() []string

// listingArgs is what a providers call is given: the proxies each list is
// asked through, and the providers not to ask (ListingOff), which keep the
// list they told last.
func listingArgs() map[string]any {
	args := map[string]any{"proxies": listingProxies()}
	if ListingOff != nil {
		if off := ListingOff(); len(off) > 0 {
			args["off"] = off
		}
	}
	return args
}

// listingProxies are the proxies each provider's model list is asked
// through, by provider and account key ("" the provider's own): a
// built-in's list is fetched through the account's proxy, so a plugin's
// is too.
func listingProxies() map[string]map[string]string {
	out := map[string]map[string]string{}
	if ProxyFor == nil {
		return out
	}
	for k := range authRaw() {
		id := ProviderOf(k)
		if out[id] == nil {
			out[id] = map[string]string{"": forHost(ProxyFor(id, ""))}
		}
		out[id][k] = forHost(ProxyFor(id, k))
	}
	return out
}

// keepListed is ps with a list a plugin fell back to replaced by the one
// it told last: a built-in whose fetch fails keeps the list it fetched
// last too, so a vendor's hiccup never shrinks the models to the plugin's
// short defaults.
func keepListed(ps, last []Provider) []Provider {
	if last == nil {
		if b, err := steady.ReadFile(providersPath()); err == nil {
			_ = json.Unmarshal(b, &last)
		}
	}
	was := map[string]Provider{}
	for _, p := range last {
		was[p.Spec+"\x00"+p.ID] = p
	}
	for i, p := range ps {
		l, ok := was[p.Spec+"\x00"+p.ID]
		if !ok {
			continue
		}
		if p.FellBack && !l.FellBack && len(l.Models) > 0 {
			ps[i].Models, ps[i].FellBack, ps[i].ListError = l.Models, false, ""
			// the first account's list is the provider's: it is kept with it
			if len(ps[i].Accounts) > 0 && ps[i].Accounts[0].Models == nil {
				ps[i].Accounts[0].FellBack, ps[i].Accounts[0].ListError = false, ""
			}
		}
		for j, a := range p.Accounts {
			if !a.FellBack {
				continue
			}
			for _, b := range l.Accounts {
				if b.Key == a.Key && !b.FellBack && len(b.Models) > 0 {
					ps[i].Accounts[j].Models, ps[i].Accounts[j].FellBack, ps[i].Accounts[j].ListError = b.Models, false, ""
				}
			}
		}
	}
	return ps
}

// keepUnloaded is ps with the providers last known of an installed
// plugin that told none this time: one that failed to load (a broken
// update, its files gone, Bun refusing it) keeps its providers, their
// accounts and what moved onto them in sight, its requests failing with
// why, rather than going as if it were removed.
func keepUnloaded(ps, last []Provider) []Provider {
	if last == nil {
		if b, err := steady.ReadFile(providersPath()); err == nil {
			_ = json.Unmarshal(b, &last)
		}
	}
	told, ids := map[string]bool{}, map[string]bool{}
	for _, p := range ps {
		told[p.Spec], ids[p.ID] = true, true
	}
	installed := map[string]bool{}
	for _, e := range list().Plugins {
		installed[e.Spec] = true
	}
	for _, p := range last {
		// a provider another plugin serves now (two plugins sign in to
		// it, and the other was installed or picked for it) isn't kept as
		// this one's too: it would be listed twice under one id
		if installed[p.Spec] && !told[p.Spec] && !ids[p.ID] {
			ps = append(ps, p)
		}
	}
	return ps
}

// UseCached is for tests: Cached answers with ps, as though the plugins
// had just been asked, without Bun; nil forgets them.
func UseCached(ps []Provider) {
	provMu.Lock()
	provCache, provGood, provTried = ps, ps != nil, ps != nil
	// a refresh in flight began under what this replaces: what it read
	// must not go over it
	provEpoch++
	if provCancel != nil {
		provCancel()
	}
	provMu.Unlock()
	// as asked with the plugins as they are now
	listSeen.Lock()
	listSeen.stamp, listSeen.set = listStamp(), ps != nil
	listSeen.Unlock()
}

// Listed is every provider the plugins last listed, an off plugin's among
// them, as Cached keeps them, with no sign-in read: false when no listing
// is known (none kept, plugin-providers.json missing or unreadable), which
// is never "no providers".
func Listed() ([]Provider, bool) {
	provMu.Lock()
	ps := provCache
	provMu.Unlock()
	if ps != nil {
		return slices.Clone(ps), true
	}
	b, err := steady.ReadFile(providersPath())
	if err != nil || json.Unmarshal(b, &ps) != nil {
		return nil, false
	}
	return ps, true
}

// refreshing is Cached's refreshes in the background.
var refreshing sync.WaitGroup

// Refreshed waits for Cached's refreshes in the background to end, the
// host left running: for tests that watch the providers' list on disk.
func Refreshed() { refreshing.Wait() }

// Told waits for the hooks told of a change or a sign-in (OnChange,
// OnSignIn) to end: for tests, so that a change one test made is not told
// in the next, nor written into its folder as it goes.
func Told() {
	telling.Lock()
	for telling.n > 0 {
		telling.Wait()
	}
	telling.Unlock()
}

// Answered is whether what Cached gives now is the plugins' own answer,
// rather than what was kept from before they were last asked. A page that
// would otherwise read "signs in to nothing" out of an empty list can say
// so only when this holds: an empty list before the answer arrives means
// "not asked yet", not "none" (#1112).
func Answered() bool {
	provMu.Lock()
	defer provMu.Unlock()
	return provGood
}

// Settle waits for Cached's refreshes to end, stops the host, waits for
// the hooks told of a change (Told) and forgets the plugins (UseCached
// nil): for tests, whose folders the host runs in go when they end, so
// the next test starts with nothing of this one's running or seen. Each
// ask is bounded and the retries are few, so this waits a bounded time
// even for a host that never answers.
//
// No refresh starts meanwhile: one a hook started after the last wait
// went on once Settle had returned, and its host started in the test's
// folder as it went, or in the next test's, while that one signed in
// (#2449).
func Settle() {
	provMu.Lock()
	provSettling = true
	provMu.Unlock()
	defer func() {
		provMu.Lock()
		provSettling = false
		provMu.Unlock()
	}()
	refreshing.Wait()
	Restart()
	// what the host said of a sign-in is written before the folder goes
	// (#1935); a hook that read the providers to know the account's
	// started a host again, which goes too
	Told()
	refreshing.Wait()
	Restart()
	// and no host goes on finishing its calls in a folder going away
	hostMu.Lock()
	hs := make([]*host, 0, len(retiring))
	for h := range retiring {
		hs = append(hs, h)
	}
	hostMu.Unlock()
	for _, h := range hs {
		h.stop()
	}
	// nor is the restart, or a sign-in a host saved, told in the next test
	Told()
	// and the plugins go with the folder: the next test's, in a folder of
	// its own, are not taken for the plugins changed (checkList), nor
	// answered with these
	UseCached(nil)
	cancelCatalogSyncs()
}

// Cached is the plugins' providers as last asked, without starting the
// host: what is known of them when magpie has only just started. A
// provider's sign-in is read afresh from plugin-auth.json.
func Cached() []Provider {
	checkList()
	l := list() // one read of plugins.json for the whole call
	provMu.Lock()
	ps := provCache
	good := provGood
	provMu.Unlock()
	if ps == nil {
		ps = savedProviders()
	}
	if len(l.Plugins) == 0 {
		return nil
	}
	auth := readAuth()
	out := make([]Provider, 0, len(ps))
	on := map[string]bool{}
	for _, e := range l.Plugins {
		if !e.Off {
			on[e.Spec] = true
		}
	}
	for _, p := range ps {
		if !on[p.Spec] {
			continue
		}
		was := p.Accounts
		p.Accounts = accountsOf(auth, p.ID)
		for i, a := range p.Accounts {
			for _, w := range was {
				if w.Key == a.Key {
					p.Accounts[i].Models, p.Accounts[i].FellBack, p.Accounts[i].ListError = w.Models, w.FellBack, w.ListError
				}
			}
		}
		p.SignedIn = len(p.Accounts) > 0
		p.AuthType = ""
		if p.SignedIn {
			p.AuthType = p.Accounts[0].Type
			p.AccountID = p.Accounts[0].AccountID
		}
		out = append(out, p)
	}
	if !good && len(l.Plugins) > 0 {
		// refreshed in the background: a sign-in or the plugins changed.
		// Whether one is already in flight is read and set under one lock,
		// so two callers asking at once start one refresh, not two.
		provMu.Lock()
		start := !provBusy && !provGood && !provTried && !provSettling
		var ctx context.Context
		if start {
			ctx, provCancel = context.WithCancel(context.Background())
			provBusy, provTried = true, true
			refreshing.Add(1)
		}
		epoch := provEpoch
		provMu.Unlock()
		if start {
			go refreshProvidersInBackground(ctx, epoch)
		}
	}
	return out
}

// savedProviders is plugin-providers.json, what the plugins last listed,
// parsed once for each version of the file: while the host hasn't
// listed them (just started, or it can't start, offline or a plugin that
// fails), every Cached read and decoded the whole file again, a few
// hundred KB for each look at a model of a plugin. What it gives is
// shared: the caller must not change it.
func savedProviders() []Provider {
	ps, _ := filememo.Read("plugin-providers", providersPath(), func(b []byte) ([]Provider, error) {
		var ps []Provider
		_ = json.Unmarshal(b, &ps) // what decodes is kept, as it always was
		return ps, nil
	})
	return ps
}

type storedAuth struct {
	Type string `json:"type"`
	// Expires is when an OAuth sign-in's token ends, in epoch
	// milliseconds, as the host reads it (a number, or a string of one)
	Expires   json.RawMessage `json:"expires"`
	AccountID string          `json:"accountId"`
	Email     string          `json:"email"`
	Key       string          `json:"key"`
	Metadata  struct {
		Email string `json:"email"`
	} `json:"metadata"`
}

// RenewAt is when the sign-in kept under key, an account of p, comes due
// to be renewed in the background, as host.js's renewAt reckons it: an
// OAuth sign-in's expires less p.RenewLead. False when it never does: p
// doesn't renew in the background, the sign-in isn't OAuth, has no
// expiry or isn't kept.
func RenewAt(p Provider, key string) (time.Time, bool) {
	if !p.Renews {
		return time.Time{}, false
	}
	a, ok := readAuth()[key]
	if !ok || a.Type != "oauth" {
		return time.Time{}, false
	}
	exp, ok := expiresOf(a.Expires)
	if !ok {
		return time.Time{}, false
	}
	return time.UnixMilli(max(1, exp-p.RenewLead)), true
}

// expiresOf is a sign-in's expires as JavaScript's Number reads it, when
// it is a time after the epoch.
func expiresOf(raw json.RawMessage) (int64, bool) {
	v := strings.TrimSpace(string(raw))
	if s, err := strconv.Unquote(v); err == nil {
		v = strings.TrimSpace(s)
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > math.MaxInt64/2 {
		return 0, false
	}
	return int64(f), true
}

// KeptSignedIn is what came of KeepSignedIn: Due false, the sign-in
// wasn't due (renewed meanwhile); Renewed, it is a new one; else Err is
// why it couldn't be, Gone whether the vendor turned it away for good
// (the host marks the account, as OnSignIn tells), and Waiting that a
// renewal failed lately and isn't tried again yet.
type KeptSignedIn struct {
	Due     bool   `json:"due"`
	Renewed bool   `json:"renewed"`
	Waiting bool   `json:"waiting"`
	Gone    bool   `json:"gone"`
	Err     string `json:"error"`
}

// KeepSignedIn renews account's sign-in of provider if it is due, as a
// request on it would, under the same once-at-a-time renewal and lock,
// and waits for it.
func KeepSignedIn(ctx context.Context, provider, account string) (KeptSignedIn, error) {
	var k KeptSignedIn
	err := Call(ctx, "keepSignedIn", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &k)
	return k, err
}

// ProviderOf is the provider an account key is one of.
func ProviderOf(key string) string {
	id, _, _ := strings.Cut(key, "#")
	return id
}

// accountsOf are provider's accounts in auth, as the host lists them.
func accountsOf(auth map[string]storedAuth, provider string) []Account {
	var out []Account
	for k, a := range auth {
		if ProviderOf(k) != provider {
			continue
		}
		who := firstNonEmpty(a.AccountID, a.Metadata.Email, a.Email)
		acct := Account{Key: k, Type: a.Type, AccountID: who}
		if a.Type == "api" && len(a.Key) >= 12 {
			acct.Hint = a.Key[len(a.Key)-4:]
		}
		out = append(out, acct)
	}
	slices.SortFunc(out, func(a, b Account) int {
		switch {
		case a.Key == provider:
			return -1
		case b.Key == provider:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// readAuth is plugin-auth.json's sign-ins as storedAuth, by key. What it
// returns is shared: the caller must not change it.
func readAuth() map[string]storedAuth {
	m, _ := filememo.Read("plugin-auth:stored", AuthPath(), func(b []byte) (map[string]storedAuth, error) {
		raw, err := parseAuth(b)
		out := make(map[string]storedAuth, len(raw))
		for k, v := range raw {
			var a storedAuth
			_ = json.Unmarshal(v, &a)
			out[k] = a
		}
		return out, err
	})
	return m
}

// authRaw is plugin-auth.json's sign-ins by key, each as it is written.
// What it returns is shared: the caller must not change it.
func authRaw() map[string]json.RawMessage {
	m, _ := filememo.Read("plugin-auth", AuthPath(), parseAuth)
	return m
}

// authParses counts plugin-auth.json's parses, for tests.
var authParses atomic.Int64

// parseAuth is plugin-auth.json's contents b, or its last good generation
// when b doesn't read (#1505). It is parsed once for each version of the
// file (filememo): the providers' every build read it whole, and parsed
// it twice over, for each plugin signed in to, on every request the
// gateway served (#2502).
func parseAuth(b []byte) (map[string]json.RawMessage, error) {
	authParses.Add(1)
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		bak, err := lastgood.Fallback(AuthPath(), b, validAuth)
		if err != nil {
			return nil, err
		}
		m = nil
		_ = json.Unmarshal(bak, &m)
	}
	return m, nil
}

// validAuth is plugin-auth.json's contents: one object of sign-ins.
func validAuth(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil
}

// readAuthFile is plugin-auth.json, or its last good generation when a
// crash left it all zero (#1505): sign-ins that don't read are unknown,
// never none.
func readAuthFile() ([]byte, error) { return lastgood.Read(AuthPath(), validAuth) }

// SignedIn is whether a plugin sign-in is kept for provider.
func SignedIn(provider string) bool {
	return len(accountsOf(readAuth(), provider)) > 0
}

// Prompt is a question a sign-in method asks before it starts.
type Prompt struct {
	Type        string `json:"type"` // text, select, password (text typed unseen, #1982)
	Key         string `json:"key"`
	Message     string `json:"message"`
	Placeholder string `json:"placeholder,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
		Hint  string `json:"hint,omitempty"`
	} `json:"options,omitempty"`
}

// NextPrompt is the method's next question given the answers so far; nil
// when it has asked them all.
func NextPrompt(ctx context.Context, provider string, method int, inputs map[string]string) (*Prompt, error) {
	var r struct {
		Prompt *Prompt `json:"prompt"`
	}
	err := Call(ctx, "prompt", map[string]any{"provider": provider, "method": method, "inputs": inputs}, &r)
	return r.Prompt, err
}

// Validate is what the method says is wrong with value as the answer to
// key, "" when nothing is.
func Validate(ctx context.Context, provider string, method int, key, value string) (string, error) {
	var r struct {
		Error *string `json:"error"`
	}
	if err := Call(ctx, "validate", map[string]any{"provider": provider, "method": method, "key": key, "value": value}, &r); err != nil {
		return "", err
	}
	if r.Error == nil {
		return "", nil
	}
	return *r.Error, nil
}

// Authorization is an OAuth sign-in begun: the page to open, and whether
// the plugin waits for it itself ("auto") or needs the code the page
// shows pasted back ("code").
type Authorization struct {
	Session      string `json:"session"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
	Method       string `json:"method"`
	// Opened is a page the plugin (its vendor's CLI) opened already, not
	// to be opened again
	Opened bool `json:"opened"`
}

// NewAccount is the account a sign-in goes to when it is a new one: the
// provider's own id while nothing is kept there. A sign-in to an account
// already signed in replaces that one's instead.
const NewAccount = "new"

// Authorize begins an OAuth sign-in to account (a key, or NewAccount),
// through the proxy ctx names (netproxy.With): the plugin's requests for
// it, there and in Finish, go through that one (#1663).
// A plugin that can't start one says why ({ type: "failed", error }), and
// that is the error (plugins#658).
func Authorize(ctx context.Context, provider string, method int, inputs map[string]string, account string) (Authorization, error) {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Authorization
	}
	if err := Call(ctx, "authorize", map[string]any{"provider": provider, "method": method, "inputs": inputs, "account": account, "proxy": proxyOf(ctx)}, &r); err != nil {
		return Authorization{}, err
	}
	if !r.OK {
		return Authorization{}, failure(r.Error)
	}
	return r.Authorization, nil
}

// Saved is where a sign-in was kept: the provider (a plugin may sign in
// to another than asked) and the account's key.
type Saved struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
}

// ErrFailed is a sign-in the plugin says failed.
var ErrFailed = errors.New("the sign-in failed")

// failure is ErrFailed with why, where the plugin told it.
func failure(why string) error {
	if why == "" {
		return ErrFailed
	}
	return fmt.Errorf("%w: %s", ErrFailed, why)
}

// Finish waits for an OAuth sign-in to finish: an "auto" one on its own,
// a "code" one with the code pasted back, through the proxy Authorize was
// given. It gives where the sign-in was saved.
func Finish(ctx context.Context, session, code string) (Saved, error) {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Saved
	}
	if err := Call(ctx, "callback", map[string]any{"session": session, "code": code}, &r); err != nil {
		return Saved{}, err
	}
	if !r.OK {
		return Saved{}, failure(r.Error)
	}
	return r.Saved, nil
}

// APIKey signs in to account (a key, or NewAccount) with a key, as an
// "api" method does, through the proxy ctx names.
func APIKey(ctx context.Context, provider string, method int, inputs map[string]string, key, account string) (Saved, error) {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Saved
	}
	if err := Call(ctx, "apiKey", map[string]any{"provider": provider, "method": method, "inputs": inputs, "key": key, "account": account, "proxy": proxyOf(ctx)}, &r); err != nil {
		return Saved{}, err
	}
	if !r.OK {
		return Saved{}, failure(r.Error)
	}
	return r.Saved, nil
}

// SignOut forgets one account of the provider's, every one when account
// is "".
func SignOut(ctx context.Context, provider, account string) error {
	if Running() {
		return Call(ctx, "signOut", map[string]any{"provider": provider, "account": account}, nil)
	}
	unlock := lockAuth()
	defer unlock()
	var m map[string]json.RawMessage
	b, err := readAuthFile()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err // sign-ins that don't read are not written over
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k := range m {
		if k == account || account == "" && ProviderOf(k) == provider {
			delete(m, k)
		}
	}
	b, _ = json.MarshalIndent(m, "", "  ")
	if err := lastgood.Keep(AuthPath(), validAuth, 0o600); err != nil {
		return err
	}
	if err := writeWhole(AuthPath(), append(b, '\n')); err != nil {
		return err
	}
	changed()
	return nil
}

// Options is what the provider's auth loader gave: where its requests
// go, the key the AI SDK would send, and whether it carries them itself.
type Options struct {
	BaseURL string            `json:"baseURL"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers"`
	Fetch   bool              `json:"fetch"`
}

var (
	optMu    sync.Mutex
	optCache = map[string]Options{}
	optGen   int64
)

// LoaderOptions runs the provider's auth loader for account (once per
// sign-in and host) and gives what it returned; account "" is the
// provider's first.
func LoaderOptions(ctx context.Context, provider, account string) (Options, error) {
	ck := provider + "\x00" + account
	optMu.Lock()
	if optGen != generation.Load() {
		optCache, optGen = map[string]Options{}, generation.Load()
	}
	o, ok := optCache[ck]
	optMu.Unlock()
	if ok {
		return o, nil
	}
	if err := Call(ctx, "load", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &o); err != nil {
		return Options{}, err
	}
	optMu.Lock()
	if optGen == generation.Load() {
		optCache[ck] = o
	}
	optMu.Unlock()
	return o, nil
}

func init() {
	OnChange(func() {
		optMu.Lock()
		optCache = map[string]Options{}
		optMu.Unlock()
	})
}

// Import keeps auth (a plugin-auth.json entry) as one more of provider's
// accounts — or, the same account as one kept already, in its place — and
// gives the account's key. The plugin's host is started for it: it alone
// writes the file while it runs.
func Import(ctx context.Context, provider string, auth map[string]any) (string, error) {
	var r struct {
		Account string `json:"account"`
	}
	if err := Call(ctx, "import", map[string]any{"provider": provider, "auth": auth}, &r); err != nil {
		return "", err
	}
	return r.Account, nil
}

// Checked is what trying an account gave: the model ids the plugin lists
// for it, its usage read (nil when the plugin tells none), and why its
// models hook said the vendor refused the sign-in, if it did.
type Checked struct {
	Models  []string `json:"models"`
	Usage   *Usage   `json:"usage"`
	Refused string   `json:"refused"`
}

// Check tries one of provider's accounts as a request would — its auth
// loader, then its models as the plugin lists them for it — and reads its
// usage, which asks the vendor of the account itself. It fails when the
// plugin reached none of the places it asked (the vendor offline): a
// models hook falling back to a list it keeps proves nothing.
func Check(ctx context.Context, provider, account string) (Checked, error) {
	var r Checked
	if err := Call(ctx, "check", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &r); err != nil {
		return Checked{}, err
	}
	return r, nil
}

// Auths are provider's sign-ins as plugin-auth.json keeps them, by key.
func Auths(provider string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for k, v := range authRaw() {
		if ProviderOf(k) != provider {
			continue
		}
		// the caller's own: decoded afresh from what is shared
		var a map[string]any
		_ = json.Unmarshal(v, &a)
		out[k] = a
	}
	return out
}

// Take gives provider's accounts named by keys (every one when there are
// none) and signs them out in one step, so the plugin renews none of their
// tokens after it gave them.
func Take(ctx context.Context, provider string, keys []string) (map[string]map[string]any, error) {
	var r struct {
		Auths map[string]map[string]any `json:"auths"`
	}
	if err := Call(ctx, "take", map[string]any{"provider": provider, "accounts": keys}, &r); err != nil {
		return nil, err
	}
	return r.Auths, nil
}

// Restore puts auth back as provider's account key.
func Restore(ctx context.Context, provider, key string, auth map[string]any) error {
	return Call(ctx, "import", map[string]any{"provider": provider, "key": key, "auth": auth}, nil)
}

// Usage is how much of its allowance one account has used, as the
// plugin's auth.usage tells it (host.js has the shape).
type Usage struct {
	Plan    string        `json:"plan"`
	Until   string        `json:"until"` // RFC 3339, "" when not told
	Renew   string        `json:"renew"`
	Balance string        `json:"balance"`
	Error   string        `json:"error"`
	User    string        `json:"user"`
	SignIn  string        `json:"signIn"` // "expired", "kept", "renewed" or ""
	Windows []UsageWindow `json:"windows"`
	Resets  *UsageResets  `json:"resets"`
}

// UsageResets are the rate-limit resets an account may spend.
type UsageResets struct {
	Count    int    `json:"count"`
	Until    string `json:"until"`
	ByWindow bool   `json:"byWindow"`
	FiveHour int    `json:"fiveHour"`
	Weekly   int    `json:"weekly"`
	Team     bool   `json:"team"` // a team plan's, not the person's own
	Each     []struct {
		Until  string `json:"until"` // RFC 3339, "": it never runs out
		Window string `json:"window"`
	} `json:"each"`
}

type UsageWindow struct {
	Name      string   `json:"name"`
	Used      *float64 `json:"used"`     // percent; nil when the plugin didn't say
	ResetsAt  string   `json:"resetsAt"` // RFC 3339
	ResetSecs int64    `json:"resetSecs"`
	Display   string   `json:"display"`
	Amount    float64  `json:"amount"` // of Limit, in Unit: the window's own count, used
	Limit     float64  `json:"limit"`
	Unit      string   `json:"unit"`
	Span      float64  `json:"span"` // seconds
	Model     string   `json:"model"`
	Models    []string `json:"models"`
	NotModels []string `json:"notModels"`
	Aside     bool     `json:"aside"`
	// RunsOutAt (RFC 3339) is when the soonest credits left in the window
	// are lost, and RunsOut how many, when the vendor's credits expire
	// apart from the window's reset (WorkBuddy's packages)
	RunsOutAt string  `json:"runsOutAt"`
	RunsOut   float64 `json:"runsOut"`
	// Balance is how much is left in Unit, a prepaid wallet's reading,
	// with no share to tell (#3950); nil when the plugin didn't say.
	Balance *float64 `json:"balance"`
}

// Checkin is what came of an account's daily check-in, as the plugin's
// auth.checkin said: Outcome is one of claimed, done, ineligible,
// inactive, captcha and failed. Until (RFC 3339, "" when not told) is when
// the vendor's own day turns and the answer stops holding: Qoder's daily
// credits open at 10:00 UTC+8, not at Beijing midnight.
type Checkin struct {
	Outcome string  `json:"outcome"`
	Credit  float64 `json:"credit"`
	Streak  int     `json:"streak"`
	Message string  `json:"message"`
	Until   string  `json:"until"`
}

// AccountCheckin asks the plugin to check account of provider in for the
// day.
func AccountCheckin(ctx context.Context, provider, account string) (Checkin, error) {
	var c Checkin
	err := Call(ctx, "checkin", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &c)
	return c, err
}

// AccountUsage asks the plugin for account's usage of provider.
func AccountUsage(ctx context.Context, provider, account string) (Usage, error) {
	var u Usage
	err := Call(ctx, "usage", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &u)
	return u, err
}
