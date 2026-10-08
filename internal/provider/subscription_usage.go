package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
)

// QuotaWindow is one rolling allowance reported by a subscription provider.
type QuotaWindow struct {
	Unlimited bool       `json:"unlimited,omitempty"`
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	ResetSecs int64      `json:"resetSecs,omitempty"`
	Display   string     `json:"display,omitempty"`
	// Amount of Limit is the window's own count in Unit, when the vendor
	// counts it so (WorkBuddy's credits): Used is Amount as a share of
	// Limit. The GUI says it as used or left, as it says the share (#659).
	Amount float64 `json:"amount,omitempty"`
	Limit  float64 `json:"limit,omitempty"`
	Unit   string  `json:"unit,omitempty"`
	// Family is the model family a per-model window belongs to (Antigravity's
	// "Gemini 3.1 Pro (High)" is Gemini's), for the GUI to show one figure a
	// family, the tightest; each window is still here, and routing reads
	// them one by one.
	Family string `json:"family,omitempty"`
	// Pool is the group of models that share one allowance (Antigravity's
	// "Gemini", "Claude & GPT"): on the group's own windows, a 5-hour and
	// a weekly, which name no Model, and on each per-model window drawing
	// on it, for the GUI to show the group's windows in place of its
	// models'.
	Pool string `json:"pool,omitempty"`

	// For routing (see Allowances): how long the window runs, zero when
	// not known; the only models it counts, by a word in their ids
	// ("opus"), when it doesn't count them all; and Aside when using it up
	// doesn't stop the account — on-demand spending past the allowance,
	// or Copilot's code completions, which no request here makes.
	Span  time.Duration `json:"-"`
	Model string        `json:"-"`
	Aside bool          `json:"-"`
	// Capped is set, for the GUI only (WithCapped), on a window an
	// account's usage cap counts: neither Aside nor unlimited. One model's
	// own counts for that model (CapHeld), so it is marked like the rest.
	Capped bool `json:"capped,omitempty"`
	// CapsSome is set beside Capped on a window that counts some models
	// only — one model's own, or a pool's — so the cap holds the account
	// for those alone, and the GUI says which.
	CapsSome bool `json:"capsSome,omitempty"`
	// Holds is what the whole window holds, reckoned from magpie's own
	// calls through the account (usage.WithWindowHolds), for the GUI only;
	// nil where that can't be told honestly.
	Holds *WindowHolds `json:"holds,omitempty"`
	// matches further scopes pools whose membership isn't one model word.
	matches func(string) bool
	// partial is set on the windows of a reading that may leave some out:
	// a Claude account only heard of as Claude Code answered (its
	// rate_limit_event names one window at a time), never read whole by
	// /usage — five hours alone there needn't mean no week.
	partial bool
}

// SubscriptionQuota is provider-reported allowance usage. This is separate
// from Dial's local token log: vendors expose percentages, not token totals.
type SubscriptionQuota struct {
	Provider  string        `json:"provider"`
	Name      string        `json:"name"`
	Icon      string        `json:"icon"`
	Plan      string        `json:"plan,omitempty"`
	AccessSKU string        `json:"accessSku,omitempty"`
	User      string        `json:"user,omitempty"` // the account, so two of one vendor tell apart
	Windows   []QuotaWindow `json:"windows"`
	Balance   string        `json:"balance,omitempty"` // what is left on an API key, instead of windows
	// BalanceParts are the Balance's amounts each apart, when the balance
	// field the user wrote has several or a percent (cardParts)
	BalanceParts []BalancePart `json:"balanceParts,omitempty"`
	// BalanceTrend is the Balance over time, as magpie read it, and when
	// it runs out at that pace (balance_history.go)
	BalanceTrend *BalanceTrend `json:"balanceTrend,omitempty"`
	// Until is when the plan's paid time ends: it renews then when Renew
	// is "auto", is over when "off", and either when "" (the vendor
	// doesn't say which).
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	Error string     `json:"error,omitempty"`
	// AsOf is when an allowance shown in place of one that couldn't be
	// read was read (see keepLast); nil for a reading just made.
	AsOf *time.Time `json:"asOf,omitempty"`
	// ReadAt is when what is shown was read: a key's balance, kept a
	// minute (KeyBalances), or an account's allowance, which a cache (a
	// minute's, or a Claude account's until Claude Code tells it again)
	// can hand back long after; nil when not known.
	ReadAt *time.Time `json:"readAt,omitempty"`
	// Resets are the rate-limit resets a Codex account holds, nil when it
	// holds none (codex_resets.go).
	Resets *ResetCredits `json:"resets,omitempty"`
	// Held is set on a ChatGPT account the Codex app sends nothing for
	// now (codexHeld): not on a window at 100% alone, which credits get
	// past.
	Held bool `json:"held,omitempty"`
	// Daily is a WorkBuddy account's credits used day by day, as magpie
	// counted them from its readings (credits_daily.go).
	Daily *DailyCredits `json:"daily,omitempty"`
	// Checkins is set on a WorkBuddy (China) or Trae CN account whose
	// daily check-in is pressed for, CheckinBy whose ("" WorkBuddy's,
	// "trae" Trae CN's), and Checkin is how its last one went, nil before
	// the first; never cached, added as the page asks (WithCheckins).
	Checkins  bool              `json:"checkins,omitempty"`
	CheckinBy string            `json:"checkinBy,omitempty"`
	Checkin   *WorkBuddyCheckin `json:"checkin,omitempty"`
	// Kind is the card's kind as another magpie is told it (CachedCards):
	// subscription, plan or balance; "" here.
	Kind string `json:"kind,omitempty"`
	// From is the remote magpie a card is that one's (remote_quotas.go),
	// by its name here; "" for this computer's own.
	From string `json:"from,omitempty"`
	// In-process read order, separate from the vendor's ReadAt and never
	// persisted: restarting starts a new sequence.
	readSeq uint64
	// glmPlan marks a key's quota read from the GLM Coding Plan endpoint,
	// including custom providers. Only these can share ZCode's allowance.
	glmPlan bool
}

var subscriptionUsageCache struct {
	sync.Mutex
	at      time.Time
	data    []SubscriptionQuota
	pending chan struct{} // closed when the refresh in flight is done
	asked   bool          // the user asked (AskClaudeUsage): wait for the refresh
}

// OnSubscriptionUsage sets what is told when a refresh has landed, for what
// shows a stale copy meanwhile (the menu bar's text) to read the new one; nil
// tells nothing. It is held atomically: a refresh runs in the background and
// may be under way while it is set (#1023).
func OnSubscriptionUsage(f func()) {
	if f == nil {
		onSubscriptionUsage.Store(nil)
		return
	}
	onSubscriptionUsage.Store(&f)
}

var onSubscriptionUsage atomic.Pointer[func()]

// subscriptionTimeout bounds one refresh; the vendors' endpoints can be
// unreachable without a proxy, and then each fetch would hang to it.
// A plugin's account waits as long as the plugin itself does (20s).
var subscriptionTimeout = 20 * time.Second

// SubscriptionUsage returns rolling quotas for signed-in first-party agents.
// Results are cached because these private account endpoints are aggressively
// rate limited when several CLI sessions are active. Once there is something
// cached it comes back at once, and a stale copy is refreshed in the
// background; only the very first call waits, for as long as ctx allows.
func SubscriptionUsage(ctx context.Context) []SubscriptionQuota {
	c := &subscriptionUsageCache
	c.Lock()
	have, fresh := c.data != nil, time.Since(c.at) < time.Minute
	if c.asked {
		have, c.asked = false, false
	}
	if !fresh && c.pending == nil {
		done := make(chan struct{})
		c.pending = done
		readCtx, seq := quotaReading(context.Background())
		go func() {
			start := time.Now()
			out := fetchSubscriptionUsage(readCtx)
			noteDailyCredits(out, time.Now())
			noteQuotaHistory(out, time.Now())
			c.Lock()
			c.data = cacheCards(c.data, out, seq, false)
			c.at, c.pending = time.Now(), nil
			if claudeAsked.Load() > start.UnixNano() {
				c.at = time.Time{} // asked meanwhile: read again
			}
			c.Unlock()
			// told before done is closed, so whoever waits for the refresh
			// has it finished, hook and all
			if f := onSubscriptionUsage.Load(); f != nil {
				(*f)()
			}
			close(done)
		}()
	}
	pending := c.pending
	c.Unlock()
	if !have && pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
			return nil
		}
	}
	c.Lock()
	out := visibleQuotas(c.data)
	c.Unlock()
	return withDailyCredits(out, time.Now())
}

// SubscriptionUsageReading says a read of the accounts' usage is under way:
// what SubscriptionUsage just gave may be the stale copy it replaces, for a
// page to ask again once it lands (#959: the panel, opened, kept the old
// copy until refreshed by hand, while the window's next read had the new).
func SubscriptionUsageReading() bool {
	c := &subscriptionUsageCache
	c.Lock()
	defer c.Unlock()
	return c.pending != nil
}

// visibleQuotas drops accounts removed from magpie since the last refresh.
func visibleQuotas(all []SubscriptionQuota) []SubscriptionQuota {
	hidden := map[string]bool{}
	for _, p := range load().Providers {
		hidden[p.ID] = p.Hidden || p.Off // switched off: not asked either
	}
	var chosen map[string]map[string]bool
	out := []SubscriptionQuota{}
	for _, q := range all {
		if hidden[q.Provider] {
			continue
		}
		if q.Provider == "gemini" || q.Provider == "antigravity" {
			if chosen == nil {
				chosen = exposedIDs()
			}
			var base func(string) string
			if q.Provider == "antigravity" {
				// Antigravity's quota names each level's id; magpie offers
				// the family (gemini-3.7-flash-high is gemini-3.7-flash)
				base = func(id string) string {
					if b, _, ok := AntigravityBase(id); ok {
						return b
					}
					return id
				}
			}
			q.Windows = chosenWindows(q.Windows, chosen[q.Provider], base)
		}
		out = append(out, q)
	}
	return out
}

// exposedIDs is, for each provider, the models magpie offers from it.
func exposedIDs() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range All() {
		ids := map[string]bool{}
		for _, m := range p.Exposed() {
			ids[m.ID] = true
		}
		out[p.ID] = ids
	}
	return out
}

// chosenWindows keeps a Google account's allowances for the models the user
// enabled: Antigravity reports one for every model it has, a couple of dozen,
// most of them never used through magpie. When none of them is enabled — the
// ids a quota names aren't always the ones served — they are all kept.
// base, when not nil, is the model magpie offers for an id a quota names.
func chosenWindows(ws []QuotaWindow, chosen map[string]bool, base func(string) string) []QuotaWindow {
	var out []QuotaWindow
	some := false // a model's window kept, not only a pool's
	for _, w := range ws {
		m := w.Model
		if base != nil && m != "" {
			m = base(m)
		}
		if w.Model == "" || chosen[m] {
			out = append(out, w)
			some = some || w.Model != ""
		}
	}
	if !some {
		return ws
	}
	return out
}

// fetchSubscriptionUsage asks every signed-in vendor at once.
func fetchSubscriptionUsage(ctx context.Context) []SubscriptionQuota {
	ctx, _ = quotaReading(ctx)
	// read for every card's caller alike, or for the one card ctx reads
	// again (RefreshUsage), as long as a refresh of them all would be
	ctx0, cancel := context.WithTimeout(context.WithoutCancel(ctx), subscriptionTimeout)
	defer cancel()
	// each account asked through its own proxy, if it has one (#237)
	proxies := map[string]string{}
	for _, p := range load().Providers {
		proxies[p.ID] = p.Proxy
	}
	via := func(id string) context.Context { return netproxy.With(ctx0, proxies[id]) }
	// and one account of several through its own, if it has one (perLogin
	// and loginQuota ask each so)
	viaLogin := func(id, user string) context.Context { return ViaLogin(ctx0, id, user) }
	hidden := map[string]bool{}
	for _, p := range load().Providers {
		hidden[p.ID] = p.Hidden || p.Off // switched off: not asked either
	}
	if r, ok := refreshing(ctx); ok {
		// one card read again: the others not asked
		for _, p := range load().Providers {
			hidden[p.ID] = hidden[p.ID] || p.ID != r.provider
		}
		for _, id := range []string{"claude", "cursor", "grok", "codex", "copilot", "kiro", "zcode", wbCN.id, wbAI.id, CommandCodePlanID, "qoder", QoderCNID, "zed", "devin", "factory", MiMoID, ChatGPTAPIID, "gemini", "antigravity"} {
			hidden[id] = hidden[id] || id != r.provider
		}
		for _, pp := range plugin.Cached() {
			id := PluginID(pp.ID)
			hidden[id] = hidden[id] || id != r.provider
			hidden[pp.ID] = hidden[pp.ID] || pp.ID != r.provider
		}
	}
	var fetches []func() SubscriptionQuota
	// a built-in moved onto its plugin shows the plugin's cards in its
	// place, and none of its own: an agent's own sign-in it still finds
	// would be a second card of the same account
	placed := map[string]bool{}
	moved := func(id string) bool {
		if !Moved(id) {
			return false
		}
		placed[id] = true
		if !hidden[id] {
			fetches = append(fetches, pluginUsageFetchesOf(via, id)...)
		}
		return true
	}
	if p, ok := claudeAccount(); ok && !hidden["claude"] {
		// signed out, Claude Code's own allowance is none: the account in
		// its place is a saved one, read as the others are
		if ls := accountsOf("claude"); len(ls) > 1 || p.Account.standIn {
			fetches = append(fetches, perLogin(via("claude"), ls, accountCard("claude"))...)
		} else {
			fetches = append(fetches, withUser(ctx, p.Account.User, func() SubscriptionQuota {
				return claudeSubscriptionUsage(viaLogin("claude", p.Account.User), p.Account.User)
			}))
		}
	}
	if user, plan, ok := cursorIdentity(); !moved("cursor") && ok && !hidden["cursor"] {
		fetches = append(fetches, withUser(ctx, user, func() SubscriptionQuota { return cursorSubscriptionUsage(viaLogin("cursor", user), plan) }))
	}
	if _, ok := grokAccount(); !moved("grok") && ok && !hidden["grok"] {
		fetches = append(fetches, func() SubscriptionQuota { return keepReading(ctx, readNow(grokSubscriptionUsage(via("grok"))), "") })
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p, ok := codexAccount(home); ok && !hidden["codex"] {
			if ls := accountsOf("codex"); len(ls) > 1 {
				fetches = append(fetches, perLogin(via("codex"), ls, accountCard("codex"))...)
			} else {
				auth := filepath.Join(home, ".codex", "auth.json")
				fetches = append(fetches, withUser(ctx, p.Account.User, func() SubscriptionQuota { return codexSubscriptionUsage(viaLogin("codex", p.Account.User), auth) }))
			}
		}
		// Every Copilot account magpie knows, the editors' or the CLI's own
		// sign-in or not: one signed in from magpie alone is enough
		// (copilotLoginList reads both, copilot_accounts.go). Asking only
		// when copilotLogin found the editors' token left an account magpie
		// signed in itself without a card on the Usage page, and out of the
		// gateway's GET /v1/magpie/quotas, however fresh its reading was
		// (subscription_usage_test.go, TestCopilotQuotaWithoutEditorsSignIn).
		if ls := copilotLoginList(); len(ls) > 0 && !hidden["copilot"] {
			fetches = append(fetches, perLogin(via("copilot"), ls, accountCard("copilot"))...)
		}
	}
	if moved("kiro") {
	} else if key := kiroKey(); key != "" && !hidden["kiro"] {
		fetches = append(fetches, func() SubscriptionQuota { return keepReading(ctx, readNow(kiroQuotaAt(via("kiro"), key, "")), "") })
	} else if !hidden["kiro"] {
		fetches = append(fetches, perLogin(via("kiro"), kiroLoginList(), accountCard("kiro"))...)
	}
	if !moved("zcode") && !hidden["zcode"] {
		fetches = append(fetches, perLogin(via("zcode"), zcodeLoginList(), accountCard("zcode"))...)
	}
	for _, w := range []*wbSite{wbCN, wbAI} {
		if !moved(w.id) && !hidden[w.id] {
			fetches = append(fetches, perLogin(via(w.id), wbLoginList(w), accountCard(w.id))...)
		}
	}
	if !moved(CommandCodePlanID) && !hidden[CommandCodePlanID] {
		fetches = append(fetches, perLogin(via(CommandCodePlanID), cmdLoginList(), accountCard(CommandCodePlanID))...)
	}
	if !moved("qoder") && !hidden["qoder"] {
		fetches = append(fetches, perLogin(via("qoder"), loginsOf(qoderLogins()), accountCard("qoder"))...)
	}
	if !moved(QoderCNID) && !hidden[QoderCNID] {
		fetches = append(fetches, perLogin(via(QoderCNID), loginsOf(qoderLoginsOf(QoderCNID)), accountCard(QoderCNID))...)
	}
	if !moved("zed") && !hidden["zed"] {
		fetches = append(fetches, perLogin(via("zed"), zedLoginList(), accountCard("zed"))...)
	}
	if !moved("devin") && !hidden["devin"] {
		fetches = append(fetches, perLogin(via("devin"), devinLoginList(), accountCard("devin"))...)
	}
	if !moved("factory") && !hidden["factory"] {
		fetches = append(fetches, perLogin(via("factory"), factoryLoginList(), accountCard("factory"))...)
	}
	if !moved(MiMoID) && !hidden[MiMoID] {
		fetches = append(fetches, perLogin(via(MiMoID), mimoLoginList(), accountCard(MiMoID))...)
	}
	for _, agent := range []string{"gemini", "antigravity"} {
		if hidden[agent] {
			continue
		}
		// read as loginQuota reads them (googleLoginQuota): one reading
		// with LoginUsage
		for _, l := range googleLogins(agent) {
			if r, ok := refreshing(ctx); ok && r.user != "" && !strings.EqualFold(l.User, r.user) {
				continue
			} else if ok {
				forgetLoginReading(l.Login)
			}
			fetches = append(fetches, func() SubscriptionQuota { return loginReading(viaLogin(agent, l.User), l.Login).read })
		}
	}
	fetches = append(fetches, pluginUsageFetches(via, hidden, placed)...)
	// each fetch keeps what it read (keepLast) itself: an account's
	// reading, shared with LoginUsage, is kept once
	out := make([]SubscriptionQuota, len(fetches))
	var wg sync.WaitGroup
	for i, f := range fetches {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = f() }()
	}
	wg.Wait()
	return out
}

// accountsOf is every account magpie remembers for agent, the one it is
// signed in to first.
func accountsOf(agent string) []Login {
	ls := Logins(agent)
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Active && !ls[j].Active })
	return ls
}

// withUser names the account a fetch is for, and keeps its reading
// (keepLast).
func withUser(ctx context.Context, user string, f func() SubscriptionQuota) func() SubscriptionQuota {
	return func() SubscriptionQuota {
		q := f()
		q.User = user
		return keepReading(ctx, readNow(q), "")
	}
}

// perLogin fetches each account's allowance on a card of its own: the
// reading LoginUsage shows beside the account (loginReading).
func perLogin(ctx context.Context, ls []Login, face cardFace) []func() SubscriptionQuota {
	var out []func() SubscriptionQuota
	for _, l := range refreshLogins(ctx, ls) {
		out = append(out, func() SubscriptionQuota {
			q := loginReading(ctx, l).read
			q.Name, q.Icon, q.User = face.name, face.icon, l.User
			return q
		})
	}
	return out
}

func accountJSON(ctx context.Context, url, token string, headers map[string]string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		e := &accountStatusError{status: res.StatusCode}
		if secs, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && secs > 0 {
			e.retryAfter = time.Duration(secs) * time.Second
		}
		return e
	}
	return json.Unmarshal(b, dst)
}

type accountStatusError struct {
	status     int
	retryAfter time.Duration // what the vendor asked to wait, when it said
}

func (e *accountStatusError) Error() string { return http.StatusText(e.status) }

// claudeSubscriptionUsage is the allowance of user, the account Claude Code
// is signed in to, by the name magpie gives it (claudeAccount): its
// readings and what it said answering are kept under that name, which for
// a Team seat isn't the email alone `claude auth status` gives.
func claudeSubscriptionUsage(ctx context.Context, user string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Windows: []QuotaWindow{}}
	if _, _, ok := claudeCredential(); !ok {
		q.Error = "Claude Code is signed out; run claude auth login"
		return q
	}
	_, plan, _ := claudeIdentity()
	q.Plan = plan
	var err error
	q.Windows, err = claudeWindows(ctx, user, true)
	if err != nil {
		q.Error = err.Error()
	} else {
		q.ReadAt = claudeReadAt(user)
	}
	return q
}

// readNow stamps a reading just made, one that doesn't say when it was
// read itself, with the time.
func readNow(q SubscriptionQuota) SubscriptionQuota {
	if q.Error == "" && q.AsOf == nil && q.ReadAt == nil && (len(q.Windows) > 0 || q.Balance != "") {
		now := time.Now()
		q.ReadAt = &now
	}
	return q
}

// claudeReadAt is when what is kept of user's Claude allowance was told:
// by /usage or as Claude Code answered. Nil when nothing is.
func claudeReadAt(user string) *time.Time {
	claudeUsage.Lock()
	e, ok := claudeUsage.m[strings.ToLower(user)]
	claudeUsage.Unlock()
	if !ok || e.at.IsZero() {
		return nil
	}
	at := e.at
	return &at
}

// claudeUsage keeps each Claude account's allowance as Claude Code last
// told it: its /usage, run when the user asks (AskClaudeUsage) and only for
// the account it is signed in to, or what it said as it answered
// (NoteClaudeLimits). magpie never asks Anthropic for it itself; the Usage
// page's timer, the switch list and routing show what is kept here.
var claudeUsage struct {
	sync.Mutex
	m map[string]claudeUsageEntry
}

type claudeUsageEntry struct {
	at    time.Time
	ws    []QuotaWindow
	heard time.Time     // when Claude Code last told it, answering
	tried time.Time     // when /usage was last run, answered or not
	wait  time.Duration // how long after tried it runs again unasked
	err   error         // what the last run said, when it failed
	whole bool          // /usage was read: ws has every window the account has
}

// windows is what e keeps as of now, partial where /usage never read it.
func (e claudeUsageEntry) windows(now time.Time) []QuotaWindow {
	ws := elapsed(e.ws, now)
	for i := range ws {
		ws[i].partial = !e.whole
	}
	return ws
}

// claudeAskFloor is the least time between two readings, however often the
// user refreshes; between two that nobody asked for (a reading magpie keeps
// up to date by itself, for the Usage page left open, the menu bar's
// figures and routing) it is claudeUsageWait, drawn afresh after each run,
// so /usage isn't run on a clock, and then only once Claude Code has been
// used since (claudeUsedSince): an allowance nobody used hasn't moved.
const (
	claudeAskFloor     = 30 * time.Second
	claudeUsageWaitMin = 5 * time.Minute
	claudeUsageWaitMax = 15 * time.Minute
)

// claudeUsageWait is how long after a run of /usage the next unasked one is
// due: a whole minute from claudeUsageWaitMin to claudeUsageWaitMax, at
// random. A var so tests can fix it.
var claudeUsageWait = func() time.Duration {
	return claudeUsageWaitMin + rand.N(claudeUsageWaitMax-claudeUsageWaitMin+time.Minute)/time.Minute*time.Minute
}

// claudeUsedSince says whether Claude Code was used since t: one of its
// sessions (a .jsonl in a project's folder under its projects/) was written
// to since. A var so tests can say.
var claudeUsedSince = func(t time.Time) bool {
	projects := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects")
	dirs, _ := os.ReadDir(projects)
	for _, d := range dirs {
		root := filepath.Join(projects, d.Name())
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		files, _ := os.ReadDir(root)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			if fi, err := f.Info(); err == nil && fi.ModTime().After(t) {
				return true
			}
		}
	}
	return false
}

// claudeAsked is when the user last asked to see Claude's usage (unix
// nanoseconds; zero: never).
var claudeAsked atomic.Int64

// AskClaudeUsage is the user asking to see Claude's usage — opening the
// Usage page, refreshing it, `magpie quota` — so Claude Code's /usage is
// run at once rather than when the next unasked reading is due
// (claudeUsageWait); the next SubscriptionUsage waits for it.
func AskClaudeUsage() {
	claudeAsked.Store(time.Now().UnixNano())
	AskUsage()
	l := &loginUsageCache
	l.Lock()
	for k := range l.m {
		if strings.HasPrefix(k, "claude/") {
			delete(l.m, k)
		}
	}
	l.Unlock()
}

// AskUsage has the next SubscriptionUsage wait for a new reading rather
// than hand back the last one, as AskClaudeUsage does, without having
// Claude Code's /usage run: magpie quota wait reads again each time it
// wakes, and only a wait for Claude needs Claude read.
func AskUsage() {
	c := &subscriptionUsageCache
	c.Lock()
	c.at, c.asked = time.Time{}, true
	c.Unlock()
	// each account's reading, which LoginUsage shares, is read again too;
	// kept, for a hiccup to keep what was known
	l := &loginUsageCache
	l.Lock()
	for k, e := range l.m {
		e.at = time.Time{}
		l.m[k] = e
	}
	l.Unlock()
}

// claudeWindows is the allowance of the Claude account user. Only the
// account Claude Code is signed in to (active) is read, by Claude Code's
// own /usage: when the user asked since it last was, or when the last
// reading's claudeUsageWait is up and Claude Code was used since; any other
// time it is what was kept.
// magpie itself never asks Anthropic.
func claudeWindows(ctx context.Context, user string, active bool) ([]QuotaWindow, error) {
	key := strings.ToLower(user)
	c := &claudeUsage
	now := time.Now()
	asked := claudeAsked.Load()
	c.Lock()
	e, ok := c.m[key]
	c.Unlock()
	due := e.tried.IsZero() || asked > e.tried.UnixNano() ||
		active && now.Sub(e.tried) >= e.wait && (e.heard.After(e.tried) || claudeUsedSince(e.tried))
	read := active && due && now.Sub(e.tried) >= claudeAskFloor
	c.Lock()
	// one reading an ask or a wait, its first caller's; the others keep
	// to it
	if f := c.m[key]; read && !f.tried.Equal(e.tried) {
		read, e = false, f
	}
	if read {
		if c.m == nil {
			c.m = map[string]claudeUsageEntry{}
		}
		e.tried, e.wait = now, claudeUsageWait()
		c.m[key] = e
	}
	c.Unlock()
	heard := e.ws != nil && now.Sub(e.heard) < claudeHeard
	if !read {
		switch {
		case ok && e.err != nil && !(heard && !claudeUsageDenied.MatchString(e.err.Error())):
			return []QuotaWindow{}, e.err
		case ok && e.ws != nil:
			return e.windows(now), nil
		case active:
			return []QuotaWindow{}, errClaudeNotAsked
		default:
			return []QuotaWindow{}, errClaudeSaved
		}
	}
	// /usage tells of the account Claude Code is signed in to as it runs:
	// one it was moved off before or while it ran (a switch, "Make first")
	// is another's, and kept as user's it showed an account nobody used as
	// spent as the one it was moved to (nil_1024)
	kept := func() ([]QuotaWindow, error) {
		if e.ws != nil {
			return e.windows(now), nil
		}
		return []QuotaWindow{}, errClaudeNotAsked
	}
	if ClaudeCodeMovedOff(user) {
		return kept()
	}
	ws, err := readClaudeUsage(ctx)
	if ClaudeCodeMovedOff(user) {
		return kept()
	}
	if err != nil {
		c.Lock()
		if f, ok := c.m[key]; ok {
			// A failed read cannot establish that an account refusal cleared.
			// A successful reading or a new header clears it instead.
			if f.err != nil && claudeUsageDenied.MatchString(f.err.Error()) && !claudeUsageDenied.MatchString(err.Error()) {
				err = f.err
			}
			if f.tried.Equal(now) {
				f.err = err
				c.m[key] = f
			}
			e = f // headers received while /usage ran are newer than e
		}
		c.Unlock()
		if e.ws != nil && time.Since(e.heard) < claudeHeard && !claudeUsageDenied.MatchString(err.Error()) {
			return e.windows(time.Now()), nil // a fresh header stands unless the account was refused
		}
		return ws, err
	}
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	c.m[key] = claudeUsageEntry{at: now, ws: ws, heard: e.heard, tried: now, wait: e.wait, whole: true}
	c.Unlock()
	// as of now, as a kept reading is: /usage gives a reset to the minute
	// and can lag it, so a window it still counts full may have started
	// again already
	return elapsed(ws, time.Now()), nil
}

var (
	errClaudeNotAsked = errors.New("not read yet: magpie reads Claude's usage by running Claude Code's /usage")
	errClaudeSaved    = errors.New("magpie doesn't read a saved account's usage; it shows what Claude Code reports while using it")
)

// elapsed is ws as of now: a window that has reset since it was read
// starts again from nothing.
func elapsed(ws []QuotaWindow, now time.Time) []QuotaWindow {
	out := make([]QuotaWindow, len(ws))
	for i, w := range ws {
		if w.ResetsAt != nil && !now.Before(*w.ResetsAt) {
			w.Used, w.ResetsAt = 0, nil
		}
		out[i] = w
	}
	return out
}

// claudeScopeModel is the word a model-scoped window counts models by, from
// the name Anthropic gives it: "Fable" counts claude-fable-*, "Fable 5.1"
// claude-fable-5-1.
func claudeScopeModel(name string) string {
	return strings.NewReplacer(" ", "-", ".", "-").Replace(strings.ToLower(name))
}

func codexSubscriptionUsage(ctx context.Context, path string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Windows: []QuotaWindow{}}
	token, accountID, err := codexToken(ctx, path)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan, q.Windows, q.Resets, q.Balance, q.Held, err = codexWindows(ctx, token, accountID)
	if b, rerr := os.ReadFile(path); rerr == nil {
		q.Until = codexUntil(b, time.Now())
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// codexUntil is when the ChatGPT plan of a Codex sign-in (auth.json) is
// paid until, as its id token says: nil when it doesn't, or names a time
// gone by — a token not refreshed since, which says nothing of now.
func codexUntil(auth []byte, now time.Time) *time.Time {
	var a codexAuth
	if json.Unmarshal(auth, &a) != nil {
		return nil
	}
	s := claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_subscription_active_until")
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || !t.After(now) {
		return nil
	}
	return &t
}

// codexWindows is the plan, allowance, rate-limit resets and credits of
// the ChatGPT account token signs in to. credits is what is left of the
// credits the account bought or was given (#571), which Codex spends once
// a window is used up, "" when it holds none or they are unlimited. held
// is whether the Codex app sends nothing for the account now (codexHeld).
func codexWindows(ctx context.Context, token, accountID string) (plan string, out []QuotaWindow, resets *ResetCredits, credits string, held bool, err error) {
	var data codexUsage
	base := strings.TrimSuffix(CodexBase, "/codex")
	out = []QuotaWindow{}
	if err = accountJSON(ctx, base+"/wham/usage", token, map[string]string{"chatgpt-account-id": accountID}, &data); err != nil {
		return "", out, nil, "", false, err
	}
	if c := data.Credits; c != nil && c.Has && !c.Unlimited {
		if n, perr := strconv.ParseFloat(strings.TrimSpace(c.Balance), 64); perr == nil && n > 0 {
			credits = fmt.Sprintf("%s credits", compactNumber(math.Round(n*100)/100))
		}
	}
	if data.Resets != nil {
		resets = codexResets(ctx, base, token, accountID, data.Resets.Available)
	}
	if data.RateLimit.Primary != nil {
		out = append(out, data.RateLimit.Primary.window())
	}
	if data.RateLimit.Secondary != nil {
		out = append(out, data.RateLimit.Secondary.window())
	}
	return data.PlanType, out, resets, credits, codexHeld(data), nil
}

// codexUsage is what /wham/usage tells of a ChatGPT account.
type codexUsage struct {
	PlanType string `json:"plan_type"`
	// as Codex's /status reads it: {"has_credits":true,
	// "unlimited":false,"balance":"1234.5"}
	Credits *struct {
		Has       bool   `json:"has_credits"`
		Unlimited bool   `json:"unlimited"`
		Balance   string `json:"balance"`
		// a workspace's spending past its allowance, still open (false)
		OverageReached *bool `json:"overage_limit_reached"`
	} `json:"credits"`
	RateLimit struct {
		// false once its allowance is used up; held only with no credits
		// to go on with (codexHeld)
		Allowed   *bool        `json:"allowed"`
		Primary   *codexWindow `json:"primary_window"`
		Secondary *codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	SpendControl *struct {
		Reached bool `json:"reached"`
	} `json:"spend_control"`
	// why the account is held: "rate_limit_reached", or a workspace's
	// "workspace_owner_usage_limit_reached" and the like
	ReachedType *struct {
		Type *string `json:"type"`
	} `json:"rate_limit_reached_type"`
	Resets *struct {
		Available int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
}

// codexWorkspacePlans are the plans of a ChatGPT workspace: every plan
// /wham/usage names (codex-rs codex-backend-openapi-models PlanType, and
// the Codex app's own plan_type switch for its usage banner, search
// "case`enterprise_cbp_trial`:" in ChatGPT.app 26.930.61225 app-initial)
// but guest, free, go, plus, pro, prolite and promax. A plan not listed
// isn't a workspace, so it is held on no credits.
var codexWorkspacePlans = []string{"free_workspace", "team", "self_serve_business_prolite", "self_serve_business_usage_based",
	"business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_trial", "enterprise_cbp_usage_based",
	"enterprise_cbp_view_only", "enterprise", "hc", "finserv", "law", "sci",
	"education", "edu", "edu_plus", "edu_pro", "quorum", "k12"}

// codexReservePlans are the workspace plans the Codex app's reserve
// experiment covers; under it the app holds them on no credits whatever
// their overage says.
var codexReservePlans = []string{"team", "self_serve_business_prolite"}

// codexHeld reports whether the Codex app (ChatGPT Desktop) holds its
// composer for the account, sending nothing at all, whichever model the
// turn is on, as the app itself decides it: the backend says the account
// isn't allowed now (rate_limit.allowed false; limit_reached alone isn't
// read), and it has no credits to go on with (has_credits or unlimited),
// and isn't a workspace one still within its overage (overage not
// reached, no spend cap, an ordinary rate_limit_reached). A window's
// percent never says it: a Pro account reads 100% on its week and is not
// allowed, while its credits carry every turn on (the app's composer
// stays open and the backend answers). Not said is not held.
//
// This is the composer's own gate in ChatGPT.app 26.930.61225, either of
// two checks: hP in app-primary (search "n.rate_limit?.allowed!==!1||Jpe(n)"),
// which lets credits through, then a workspace within its overage, and
// hardBlocked in app-initial (search "hardBlocked:r.rate_limit?.allowed===!1"),
// which lets credits through but no overage, for the reserve experiment's
// plans. Tht (search "spend_control?.reached||e.credits?.overage_limit_reached"),
// which reads a spend cap before credits, is not the gate: it only tells a
// poller when to read usage again.
//
// Where the app goes by what usage can't show (the reserve experiment's
// gate, the reserve it may send on, a reset redeemed in the app), this
// says held: wrongly held costs the app its ChatGPT extras while magpie
// serves the turn, wrongly not held leaves it sending nothing at all.
func codexHeld(u codexUsage) bool {
	if u.RateLimit.Allowed == nil || *u.RateLimit.Allowed {
		return false
	}
	c := u.Credits
	if c != nil && (c.Has || c.Unlimited) {
		return false
	}
	spent := u.SpendControl != nil && u.SpendControl.Reached
	why := u.ReachedType != nil && u.ReachedType.Type != nil && *u.ReachedType.Type != "rate_limit_reached"
	plan := strings.ToLower(u.PlanType)
	workspace := slices.Contains(codexWorkspacePlans, plan) && !slices.Contains(codexReservePlans, plan)
	if workspace && c != nil && c.OverageReached != nil && !*c.OverageReached && !spent && !why {
		return false
	}
	return true
}

type codexWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	LimitWindowSecs int64   `json:"limit_window_seconds"`
	ResetAt         int64   `json:"reset_at"`
	ResetAfterSecs  int64   `json:"reset_after_seconds"`
}

func quotaDurationName(seconds int64) string {
	if seconds > 0 && seconds%(24*60*60) == 0 {
		return fmt.Sprintf("%d days", seconds/(24*60*60))
	}
	if seconds > 0 && seconds%(60*60) == 0 {
		return fmt.Sprintf("%d hours", seconds/(60*60))
	}
	return "Allowance"
}

func (w codexWindow) window() QuotaWindow {
	out := QuotaWindow{Name: quotaDurationName(w.LimitWindowSecs), Used: w.UsedPercent, ResetSecs: w.ResetAfterSecs,
		Span: time.Duration(w.LimitWindowSecs) * time.Second}
	if w.ResetAt > 0 {
		t := time.Unix(w.ResetAt, 0)
		out.ResetsAt = &t
	}
	return out
}

func copilotSubscriptionUsage(ctx context.Context, githubToken, host string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "copilot", Name: "Copilot", Icon: "githubcopilot", Windows: []QuotaWindow{}}
	var data struct {
		copilotEntitlement
		Snapshots map[string]copilotQuotaWire `json:"quota_snapshots"`
		Reset     string                      `json:"quota_reset_date_utc"`
		ResetDay  string                      `json:"quota_reset_date"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotUserURL(host), nil)
	if err == nil {
		req.Header.Set("Authorization", "token "+githubToken)
		req.Header.Set("Accept", "application/json")
		for k, v := range copilotHeaders {
			req.Header.Set(k, v)
		}
		var res *http.Response
		res, err = http.DefaultClient.Do(req)
		if err == nil {
			defer res.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				err = &accountStatusError{status: res.StatusCode}
			} else {
				err = json.Unmarshal(b, &data)
			}
		}
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan, q.AccessSKU = data.label(), data.AccessSKU
	// the allowances renew with the month, on the day GitHub says
	var unlimited []QuotaWindow
	var resets *time.Time
	if t, err := time.Parse(time.RFC3339, data.Reset); err == nil {
		resets = &t
	} else if t, err := time.Parse("2006-01-02", data.ResetDay); err == nil {
		resets = &t
	}
	// A Business or Enterprise seat's organization decides what happens
	// past its allowance, so using it up pauses the seat whatever
	// overage_permitted says, as VS Code's isChatQuotaExceeded has it.
	managed := strings.EqualFold(data.Plan, "business") || strings.EqualFold(data.Plan, "enterprise")
	for _, x := range []struct{ id, name string }{{"chat", "Chat requests"}, {"completions", "Completions"}, {"premium_interactions", "Premium requests"}} {
		w, ok := data.Snapshots[x.id]
		if !ok {
			continue
		}
		at := resets
		if sec, ok := w.ResetAt.value(); ok && sec > 0 {
			t := time.Unix(int64(sec), 0)
			at = &t
		}
		if w.Unlimited {
			// An organization's pooled premium allowance reads unlimited,
			// and has_quota false when the pool is spent (#1063): VS Code
			// pauses the seat then, so it is used up, not unlimited. Chat
			// and completions say has_quota false under token-based
			// billing whatever is left, and VS Code reads them unlimited.
			if x.id == "premium_interactions" && w.HasQuota != nil && !*w.HasQuota {
				q.Windows = append(q.Windows, QuotaWindow{Name: x.name, Used: 100, ResetsAt: at,
					Span: 30 * 24 * time.Hour, Aside: w.Overage && !managed})
				continue
			}
			unlimited = append(unlimited, QuotaWindow{Name: x.name, Unlimited: true, Display: "Unlimited", Aside: true})
			continue
		}
		// has_quota isn't whether there is an allowance: GitHub says false
		// for one used up, and under token-based billing for every one
		// (#1063). The entitlement says it; 0 is none, as VS Code reads it.
		ent, entOK := w.Entitlement.value()
		if entOK && ent <= 0 {
			continue
		}
		var used float64
		display := ""
		if rem, ok := w.Remaining.value(); entOK && ok {
			n := max(0, ent-rem)
			used = min(100, 100*n/ent)
			display = fmt.Sprintf("%s / %s", compactNumber(n), compactNumber(ent))
		} else if w.Percent != nil {
			used = min(100, max(0, 100-*w.Percent))
		} else {
			// nothing says how much is used: unknown, never unlimited
			continue
		}
		q.Windows = append(q.Windows, QuotaWindow{Name: x.name, Used: used, ResetsAt: at, Display: display,
			Span:  30 * 24 * time.Hour,
			Aside: x.id == "completions" || x.id == "premium_interactions" && w.Overage && !managed})
	}
	q.Windows = append(q.Windows, unlimited...)
	return q
}

// copilotQuotaWire is one of /copilot_internal/user's quota_snapshots, as
// VS Code's IQuotaSnapshotData has it: entitlement and quota_remaining
// come as numbers or, under token-based billing, as strings ("3900").
type copilotQuotaWire struct {
	Unlimited   bool          `json:"unlimited"`
	HasQuota    *bool         `json:"has_quota"`
	Entitlement copilotNumber `json:"entitlement"`
	Remaining   copilotNumber `json:"quota_remaining"`
	Percent     *float64      `json:"percent_remaining"`
	Overage     bool          `json:"overage_permitted"`
	ResetAt     copilotNumber `json:"quota_reset_at"`
}

// copilotNumber is a JSON number or a string of one; absent, null or
// unreadable is unknown.
type copilotNumber struct {
	n  float64
	ok bool
}

func (c *copilotNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if n, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
		*c = copilotNumber{n, true}
	}
	return nil
}

func (c copilotNumber) value() (float64, bool) { return c.n, c.ok }

func compactNumber(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d", int64(n))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", n), "0"), ".")
}

// Count is the window's own count as the GUI says it, beside its share:
// "355 / 500 credits" used, or what is left of it when left (#659); the
// vendor's Display when it doesn't count in amounts.
func (w QuotaWindow) Count(left bool) string {
	if w.Limit <= 0 {
		return w.Display
	}
	n := w.Amount
	if left {
		n = max(0, w.Limit-w.Amount)
	}
	return strings.TrimSpace(groupedNumber(n) + " / " + groupedNumber(w.Limit) + " " + w.Unit)
}

// groupedNumber is n with its thousands apart, "12,345.5", to two places.
func groupedNumber(n float64) string {
	s := compactNumber(math.Round(n*100) / 100)
	whole, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(whole, "-")
	whole = strings.TrimPrefix(whole, "-")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if neg {
		whole = "-" + whole
	}
	if frac != "" {
		return whole + "." + frac
	}
	return whole
}
