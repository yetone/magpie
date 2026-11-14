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

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// QuotaWindow is one rolling allowance reported by a subscription provider.
type QuotaWindow struct {
	Unlimited bool       `json:"unlimited,omitempty"`
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	ResetSecs int64      `json:"resetSecs,omitempty"`
	Display   string     `json:"display,omitempty"`
	// Text is set on a window that is its Display alone, with no share
	// to tell: a plugin's row of what an account holds or has done
	// (AutoClaw's 积分余额 and 每日签到, Cursor's on-demand spend, ZCode's
	// gift to claim), which plugins send set aside with used 0 and no
	// count. Its Used is no reading, so it is told as its text, never as
	// "100% left". Claude's "Extra usage" row (claudeOveragesWindow) is
	// the one Text window with Used 100: the account counts as spent.
	Text bool `json:"text,omitempty"`
	// Lane names a window of an allowance apart from the account's own
	// ("images", ImageLane): its row is Text, and says only when it comes
	// back, if known. Added as cards are handed out (WithImageLane), never
	// cached.
	Lane string `json:"lane,omitempty"`
	// Amount of Limit is the window's own count in Unit, when the vendor
	// counts it so (WorkBuddy's credits): Used is Amount as a share of
	// Limit. The GUI says it as used or left, as it says the share (#659).
	Amount float64 `json:"amount,omitempty"`
	Limit  float64 `json:"limit,omitempty"`
	Unit   string  `json:"unit,omitempty"`
	// RunsOutAt is when the soonest of what is left is lost, and RunsOut
	// how much (in Unit), when it runs out apart from the window's reset:
	// WorkBuddy's credits come in packages that each end on their own
	// day. Smart routing takes it for the window's renewal (cdredfox on X).
	RunsOutAt *time.Time `json:"runsOutAt,omitempty"`
	RunsOut   float64    `json:"runsOut,omitempty"`
	// Balance is how much is left of the window in Unit, when the vendor
	// tells only that: a prepaid wallet, with no total to take a share of
	// (SuperMagic's points, #3950). Used is then no reading (0); the GUI
	// says the figure left, and routing knows the account by it and puts
	// the one with the most left first among a provider's accounts in the
	// same unit (Allowance.Left). nil when not told. On an account with a
	// usage cap, Amount of Limit and Used are what the wallet spent in the
	// cap's period instead (wallet_spend.go, #5770), for the cap to hold.
	Balance *float64 `json:"balance,omitempty"`
	// Spent is, on a wallet's window (Balance), how much of it magpie saw
	// go since SpentSince, the first reading of the period a cap counts it
	// over: the window's own until its reset, else the local day
	// (wallet_spend.go). nil when not counted.
	Spent      *float64   `json:"spent,omitempty"`
	SpentSince *time.Time `json:"spentSince,omitempty"`
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
	// Credits is set on each window of a ChatGPT account that holds
	// credits (has_credits with a balance left, or unlimited): using the
	// window up doesn't stop it, the vendor spends the credits instead
	// (codexGoesOnCredits); and on each of a Kimi Code key's while its
	// booster holds money (kimiBooster). Routing reads it
	// (Allowance.OnCredits); kept
	// with the reading, so one shown from before still says it.
	Credits bool `json:"credits,omitempty"`
	// Extra is set on a Claude account's "Usage credits" window
	// (claudeExtraWindow): the extra usage Anthropic bills past the plan,
	// set aside. A cap the user sets on it holds the account at 100% of a
	// plan window once reached (Allowance.extraCapped), never outright.
	Extra bool `json:"extra,omitempty"`
	// Capped is set, for the GUI only (WithCapped), on a window an
	// account's usage cap counts: neither Aside nor unlimited. One model's
	// own counts for that model (CapHeld), so it is marked like the rest.
	Capped bool `json:"capped,omitempty"`
	// CapsSome is set beside Capped on a window that counts some models
	// only — one model's own, or a pool's — so the cap holds the account
	// for those alone, and the GUI says which.
	CapsSome bool `json:"capsSome,omitempty"`
	// CapID is, beside Capped, how the window is known to a cap of its
	// own (WindowCapID), for the GUI to set and show one.
	CapID string `json:"capId,omitempty"`
	// OwnCap is set beside Capped on a window set aside: only a cap the
	// user set on it itself holds the account there (WindowCaps.own),
	// never the account's, and the GUI offers none new.
	OwnCap bool `json:"ownCap,omitempty"`
	// Holds is what the whole window holds, reckoned from magpie's own
	// calls through the account (usage.WithWindowHolds), for the GUI and,
	// through ReckonHolds, magpie quota --json and /v1/magpie/quotas
	// (#2333); nil where that can't be told honestly.
	Holds *WindowHolds `json:"holds,omitempty"`
	// HoldsWhy is, where Holds is nil on a window of an account's own card
	// that runs out and starts again, why its whole isn't told (#2045):
	// HoldsLow, HoldsNoClock, HoldsBeforeLog, HoldsNoneRouted or
	// HoldsCredits. Empty where no whole is ever told (a pool, a family,
	// money aside, another magpie's card).
	HoldsWhy string `json:"holdsWhy,omitempty"`
	// StartsAt is when the window began (Bounds), for the GUI only
	// (WithWindowStarts): with ResetsAt it places an even pace through the
	// window, which the card's Pace number is measured from (fflake33).
	StartsAt *time.Time `json:"startsAt,omitempty"`
	// TrayKind is, for the GUI only, how Settings names the window among
	// those the menu bar can show (gui.withTrayKinds): "5h", "7d",
	// "month", "7d:grok bot"; empty on one the menu bar can't say as a
	// percent.
	TrayKind string `json:"trayKind,omitempty"`
	// TrayExtra is, for the GUI only, set on a window with a TrayKind that
	// the menu bar shows only when picked (gui.trayChoices): money aside,
	// one model's window, Cursor's Grok Bot.
	TrayExtra bool `json:"trayExtra,omitempty"`
	// HeldBy is, for the GUI only (WithHeldWindows), the name of another
	// window of the account that is used up and stops it for every model
	// ("7 days"): what is left of this one can't be used until that one
	// starts again, so a sum over accounts counts it as nothing left
	// (#4791).
	HeldBy string `json:"heldBy,omitempty"`
	// matches further scopes pools whose membership isn't one model word.
	matches func(string) bool
	// partial is set on the windows of a reading that may leave some out:
	// a Claude account only heard of as Claude Code answered (its
	// rate_limit_event names one window at a time), never read whole by
	// /usage — five hours alone there needn't mean no week.
	partial bool
	// noShare is set on a window set aside whose used the plugin left
	// out: its Used is 0 for want of a share said, not a reading at 0, so
	// quota history keeps no line for it (#4825). The card still tells
	// the row's text and routing still sets it aside.
	noShare bool
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
	// BalanceSplit is what the Balance is made of, when the vendor tells
	// it: DeepSeek's granted credit and the money topped up, each a part
	// labelled SplitGranted or SplitTopUp
	BalanceSplit []BalancePart `json:"balanceSplit,omitempty"`
	// BalanceTrend is the Balance over time, as magpie read it, and when
	// it runs out at that pace (balance_history.go)
	BalanceTrend *BalanceTrend `json:"balanceTrend,omitempty"`
	// Estimate is set when the Balance is one the user typed less what
	// magpie priced the key's calls at since (key_balance_own.go), not a
	// vendor's reading
	Estimate *BalanceEstimate `json:"estimate,omitempty"`
	// SharedBalance is set on each key of a provider found to read one
	// account's balance with others (balance_shared.go): the name of the
	// first of them, whose card shows it once for all
	SharedBalance string `json:"sharedBalance,omitempty"`
	// Until is when the plan's paid time ends: it renews then when Renew
	// is "auto", is over when "off", and either when "" (the vendor
	// doesn't say which).
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	Error string     `json:"error,omitempty"`
	// PluginError is an Error a plugin's own usage read gave, in its own
	// words: magpie has no reading of it to say better, so the GUI shows
	// it on the card rather than only in a tooltip (aqiu817/magpie-sensenova#1).
	PluginError bool `json:"pluginError,omitempty"`
	// NeedsAccessKey is a plan read with the account's access key, which
	// its provider hasn't (accessKeyCard, #1821): the Usage page offers
	// the provider's settings to give it in.
	NeedsAccessKey bool `json:"needsAccessKey,omitempty"`
	// SeeAt is the vendor's page that shows what its API tells no key:
	// OpenCode Zen's credits, on its console (errOpenCodeNoBalance). The
	// Usage page offers to open it.
	SeeAt string `json:"seeAt,omitempty"`
	// AsOf is when an allowance shown in place of one that couldn't be
	// read was read (see keepLast); nil for a reading just made.
	AsOf *time.Time `json:"asOf,omitempty"`
	// ReadDeferred distinguishes a reading deliberately not requested from
	// a request that failed. AsOf and ReadAt still date the cached values.
	ReadDeferred bool `json:"readDeferred,omitempty"`
	// ReadAt is when what is shown was read: a key's balance, kept a
	// minute (KeyBalances), or an account's allowance, which a cache (a
	// minute's, or a Claude account's until Claude Code tells it again)
	// can hand back long after; nil when not known.
	ReadAt *time.Time `json:"readAt,omitempty"`
	// Resets are the rate-limit resets a Codex account holds, nil when it
	// holds none (codex_resets.go).
	Resets *ResetCredits `json:"resets,omitempty"`
	// ResetsUsed are the Codex account's resets magpie spent, newest
	// first, by itself or by hand (codex_resets_used.go): what the card
	// says when one is gone and the user asks where
	ResetsUsed []ResetUse `json:"resetsUsed,omitempty"`
	// Held is set on a ChatGPT account the Codex app sends nothing for
	// now (codexHeld): not on a window at 100% alone, which credits get
	// past.
	Held bool `json:"held,omitempty"`
	// Daily is the account's credits used day by day, as magpie counted
	// them from its readings (credits_daily.go): its first credits
	// window's, and MoreDaily its other pools', each apart.
	Daily     *DailyCredits   `json:"daily,omitempty"`
	MoreDaily []*DailyCredits `json:"moreDaily,omitempty"`
	// Checkins is set on a WorkBuddy (China) or Trae CN account whose
	// daily check-in is pressed for, CheckinBy whose ("" WorkBuddy's,
	// "trae" Trae CN's), and Checkin is how its last one went, nil before
	// the first; never cached, added as the page asks (WithCheckins).
	Checkins  bool              `json:"checkins,omitempty"`
	CheckinBy string            `json:"checkinBy,omitempty"`
	Checkin   *WorkBuddyCheckin `json:"checkin,omitempty"`
	// CheckinOff is set on such an account the user left out of its
	// vendor's check-in (settings.CheckinOff, #1902): it isn't checked in.
	CheckinOff bool `json:"checkinOff,omitempty"`
	// CheckinKey is such an account's entry in its vendor's check-in, the
	// key Check in now presses it by (CheckInAccount).
	CheckinKey string `json:"checkinKey,omitempty"`
	// Kind is the card's kind as another magpie is told it (CachedCards):
	// subscription, plan or balance; "" here.
	Kind string `json:"kind,omitempty"`
	// From is the remote magpie a card is that one's (remote_quotas.go),
	// by its name here; "" for this computer's own.
	From string `json:"from,omitempty"`
	// Via is the magpies (catalog.Self) a card told another magpie
	// passed through, the one that read it first, this one last
	// (CachedCards): the receiver leaves out a card that comes back to
	// it (catalog.Loops). Only between magpies; nil here.
	Via []string `json:"via,omitempty"`
	// LastServedAt is when the account, plan or key last answered a
	// request through this computer's gateway, nil when it hasn't in the
	// last 30 days (served.go); set by Quotas, never cached.
	LastServedAt *time.Time `json:"lastServedAt,omitempty"`
	// Alias is the account's name of the user's own (AccountNames), else
	// Seat; Seat is the name of the key plan this account's card stands
	// in for (seatCards, #1515): the same GLM seat added both as a key
	// and as a signed-in account. Both "" when none; set as cards are
	// handed out, never cached.
	Alias string `json:"alias,omitempty"`
	Seat  string `json:"seat,omitempty"`
	// Named is set when Alias is the user's own name (AccountNames), not
	// the plugin's name for the account (PluginUserOf, a masked phone) or
	// Seat: the GUI's Hide accounts keeps the user's own name in sight
	// and hides the others as accounts (#3656).
	Named bool `json:"named,omitempty"`
	// GatewayKey is set on the card of a gateway key a remote magpie gave
	// this computer (#1547, remote_key_limits.go): the limit that magpie
	// holds the key to, beside the windows that show it.
	GatewayKey *GatewayKeyLimit `json:"gatewayKey,omitempty"`
	// In-process read order, separate from the vendor's ReadAt and never
	// persisted: restarting starts a new sequence.
	readSeq uint64
	// wideBalance marks a balance that is the account's, not the key's
	// own (balanceAccountWide)
	wideBalance bool
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

// toldUsage tells OnSubscriptionUsage's hook that what Quotas answers has
// changed: a whole read landing, a card read again on its own, a Codex
// reset spent. The menu bar reads again then, not at its next tick
// minutes later, so it says what the panel does (#2357).
func toldUsage() {
	if f := onSubscriptionUsage.Load(); f != nil {
		(*f)()
	}
}

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
	if settings.AccountDiscoveryOff() {
		return nil
	}
	c := &subscriptionUsageCache
	c.Lock()
	have, fresh := c.data != nil, time.Since(c.at) < time.Minute
	held := heldRead(ctx)
	if held && have {
		// allowances read only when asked (#1518): the cards stand as
		// they were last read till the user asks again
		fresh = true
	}
	if c.asked && !held {
		have, c.asked = false, false
	}
	if !fresh && c.pending == nil {
		done := make(chan struct{})
		c.pending = done
		bg := context.Background()
		if wasAsked(ctx) {
			bg = Asked(bg)
		}
		readCtx, seq := quotaReading(bg)
		asked := claudeAskGeneration.Load()
		goRead(func() {
			out := fetchSubscriptionUsage(readCtx)
			noteDailyCredits(out, time.Now())
			noteCardHistory(out, time.Now())
			c.Lock()
			c.data = cacheCards(c.data, out, seq, false)
			c.at, c.pending = time.Now(), nil
			if claudeAskGeneration.Load() != asked {
				c.at = time.Time{} // asked meanwhile: read again
			}
			c.Unlock()
			// told before done is closed, so whoever waits for the refresh
			// has it finished, hook and all
			toldUsage()
			close(done)
		})
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
				chosen = keptExposedIDs("gemini", "antigravity")
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

// keptExposed is exposedIDs for Gemini's and Antigravity's quotas, kept
// while StampNow stays the same, and no longer than exposedFor. The tray
// reads usage every few minutes, and building it each time loaded the
// whole catalog (5.6 MB of a tray-only magpie's 12.8 MB live heap) before
// catalog's loadIdle could let it go.
var keptExposed struct {
	sync.Mutex
	ids   map[string]map[string]bool
	key   string
	stamp Stamp
	at    time.Time
}

// exposedFor bounds how long keptExposed is trusted across what a Stamp
// can't see: a model retired by its date, another app's account file.
const exposedFor = 30 * time.Minute

// exposedBuilds counts keptExposedIDs' builds, for a test.
var exposedBuilds atomic.Int64

// keptExposedIDs is exposedIDs(ids...), built again only after a change
// or for other ids. What it gives is shared: read only.
func keptExposedIDs(ids ...string) map[string]map[string]bool {
	key := strings.Join(ids, "\x00")
	now := StampNow()
	keptExposed.Lock()
	defer keptExposed.Unlock()
	if keptExposed.ids != nil && keptExposed.key == key && keptExposed.stamp == now && time.Since(keptExposed.at) < exposedFor {
		return keptExposed.ids
	}
	exposedBuilds.Add(1)
	built := exposedIDs(ids...)
	// a change while it was built: the next read builds it again
	if StampNow() == now {
		keptExposed.ids, keptExposed.key, keptExposed.stamp, keptExposed.at = built, key, now, time.Now()
	} else {
		keptExposed.ids = nil
	}
	return built
}

// exposedIDs is, for each provider of ids, the models magpie offers from
// it. Only those are built: every provider's whole list, each account's
// and plugin's, was built for the two whose quotas are narrowed, on each
// usage read (20 MB of a live magpie's idle two minutes).
func exposedIDs(ids ...string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range All() {
		if !slices.Contains(ids, p.ID) {
			continue
		}
		offered := map[string]bool{}
		for _, m := range p.Exposed() {
			offered[m.ID] = true
		}
		out[p.ID] = offered
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
	if settings.AccountDiscoveryOff() {
		return nil
	}
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
		if !PluginServes(id) {
			return false
		}
		placed[id] = true
		if !hidden[id] {
			fetches = append(fetches, pluginUsageFetchesOf(via, id)...)
		}
		return true
	}
	// what is switched off or removed is looked at no further: finding
	// the account asks its CLI who is signed in (leftAlone)
	if !hidden["claude"] {
		if p, ok := claudeAccount(); ok {
			// signed out, Claude Code's own allowance is none: the account
			// in its place is a saved one, read as the others are
			if ls := accountsOf("claude"); len(ls) > 1 || p.Account.standIn {
				fetches = append(fetches, perLogin(via("claude"), ls, accountCard("claude"))...)
			} else {
				fetches = append(fetches, withUser(ctx, p.Account.User, func() SubscriptionQuota {
					return claudeSubscriptionUsage(viaLogin("claude", p.Account.User), p.Account.User)
				}))
			}
		}
	}
	// each retired built-in is served by its plugin, whose cards stand
	// where the built-in's stood
	moved("cursor")
	moved("grok")
	if home, err := os.UserHomeDir(); err == nil {
		if p, ok := codexAccount(home); ok && !hidden["codex"] {
			if ls := accountsOf("codex"); len(ls) > 1 {
				fetches = append(fetches, perLogin(via("codex"), ls, accountCard("codex"))...)
			} else {
				dir := appdir.CodexHomeIn(home)
				fetches = append(fetches, withUser(ctx, p.Account.User, func() SubscriptionQuota { return codexSubscriptionUsage(viaLogin("codex", p.Account.User), dir) }))
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
	for _, id := range []string{"kiro", "zcode", wbCN.id, wbAI.id, CommandCodePlanID, "qoder", QoderCNID, "zed", "devin", "factory", MiMoID} {
		moved(id)
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
			fetches = append(fetches, func() SubscriptionQuota {
				q := loginReading(viaLogin(agent, l.User), l.Login).read
				if q.User == "" {
					q.User = l.User // as perLogin names it
				}
				return q
			})
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
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		e := &accountStatusError{status: res.StatusCode, body: b[:min(len(b), 4<<10)]}
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
	body       []byte        // the start of what it answered (accountJSON)
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
// told it: its /usage, run when the user asks (AskClaudeUsage, Asked), or
// what it said as it answered
// (NoteClaudeLimits). magpie never asks Anthropic for it itself; the Usage
// page's timer, the switch list and routing show what is kept here.
var claudeUsage struct {
	sync.Mutex
	m map[string]claudeUsageEntry
}

type claudeUsageEntry struct {
	at       time.Time
	ws       []QuotaWindow
	heard    time.Time     // when Claude Code last told it, answering
	heardSeq uint64        // that telling's place among the readings (quotaReadSequence)
	tried    time.Time     // when /usage was last run, answered or not
	asked    uint64        // the ask generation that run answered
	wait     time.Duration // how long after tried it runs again unasked
	err      error         // what the last run said, when it failed
	whole    bool          // /usage was read: ws has every window the account has
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

// claudeAskGeneration orders asks against readings, even when both happen
// in one wall-clock tick. tried and wait still schedule the next /usage.
var claudeAskGeneration atomic.Uint64

// claudeEveryAsked is the ask generation of the user's last refresh of
// every allowance at once (AskEveryClaudeUsage): a saved account read
// before it (claudeUsageEntry.asked) is read again, as its card's refresh
// would read it (#5380), by a read that starts within claudeEveryFor of
// the ask (claudeEveryAt): one the 30-second floor put off isn't run by a
// timed read long after.
var (
	claudeEveryAsked atomic.Uint64
	claudeEveryAt    atomic.Int64
)

const claudeEveryFor = 2 * time.Minute

func markClaudeUsageAsked() { markClaudeAsk(false) }

// markClaudeAsk is an ask of Claude's usage; every, the user's refresh of
// every allowance, whose saved accounts are read too.
func markClaudeAsk(every bool) {
	// Serialize the ask with both caches' snapshots and publications:
	// neither may observe the old generation, then publish after this ask.
	// When both locks are needed, the subscription cache comes first.
	subscriptionUsageCache.Lock()
	loginUsageCache.Lock()
	at := time.Now().UnixNano()
	claudeAsked.Store(at)
	if g := claudeAskGeneration.Add(1); every {
		claudeEveryAt.Store(at)
		claudeEveryAsked.Store(g)
	}
	loginUsageCache.Unlock()
	subscriptionUsageCache.Unlock()
}

// AskClaudeUsage is the user asking to see Claude's usage — opening the
// Usage page, refreshing it, `magpie quota` — so Claude Code's /usage is
// run at once rather than when the next unasked reading is due
// (claudeUsageWait); the next SubscriptionUsage waits for it.
func AskClaudeUsage() { askClaudeUsage(false) }

// AskEveryClaudeUsage is the user refreshing every allowance by hand: the
// tray panel's or the Usage page's Refresh, routing's read of the
// allowances, the TUI's r (#5380: the panel said "Allowances refreshed"
// while each saved Claude account kept its old reading, which only its
// own card's refresh read again). As AskClaudeUsage, and each saved
// Claude account's /usage is run too, as its card's refresh runs it, the
// 30-second floor and all. Opening the page, its timer and `magpie
// quota` ask with AskClaudeUsage, which starts no saved account's Claude
// Code (savedRefresh).
func AskEveryClaudeUsage() { askClaudeUsage(true) }

func askClaudeUsage(every bool) {
	markClaudeAsk(every)
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

// claudeWindows is the allowance of the Claude account user, read by
// Claude Code's own /usage. The account Claude Code is signed in to
// (active) is read when the user asked since it last was, when the last
// reading's claudeUsageWait (claudeNearOutWait near out) is up and Claude
// Code was used since, or when Claude Code was refused for its limit
// since. A saved account is read only when its card is refreshed
// (savedRefresh) or every allowance was refreshed by hand since it was
// read (AskEveryClaudeUsage), or when the switch off a spent account
// has no reading of it (spareRead), or when the keep-alive runs Claude
// Code on it (keepingAlive), by Claude Code run in the account's own
// config directory
// (readSavedClaudeUsage), Claude Code's own sign-in left as it is (#1630).
// Any other time it is what was kept.
// magpie itself never asks Anthropic.
func claudeWindows(ctx context.Context, user string, active bool) ([]QuotaWindow, error) {
	key := strings.ToLower(user)
	c := &claudeUsage
	now := time.Now()
	asked := claudeAskGeneration.Load()
	c.Lock()
	e, ok := c.m[key]
	c.Unlock()
	// the account Claude Code is on is read sooner near out
	// (claudeWaitOf), and at once once Claude Code was refused for its
	// limit since (claudeLimitHitAfter, #4443)
	due := e.tried.IsZero() || claudeAsked.Load() != 0 && asked > e.asked ||
		active && now.Sub(e.tried) >= claudeWaitOf(e, now) && (e.heard.After(e.tried) || claudeUsedSince(e.tried)) ||
		active && claudeLimitHitAfter(e.tried) ||
		// a first reading that failed (Claude Code not on a GUI's PATH, a
		// network blink) is tried again after the wait, as nothing is kept
		// to show and nobody need have used Claude Code since: a user who
		// never routes Claude through magpie would see "not read" for good
		active && e.ws == nil && e.err != nil && now.Sub(e.tried) >= e.wait
	// a saved account the switch off a spent one has no reading of
	// (spareRead): read once, and again only after a wait while its read
	// keeps failing, never every look
	spare := !active && spareRead(ctx) && e.ws == nil && (e.tried.IsZero() || now.Sub(e.tried) >= e.wait)
	// nor, with allowances read only when asked, one nobody asked for
	// and the keep-alive's run on a saved account (renewClaude), an hour
	// apart at the most, whose run is the renewal: not put off by the floor
	alive := keepAliveOf(ctx, user)
	if active {
		alive = nil
	}
	// every allowance refreshed by hand since this saved account's last
	// read (AskEveryClaudeUsage), as its own card's refresh would
	every := !active && claudeEveryAsked.Load() > e.asked && now.Sub(time.Unix(0, claudeEveryAt.Load())) < claudeEveryFor
	read := ((active || savedRefresh(ctx, user) || every) && due || spare || alive != nil) && (now.Sub(e.tried) >= claudeAskFloor || alive != nil) && !holding(ctx)
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
		e.asked = asked
		c.m[key] = e
	}
	c.Unlock()
	heard := e.ws != nil && now.Sub(e.heard) < claudeHeard
	if !read {
		switch {
		case ok && e.err != nil && !(heard && !claudeDenied(e.err.Error())):
			return []QuotaWindow{}, e.err
		case ok && e.ws != nil:
			return e.windows(now), nil
		case active:
			return []QuotaWindow{}, errClaudeNotAsked
		case claudeTokenAccount(user):
			// a refresh wouldn't read it either (readSavedClaudeUsage)
			return []QuotaWindow{}, errClaudeSetupToken
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
	if active && ClaudeCodeMovedOff(user) {
		return kept()
	}
	var ws []QuotaWindow
	var err error
	if active {
		ws, err = readClaudeUsage(ctx, "")
	} else {
		ws, err = readSavedClaudeUsage(ctx, user)
		if alive != nil {
			alive.done(err)
		}
	}
	if active && ClaudeCodeMovedOff(user) {
		return kept()
	}
	if err != nil {
		c.Lock()
		if f, ok := c.m[key]; ok {
			// A failed read cannot establish that an account refusal cleared.
			// A successful reading or a new header clears it instead.
			if f.err != nil && claudeDenied(f.err.Error()) && !claudeDenied(err.Error()) {
				err = f.err
			}
			if f.tried.Equal(now) {
				f.err = err
				c.m[key] = f
			}
			e = f // headers received while /usage ran are newer than e
		}
		c.Unlock()
		if e.ws != nil && time.Since(e.heard) < claudeHeard && !claudeDenied(err.Error()) {
			return e.windows(time.Now()), nil // a fresh header stands unless the account was refused
		}
		return ws, err
	}
	// a whole reading: a window it leaves out hasn't started
	ws = claudeWithUnstarted(ws)
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	c.m[key] = claudeUsageEntry{at: now, ws: ws, heard: e.heard, tried: now, asked: asked, wait: e.wait, whole: true}
	c.Unlock()
	// as of now, as a kept reading is: /usage gives a reset to the minute
	// and can lag it, so a window it still counts full may have started
	// again already
	return elapsed(ws, time.Now()), nil
}

var (
	errClaudeNotAsked = errors.New("not read yet: magpie reads Claude's usage by running Claude Code's /usage")
	errClaudeSaved    = errors.New("not read yet: refresh this account's card to have Claude Code's /usage read it")
)

// savedRefresh says ctx is the refresh of the card of the saved Claude
// account user, one ask that runs Claude Code for a saved account; the
// other is the user's refresh of every allowance (AskEveryClaudeUsage). The
// Usage page opened, its timer, `magpie quota` and routing keep to what
// was read: a run in every saved account's directory for each of them
// would start Claude Code once per account.
func savedRefresh(ctx context.Context, user string) bool {
	r, ok := refreshing(ctx)
	return ok && wasAsked(ctx) && r.wants("claude", user)
}

type spareReadKey struct{}

// readingSpare is ctx for the switch off a spent account (NextLogin)
// reading a saved Claude account it has nothing on: with Claude Code kept
// to its own sign-in (switch-only), no request of a saved account passes
// magpie to tell its allowance, and one never signed in to in Claude Code
// since had no reading at all, so the switch found no account with room
// and never moved (cn_Vincentzyx on X).
func readingSpare(ctx context.Context) context.Context {
	return context.WithValue(ctx, spareReadKey{}, true)
}

// spareRead says ctx is readingSpare's.
func spareRead(ctx context.Context) bool {
	b, _ := ctx.Value(spareReadKey{}).(bool)
	return b
}

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

func codexSubscriptionUsage(ctx context.Context, dir string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Windows: []QuotaWindow{}}
	if holding(ctx) {
		q.Error = errNotAsked.Error()
		return q
	}
	token, accountID, err := codexToken(ctx, dir)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	renew := func(ctx context.Context, stale string) (string, string, error) {
		return codexTokenRenewing(ctx, dir, stale)
	}
	q.Plan, q.Windows, q.Resets, q.Balance, q.Held, err = codexWindowsRenewing(ctx, token, accountID, renew)
	if b, _, rerr := readCodexLogin(dir, false); rerr == nil {
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
	return chatgptUntil(now, a.Tokens.IDToken)
}

// chatgptUntil is when the ChatGPT plan the first of tokens to name one
// is paid until (chatgpt_subscription_active_until), nil when none does
// or the time it names is gone by.
func chatgptUntil(now time.Time, tokens ...string) *time.Time {
	for _, tok := range tokens {
		s := claimString(jwtClaims(tok), "https://api.openai.com/auth", "chatgpt_subscription_active_until")
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || !t.After(now) {
			return nil
		}
		return &t
	}
	return nil
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
	if err = accountJSON(ctx, base+"/wham/usage", token, codexAccountHeaders(accountID), &data); err != nil {
		return "", out, nil, "", false, codexUsageWords(err)
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
	if codexGoesOnCredits(data) {
		for i := range out {
			out[i].Credits = true
		}
	}
	return data.PlanType, out, resets, credits, codexHeld(data), nil
}

// codexGoesOnCredits reports whether the ChatGPT account goes on past its
// allowance on credits, as the Codex app reads it (codexHeld): it has
// them (has_credits) or they are unlimited. A balance said and spent (0
// or less) is none left, whatever has_credits says.
func codexGoesOnCredits(u codexUsage) bool {
	c := u.Credits
	if c == nil {
		return false
	}
	if c.Unlimited {
		return true
	}
	if !c.Has {
		return false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(c.Balance), 64)
	return err != nil || n > 0
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
		Allowed *bool `json:"allowed"`
		// the backend's own word that a limit is hit
		LimitReached *bool        `json:"limit_reached"`
		Primary      *codexWindow `json:"primary_window"`
		Secondary    *codexWindow `json:"secondary_window"`
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
	if !codexAtLimit(u) {
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

// codexAtLimit is whether the reading says the allowance is used up as
// the Codex app's composer reads it: rate_limit.allowed false, and that
// alone. Not said is not at the limit, and neither is allowed true with a
// window at 100%: the app's gate returns open on anything but allowed
// false (ChatGPT.app 26.1029.61003, the macOS build of the version
// xiaoq6285 runs on Windows, async chunk "a6kU": search
// "n.rate_limit?.allowed!==!1||"; the same in OpenAI.Codex 26.1027 on the
// Windows box and in 26.1021), and its remaining-percent reading keeps a
// window of an allowed account at 1% or more (search
// "Math.max(100-e,+(!0===t))"), so to the app such an account still has
// room. Holding it would take the app out of ChatGPT (durable threads,
// remote control, its usage meter, #996) for no Send it would grey.
//
// A used-up 5h window with the week open is held through allowed: the
// backend says allowed false and limit_reached true there (the real Team
// reply in testdata, 5h at 100% and the week at 63%), which is what
// greys the app's Send.
func codexAtLimit(u codexUsage) bool {
	a := u.RateLimit.Allowed
	return a != nil && !*a
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
		res, err = Keyed(http.DefaultClient).Do(req)
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
