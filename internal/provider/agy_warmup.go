package provider

// Starting an agy CLI (Antigravity) account's next windows as soon as the
// last ones reset, as codex_warmup.go does a ChatGPT account's (0Block0_o
// on X). Google's quota is in pools of models (Gemini, Claude & GPT, …),
// each with its own 5-hour and 7-day window, and a pool's window starts at
// the first request to one of its models after it resets: until then it
// reads 100% left with its reset a whole window from each reading, rolling
// on (the owner's quota history: Gemini's 7 days stayed so from its reset
// till 10-28 07:24, then began at 07:24:29 with 99.92% left, while Claude &
// GPT's rolled on). A request to a Gemini model starts no Claude & GPT
// window, so each pool is warmed on its own, with one of its own models.
//
// The engine is the Codex one, unchanged, with each (account, pool) for an
// account: its windows are the pool's own (the plugin sets them aside, as
// display only; the warm-up reads them all the same). The request goes
// through the agy-cli plugin, which runs the agy CLI itself, as the
// provider editor's Test sends it: "hi", to that account, so it is agy's
// own request. What it saw and did is kept in agy-warmup.json.

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func agyWarmPath() string { return filepath.Join(filepath.Dir(Path()), "agy-warmup.json") }

// agyPoolSep is between an account and a pool in a warm-up key.
const agyPoolSep = " · "

// agyPoolKey is the warm-up's name for user's pool.
func agyPoolKey(user, pool string) string { return user + agyPoolSep + pool }

// agyTarget is where one (account, pool)'s warm-up goes.
type agyTarget struct {
	user, pool, model string
}

// agyWarm is the agy warm-up's reads and sends, and what its usage found
// for its send: the account, pool and model of each key it gave.
type agyWarm struct {
	read   func(context.Context) map[string]SubscriptionQuota // by account
	models func() []string
	post   func(ctx context.Context, user, model string) error

	mu      sync.Mutex
	targets map[string]agyTarget
}

// newAgyWarm is the agy warm-up as it runs: the agy-cli accounts' cached
// readings, the provider's models, the plugin's request.
func newAgyWarm() *agyWarm {
	return &agyWarm{read: agyWarmUsableLogins, models: agyModels, post: warmAgyModel}
}

// agyPools is the warm-up's view of usage, each agy-cli account's reading
// by user: one reading per (account, pool), its key agyPoolKey, holding
// the pool's own windows (not a model family's), set aside no longer, and
// the model of models a request to the pool goes to. A pool none of
// models is in, as when agy's catalog isn't read yet, is left out: nothing
// is known to start it.
func agyPools(usage map[string]SubscriptionQuota, models []string) (map[string]SubscriptionQuota, map[string]agyTarget) {
	out, targets := map[string]SubscriptionQuota{}, map[string]agyTarget{}
	for user, q := range usage {
		if q.Error != "" {
			continue
		}
		byPool := map[string][]QuotaWindow{}
		var pools []string
		for _, w := range q.Windows {
			if w.Pool == "" || w.Family != "" || w.Model != "" || w.Span <= 0 || w.Text || w.noShare || w.matches == nil {
				continue
			}
			if _, ok := byPool[w.Pool]; !ok {
				pools = append(pools, w.Pool)
			}
			w.Aside = false
			byPool[w.Pool] = append(byPool[w.Pool], w)
		}
		for _, pool := range pools {
			ws := byPool[pool]
			model := agyWarmModel(models, ws[0].matches)
			if model == "" {
				continue
			}
			key := agyPoolKey(user, pool)
			p := q
			p.Windows = ws
			out[key] = p
			targets[key] = agyTarget{user: user, pool: pool, model: model}
		}
	}
	return out, targets
}

// agyWarmModel is the model of models (the provider's, in its order) that
// a pool's warm-up asks, in matches: the lightest by its name (a flash or
// open model, a low effort), so the request spends as little of the pool
// as one can. "" when none is.
func agyWarmModel(models []string, matches func(string) bool) string {
	var in []string
	for _, m := range models {
		if matches(strings.ToLower(m)) {
			in = append(in, m)
		}
	}
	weight := func(id string) int {
		// by the id's words: "gemini" holds "mini"
		words := strings.Split(strings.ToLower(id), "-")
		n := 4
		if slices.ContainsFunc(words, func(w string) bool {
			return slices.Contains([]string{"flash", "lite", "oss", "haiku", "mini", "nano"}, w)
		}) {
			n -= 2
		}
		switch words[len(words)-1] {
		case "low", "minimal":
			n--
		case "high", "xhigh", "max":
			n++
		}
		return n
	}
	slices.SortStableFunc(in, func(a, b string) int {
		return cmp.Or(cmp.Compare(weight(a), weight(b)), cmp.Compare(len(a), len(b)))
	})
	if len(in) == 0 {
		return ""
	}
	return in[0]
}

// agyModels is the agy-cli provider's models, but the ones the user
// disabled: a request to one is refused.
func agyModels() []string {
	pp, ok := PluginOf(agyCLIID)
	if !ok {
		return nil
	}
	var out []string
	for _, m := range pp.Models {
		if _, off := ModelDisabled(PluginID(pp.ID), m.ID); !off {
			out = append(out, m.ID)
		}
	}
	return out
}

// agyWarmUsableLogins is the agy-cli accounts' readings, but the ones whose
// sign-in lapsed.
func agyWarmUsableLogins(ctx context.Context) map[string]SubscriptionQuota {
	u := LoginUsage(ctx, agyCLIID)
	for _, l := range Logins(agyCLIID) {
		if l.Lapsed != "" {
			delete(u, l.User)
		}
	}
	return u
}

func (a *agyWarm) usage(ctx context.Context) map[string]SubscriptionQuota {
	out, targets := agyPools(a.read(ctx), a.models())
	a.mu.Lock()
	a.targets = targets
	a.mu.Unlock()
	return out
}

func (a *agyWarm) target(key string) (agyTarget, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.targets[key]
	return t, ok
}

// send sends key's pool one request to its model, on its account.
func (a *agyWarm) send(ctx context.Context, key string) error {
	t, ok := a.target(key)
	if !ok {
		return fmt.Errorf("no agy pool %q", key)
	}
	return a.post(ctx, t.user, t.model)
}

// warmAgyModel sends the agy-cli account user one tiny request to model,
// through the plugin, as the provider editor's Test of that account sends
// it.
func warmAgyModel(ctx context.Context, user, model string) error {
	p, err := Find(PluginID(agyCLIID))
	if err != nil {
		return err
	}
	rs := p.TestModels(TestOn(ctx, user), []string{model})
	if len(rs) == 0 {
		return fmt.Errorf("%s: no answer", model)
	}
	if r := rs[0]; !r.OK {
		return fmt.Errorf("%s: %s", model, cmp.Or(r.Error, "no answer"))
	}
	return nil
}

// AgyWarmed is when magpie last started a window of each agy-cli
// account, any pool's, by user as kept (lower-cased).
func AgyWarmed() map[string]time.Time { return agyWarmedOf(codexWarmedIn(agyWarmPath())) }

// agyWarmedOf is by-pool warm-ups (agyPoolKey) by account.
func agyWarmedOf(byKey map[string]time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	for key, at := range byKey {
		user, _, _ := strings.Cut(key, agyPoolSep)
		if at.After(out[user]) {
			out[user] = at
		}
	}
	return out
}

// agyWarmer is the agy warm-up's engine, with a's reads and sends.
func agyWarmer(a *agyWarm) codexWarmer {
	return codexWarmer{path: agyWarmPath(), now: time.Now, usage: a.usage, send: a.send,
		warmed: func(key string) {
			if t, ok := a.target(key); ok {
				StaleAllowance(agyCLIID, t.user)
			}
		}}
}

// KeepAgyWindowsWarm starts the agy-cli accounts' windows as they reset,
// each pool's with a request to one of its models, while settings say to.
// There is no time of day for them: Google's 5-hour windows aren't kept to
// one, as Codex's and Claude's daily warm-ups are.
func KeepAgyWindowsWarm(ctx context.Context) {
	keepWarm(ctx, agyCLIID, agyWarmer(newAgyWarm()), func() (string, []string) { return settings.Load().AgyWarmup, nil })
}

// warmAgyNow is Warm up now for the agy-cli account user: one request to
// each pool of it whose windows aren't running, read as the warm-up reads
// them; every pool, when none is known (an account never read).
func warmAgyNow(ctx context.Context, user string, usage map[string]SubscriptionQuota, models []string, now time.Time,
	send func(ctx context.Context, user, model string) error) error {
	one := map[string]SubscriptionQuota{}
	for u, q := range usage {
		if strings.EqualFold(u, user) {
			one[u] = q
		}
	}
	pools, targets := agyPools(one, models)
	keys := make([]string, 0, len(pools))
	for k := range pools {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return fmt.Errorf("no pool of %s's agy quota is known to warm up: its usage isn't read yet", user)
	}
	var errs []string
	sent := 0
	for _, k := range keys {
		if windowsRunning(pools[k], now) {
			continue
		}
		sent++
		if err := send(ctx, targets[k].user, targets[k].model); err != nil {
			errs = append(errs, targets[k].pool+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if sent == 0 {
		return errAgyRunning
	}
	return nil
}

// errAgyRunning is Warm up now finding every pool's windows running.
var errAgyRunning = fmt.Errorf("every pool's windows are running already")

// agyByAccount is usage with each account's pools' windows its own, set
// aside no longer, for Warm up all to tell an account whose every pool
// runs already (windowsRunning).
func agyByAccount(usage map[string]SubscriptionQuota, models []string) map[string]SubscriptionQuota {
	pools, targets := agyPools(usage, models)
	out := make(map[string]SubscriptionQuota, len(usage))
	for user, q := range usage {
		q.Windows = nil
		out[user] = q
	}
	for k, p := range pools {
		user := targets[k].user
		q := out[user]
		q.Windows = append(q.Windows, p.Windows...)
		out[user] = q
	}
	return out
}
