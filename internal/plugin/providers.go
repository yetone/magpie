package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
	Cost    *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
	// ImageSaid is whether the plugin (or models.dev) said if it takes
	// images: Image false without it is not known
	ImageSaid bool `json:"imageSaid"`
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
	Usage     bool    `json:"usage"`
	SignedIn  bool    `json:"signedIn"`
	AuthType  string  `json:"authType"`
	AccountID string  `json:"accountId"`
	Models    []Model `json:"models"`
	// FellBack says the plugin's models hook couldn't fetch its vendor's
	// list and gave the default one back.
	FellBack bool `json:"fellBack,omitempty"`
	// Accounts are the accounts signed in to it, the one kept under its
	// own id first; SignedIn, AuthType and AccountID are that one's.
	Accounts []Account `json:"accounts"`
	// MaxConcurrency is how many requests the plugin says each of its
	// accounts takes at once (magpie's own field, which OpenCode ignores:
	// the auth hook's maxConcurrency, else package.json's
	// magpie.maxConcurrency); 0 for none said. The user's setting on the
	// provider goes over it.
	MaxConcurrency int `json:"maxConcurrency,omitempty"`
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
	Models   []string `json:"models,omitempty"`
	FellBack bool     `json:"fellBack,omitempty"`
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
	if err := Call(ctx, "providers", map[string]any{"proxies": listingProxies()}, &ps); err != nil {
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
	provCache, provGood, provTried = ps, true, true
	provMu.Unlock()
	if b, err := json.Marshal(ps); err == nil {
		_ = writeWhole(providersPath(), b)
	}
	return ps
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
	if err := callWithTimeout(ctx, "providers", map[string]any{"proxies": listingProxies()}, &ps, refreshDeadline); err != nil {
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
		if provEpoch != epoch && !provGood && !provTried {
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

// listingProxies are the proxies each provider's model list is asked
// through, by provider and account key ("" the provider's own): a
// built-in's list is fetched through the account's proxy, so a plugin's
// is too.
func listingProxies() map[string]map[string]string {
	out := map[string]map[string]string{}
	if ProxyFor == nil {
		return out
	}
	var m map[string]json.RawMessage
	if b, err := steady.ReadFile(AuthPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	for k := range m {
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
			ps[i].Models, ps[i].FellBack = l.Models, false
		}
		for j, a := range p.Accounts {
			if !a.FellBack {
				continue
			}
			for _, b := range l.Accounts {
				if b.Key == a.Key && !b.FellBack && len(b.Models) > 0 {
					ps[i].Accounts[j].Models, ps[i].Accounts[j].FellBack = b.Models, false
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
	told := map[string]bool{}
	for _, p := range ps {
		told[p.Spec] = true
	}
	installed := map[string]bool{}
	for _, e := range list().Plugins {
		installed[e.Spec] = true
	}
	for _, p := range last {
		if installed[p.Spec] && !told[p.Spec] {
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

// refreshing is Cached's refreshes in the background.
var refreshing sync.WaitGroup

// Refreshed waits for Cached's refreshes in the background to end, the
// host left running: for tests that watch the providers' list on disk.
func Refreshed() { refreshing.Wait() }

// Settle waits for Cached's refreshes to end, then stops the host: for
// tests, whose folders the host runs in go when they end. Each ask is
// bounded and the retries are few, so this waits a bounded time even for
// a host that never answers.
func Settle() {
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
		if b, err := steady.ReadFile(providersPath()); err == nil {
			_ = json.Unmarshal(b, &ps)
		}
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
					p.Accounts[i].Models = w.Models
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
		start := !provBusy && !provGood && !provTried
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

type storedAuth struct {
	Type      string `json:"type"`
	AccountID string `json:"accountId"`
	Email     string `json:"email"`
	Key       string `json:"key"`
	Metadata  struct {
		Email string `json:"email"`
	} `json:"metadata"`
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

func readAuth() map[string]storedAuth {
	var m map[string]storedAuth
	if b, err := steady.ReadFile(AuthPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// SignedIn is whether a plugin sign-in is kept for provider.
func SignedIn(provider string) bool {
	return len(accountsOf(readAuth(), provider)) > 0
}

// Prompt is a question a sign-in method asks before it starts.
type Prompt struct {
	Type        string `json:"type"` // text, select
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
}

// NewAccount is the account a sign-in goes to when it is a new one: the
// provider's own id while nothing is kept there. A sign-in to an account
// already signed in replaces that one's instead.
const NewAccount = "new"

// Authorize begins an OAuth sign-in to account (a key, or NewAccount).
func Authorize(ctx context.Context, provider string, method int, inputs map[string]string, account string) (Authorization, error) {
	var a Authorization
	err := Call(ctx, "authorize", map[string]any{"provider": provider, "method": method, "inputs": inputs, "account": account}, &a)
	return a, err
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
// a "code" one with the code pasted back. It gives where the sign-in was
// saved.
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
// "api" method does.
func APIKey(ctx context.Context, provider string, method int, inputs map[string]string, key, account string) (Saved, error) {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Saved
	}
	if err := Call(ctx, "apiKey", map[string]any{"provider": provider, "method": method, "inputs": inputs, "key": key, "account": account}, &r); err != nil {
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
	b, err := steady.ReadFile(AuthPath())
	if err != nil {
		return nil
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
	var m map[string]map[string]any
	if b, err := steady.ReadFile(AuthPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	out := map[string]map[string]any{}
	for k, v := range m {
		if ProviderOf(k) == provider {
			out[k] = v
		}
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
}

type UsageWindow struct {
	Name      string   `json:"name"`
	Used      float64  `json:"used"`     // percent
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
}

// AccountUsage asks the plugin for account's usage of provider.
func AccountUsage(ctx context.Context, provider, account string) (Usage, error) {
	var u Usage
	err := Call(ctx, "usage", map[string]any{"provider": provider, "account": account, "proxy": proxyOf(ctx)}, &u)
	return u, err
}
