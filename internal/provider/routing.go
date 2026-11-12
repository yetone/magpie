package provider

// Routing: how the gateway spreads requests over the keys or accounts a
// provider has on (see Provider.Routing). Choosing by use needs to know the
// use without waiting for it, so a subscription's allowance is read from
// what was fetched last, and fetched again behind the request when it has
// gone stale — or when an account says it has run out.

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// The routings besides the default, smart, in order.
const (
	Ordered   = "order"
	Rotate    = "rotate"
	LeastUsed = "usage"
	Pace      = "pace"
	// Weighted spreads a provider's requests over its keys by each key's
	// weight (KeyAccount.Weight, #841), or a subscription's over its
	// accounts by each account's (AccountWeights), smoothly: 3 and 1 go
	// a, a, b, a…
	Weighted = "weight"
	// FreeFirst spends what costs nothing first: the models its vendor
	// serves at no cost to the subscription's allowance or credits, before
	// the ones that spend it (routes.FreeFirst).
	FreeFirst = "free"
	// Fastest goes by speed: the one that has answered the model fastest
	// lately — first token, then how fast it wrote — first; one not
	// measured lately gets a turn now and then to be (routes.Fastest).
	Fastest = "fast"
	// Cheapest goes by price: the member whose model costs least at the
	// request's time first — its vendor's peak hours and the provider's
	// rate windows read then — those alike in their listed order
	// (routes.Cheapest). A group's only: a provider's keys and accounts
	// all bill its one price.
	Cheapest = "cheap"
	// Drain stays on one key or account until it is used up, then goes
	// on to the next in the listed order, and stays on that one: one
	// before it that renews meanwhile doesn't take its requests back
	// (dzxx0496 on Discord; routes.Drain).
	Drain = "drain"
	// FreeExpiring spends free quota before paid, the free quota that is
	// lost soonest first: a free account (FreePlan, or a model that costs
	// nothing there, FreeModel) at its next reset or its credits' expiry,
	// then the paid ones as Smart orders them, and the used up last
	// (routes.FreeExpiring; wowbbs on Discord: five check-in accounts of
	// WorkBuddy, MiniMax, Trae and Qoder in one group). A group's only.
	FreeExpiring = "expiring"
)

// FreePlan says whether an account's plan, as its vendor or plugin names
// it, is a free one: "Free", "Free plan", a trial. FreeExpiring spends
// such an account's quota before a paid one's.
func FreePlan(plan string) bool {
	p := strings.ToLower(plan)
	for _, w := range []string{"free", "trial", "免费", "试用", "體驗", "体验"} {
		if strings.Contains(p, w) {
			return true
		}
	}
	return false
}

// pluginRouting is a routing a plugin serves: "<plugin>/<name>".
var pluginRouting = lazyre.New(`^[A-Za-z0-9._-]+/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// PluginRouting says whether routing is one a plugin serves,
// "<plugin>/<name>" (a JS gateway plugin's strategies). providers.json
// keeps one as saved, served now or not: one no plugin serves is tried in
// its listed order.
func PluginRouting(routing string) bool { return pluginRouting.MatchString(routing) }

// SetRouting changes how a provider's requests spread over its keys or
// accounts.
func SetRouting(id, routing string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Routing = routing
	return Save(*p)
}

// SetSink sends a provider's keys or accounts rate limited with quota
// left to the back of its order, or lets them keep their place
// (Provider.Sink).
func SetSink(id string, sink bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Sink = sink
	return Save(*p)
}

// SetUseFirst puts a provider's keys or accounts ahead of the other
// providers' where Smart weighs them together, or back in Smart's own
// order (Provider.UseFirst).
func SetUseFirst(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.UseFirst = on
	return Save(*p)
}

// SetNoStandIn keeps another model's request off a provider, or lets it
// stand in for any again (Provider.NoStandIn).
func SetNoStandIn(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.NoStandIn = on
	return Save(*p)
}

// SetCapStop has a subscription answer 429 while every account is held at
// its usage cap, rather than go on to its fallbacks (Provider.CapStop), or
// go on to them again.
func SetCapStop(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.CapStop = on
	return Save(*p)
}

// SetCapCareful has a subscription's capped accounts read again near
// their cap and held while their usage can't be told
// (Provider.CapCareful), or go by the last reading again.
func SetCapCareful(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.CapCareful = on
	return Save(*p)
}

// SetCountFree has every model of a provider count as free for the
// routings that weigh it, or go by its vendor's word again
// (Provider.CountFree).
func SetCountFree(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.CountFree = on
	return Save(*p)
}

// SetKeepLogin keeps Codex or Claude Code signed in to the account the
// user made first (Provider.KeepLogin), or lets magpie move it on again.
// Either way it is no longer kept on one of the user's choosing
// (KeepLoginAs): it is signed in to the first again.
func SetKeepLogin(id string, keep bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil || p.Account.Agent != p.ID || !slices.Contains(switchedAgents, p.ID) {
		return fmt.Errorf("magpie doesn't sign %s in to another of its accounts", p.Name)
	}
	if keep && p.SwitchOnly {
		return errSwitchOnly(*p)
	}
	chosen := p.keepsLogin() && p.KeepLoginAs != ""
	p.KeepLogin, p.KeepLoginAs = keep, ""
	if err := Save(*p); err != nil {
		return err
	}
	if chosen {
		return signInToFirst(p.ID)
	}
	return nil
}

// SetSwitchOnly has magpie only move Codex's or Claude Code's own sign-in
// between its accounts (Provider.SwitchOnly): Codex's requests never sent
// through the gateway for account failover, Claude's accounts serving no
// request through the gateway (KeptToAgent). Off, both are as before.
func SetSwitchOnly(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil || p.Account.Agent != p.ID || p.ID != "codex" && p.ID != "claude" {
		return fmt.Errorf("magpie doesn't route %s's own requests for account failover", p.Name)
	}
	p.SwitchOnly = on
	return Save(*p)
}

// SetRestartApp has magpie restart the Codex app's app-server when it
// changes Codex's account by itself, or leave the app on its account till
// it is restarted (Provider.RestartApp).
func SetRestartApp(id string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.ID != "codex" || p.Account == nil || p.Account.Agent != p.ID {
		return fmt.Errorf("magpie restarts no app of %s's", p.Name)
	}
	p.RestartApp = on
	return Save(*p)
}

// codexRestartsApp is whether the user had magpie restart the Codex app's
// app-server for a change it makes by itself (Provider.RestartApp). While
// providers.json can't be read it is unknown, and the app is left be: a
// restart missed is the user's to make, one made shows as a crash.
var codexRestartsApp = func() bool {
	f, err := read()
	if err != nil {
		return false
	}
	for _, p := range f.Providers {
		if p.ID == "codex" {
			return p.RestartApp
		}
	}
	return false
}

// SwitchOnly reports whether magpie only moves agent's own sign-in between
// its accounts, sending none of its requests through the gateway for
// account failover (Provider.SwitchOnly). While providers.json can't be
// read the user's choice is unknown, and failover, magpie's own idea, is
// not taken up on it.
func SwitchOnly(agent string) bool {
	f, err := read()
	if err != nil {
		return true
	}
	for _, p := range f.Providers {
		if p.ID == agent {
			return p.SwitchOnly
		}
	}
	return false
}

// SetAffinity changes how long a provider's conversations stay with the key
// or account that answered them.
func SetAffinity(id, affinity string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Affinity = affinity
	return Save(*p)
}

var usedCache struct {
	sync.Mutex
	m       map[string]map[string]Allowance // agent → user → allowance
	at      map[string]time.Time
	loading map[string]chan struct{} // closed when the fetch in flight is done
	// renewed: the generation when an account's windows were started
	// again. Read/reset order cannot use the clock: two events can have
	// the same timestamp, even with its monotonic part.
	renewed    map[string]uint64
	generation uint64
	// seen: each account's windows as last read, and when, by
	// agent/user, kept through a reading that failed, for the next to be
	// told renewed by (renewedFrom)
	seen map[string]reading
	// stale: made stale (StaleAllowance) while a reading was out, by
	// agent; that reading was asked before an account said it was out,
	// so what it tells is kept only until the next ask reads again
	stale map[string]bool
	// read: when each account's allowance was last read well, and whether
	// the last try failed, by agent/user (UsageRead)
	read map[string]readState
	// heard: accounts no reading has told the windows of (it failed, was
	// held, or named none) that a reply told them of, by agent/user
	// (NoteCodexLimits): known from that until a reading tells them
	heard map[string]heardOnly
}

// firstWait is how long a request waits for an agent's allowances the
// first time they are asked for, so the first requests after magpie
// starts are routed by them too; later ones never wait.
var firstWait = 3 * time.Second

// unwaited counts the callers under Unwaited.
var unwaited atomic.Int32

// Unwaited has Allowances answer from what is last known, without the
// first ask's wait, until release: for a caller that only asks whether a
// model is there, not which account a request goes to. Every agent's model
// list asks the gateway whether a model sees images, and the migrations at
// start read Pi's effort, which builds its list: each agent's first ask
// waited firstWait for its accounts' usage over the network, and magpie's
// gateway listened ~9 s after start.
func Unwaited() (release func()) {
	unwaited.Add(1)
	return sync.OnceFunc(func() { unwaited.Add(-1) })
}

// readsOut counts the allowance and usage reads a caller left running
// in the background (Allowances past firstWait, SubscriptionUsage with a
// stale copy to serve, a key's allowance), for a test to wait for at its
// end: what such a read finds — Claude Code's sign-in in whatever home is
// set by then — lands in the next test's caches otherwise (#3174).
var readsOut struct {
	sync.Mutex
	n    int
	idle chan struct{} // closed when n drops to 0
}

// goRead runs f as a read left out (readsOut).
func goRead(f func()) {
	readsOut.Lock()
	if readsOut.n == 0 {
		readsOut.idle = make(chan struct{})
	}
	readsOut.n++
	readsOut.Unlock()
	go func() {
		defer func() {
			readsOut.Lock()
			if readsOut.n--; readsOut.n == 0 {
				close(readsOut.idle)
			}
			readsOut.Unlock()
		}()
		f()
	}()
}

// Allowance is an account's allowance windows as last known.
type Allowance []Limit

// Limit is one window of an allowance.
type Limit struct {
	Used   float64       // share used, 0–100
	Resets time.Time     // zero when not known
	Span   time.Duration // how long the window runs; zero when not known
	Model  string        // the only models it counts, by a word in their ids
	// Amount of Of in Unit: the window's own count, used, when the vendor
	// counts it so (QuotaWindow.Amount)
	Amount, Of float64
	Unit       string
	matches    func(string) bool
	partial    bool   // of a reading that may leave windows out (QuotaWindow.partial)
	Name       string // QuotaWindow.Name: which window it is, one reading to the next (WindowCapID)
	// pool is the windows of the pool a model's own window draws on
	// (Antigravity's "Gemini · 7 days"), which hold the account at its
	// caps only (capLimits): routing goes on by the model's own window
	pool []Limit
	// aside is set on a window set aside for routing (QuotaWindow.Aside)
	// that a cap the user set on it while it counted still holds the
	// account at (WindowCaps.own); it rides in pool, never in the
	// allowance itself
	aside bool
	// ResetRunsOut is when the reset the account spends by itself before
	// it runs out does (resetRunsOut): spent then, it starts this window
	// again — at Restarts, which routing takes for the window's renewal
	// when it is the sooner. Zero when none will be spent so.
	ResetRunsOut time.Time
	// RunsOut is when the soonest of what the window has left is lost,
	// apart from its reset (QuotaWindow.RunsOutAt): zero when not known.
	RunsOut time.Time
	// RunsOutLeft is how much, in Unit, is lost at RunsOut
	// (QuotaWindow.RunsOut): zero when not told
	RunsOutLeft float64
	// Credits: the account goes on past this window on its credits
	// (QuotaWindow.Credits)
	Credits bool
	// Balance is how much is left in Unit, a wallet's reading with no
	// share (QuotaWindow.Balance); nil when not told
	Balance *float64
}

// OnCredits says whether the account goes on past its allowance on its
// credits, as its last reading says (a ChatGPT account holding them,
// QuotaWindow.Credits): a window used up doesn't stop it. Whether it is
// let spend them is the user's (CodexCredits).
func (a Allowance) OnCredits() bool {
	for _, l := range a {
		if l.Credits {
			return true
		}
	}
	return false
}

// WeekUsedUp says whether the allowance's week, a window of a day or more
// that counts every model, is used up at now, as its last reading says:
// weekUsedUp's rule (AutoUseCodexReset) on an allowance, credits or none.
func (a Allowance) WeekUsedUp(now time.Time) bool {
	for _, l := range a {
		if l.Span >= 24*time.Hour && l.Model == "" && l.matches == nil && l.Used >= 100 && l.Resets.After(now) {
			return true
		}
	}
	return false
}

// Restarts is when an auto-used reset starts the account's windows again,
// as the Codex reset about to run out is spent (expiringResetSpent):
// resetLead before it runs out, or now when the account is held up
// past then. What they have left then is lost, as at their own reset, so
// Use before expiry and Smart take it for their renewal when it comes sooner
// (#717, #718). Zero when none will be.
func (a Allowance) Restarts(now time.Time) time.Time {
	for _, l := range a {
		if !l.ResetRunsOut.IsZero() {
			stopped, back := a.heldUp()
			return expiringResetSpent(l.ResetRunsOut, now, stopped, back)
		}
	}
	return time.Time{}
}

// heldUp is usedUp and BackAt by an allowance: whether a window that
// stops the account, for every model, is used up, and when the last of
// those starts again (zero when one doesn't say).
func (a Allowance) heldUp() (stopped bool, back time.Time) {
	for _, l := range a {
		if l.Model != "" || l.matches != nil || l.Used < 100 {
			continue
		}
		if l.Resets.IsZero() {
			return true, time.Time{}
		}
		if !stopped || l.Resets.After(back) {
			back = l.Resets
		}
		stopped = true
	}
	return stopped, back
}

// restarted is when a window that renews at renews (zero: not known) is
// started again: at restart (Restarts) when that is the sooner.
func restarted(renews, restart time.Time) time.Time {
	if !restart.IsZero() && (renews.IsZero() || restart.Before(renews)) {
		return restart
	}
	return renews
}

func (l Limit) applies(model string) bool {
	return (l.Model == "" || strings.Contains(model, l.Model)) && (l.matches == nil || l.matches(model))
}

// For is what an allowance leaves a request for model at now: the share
// used of the fullest window that counts it, and when those windows renew,
// the biggest first — the longest, the week before the five hours in it.
// A window whose reset has passed is empty again, its next reset not known.
func (a Allowance) For(model string, now time.Time) (used float64, renews []time.Time) {
	model = strings.ToLower(model)
	var ls []Limit
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			l.Used, l.Resets = 0, time.Time{}
		}
		used = max(used, l.Used)
		ls = append(ls, l)
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Span != ls[j].Span {
			return ls[i].Span > ls[j].Span
		}
		return ls[i].Resets.After(ls[j].Resets)
	})
	for _, l := range ls {
		renews = append(renews, l.Resets)
	}
	return used, renews
}

// CapUsed is For's share over the windows that count model other than a
// budget known to run a day or longer: a rate cap (the five hours), or a
// window that doesn't say how long it runs. Smart and Use before expiry
// hold an account back at routes.LowShare of these only: a rate cap near
// full refuses the requests sent into it before the allowance is read
// again, while what a week has left is lost at its reset, so a week at
// 90% renewing today is spent first, not kept for last
// (ysicing_01858 on Discord). A window whose reset has passed is empty.
func (a Allowance) CapUsed(model string, now time.Time) float64 {
	model = strings.ToLower(model)
	used := 0.0
	for _, l := range a {
		if !l.applies(model) || l.Span >= budgetSpan {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			continue
		}
		used = max(used, l.Used)
	}
	return used
}

// Fullest is when the window For's share is of renews: the fullest that
// counts model, the later of those as full. A display pairs the two, so
// it names one window: a Factory account 32% into its week and 16% into
// its month is 32% used, renewing with the week, not the month (#1778).
// Zero when that window's reset isn't known, or has passed (it is empty
// again then, its next reset not known).
func (a Allowance) Fullest(model string, now time.Time) time.Time {
	model = strings.ToLower(model)
	used, at := -1.0, time.Time{}
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			l.Used, l.Resets = 0, time.Time{}
		}
		if l.Used > used || l.Used == used && l.Resets.After(at) {
			used, at = l.Used, l.Resets
		}
	}
	return at
}

// Count is the count of the window For's share is of — the fullest that
// counts model — when the vendor counts it (WorkBuddy's credits): how much
// of of is used, in unit; of is zero when it isn't counted so. A window
// whose reset has passed is empty again.
func (a Allowance) Count(model string, now time.Time) (amount, of float64, unit string) {
	model = strings.ToLower(model)
	used := -1.0
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			l.Used, l.Amount = 0, 0
		}
		if l.Used > used {
			used, amount, of, unit = l.Used, l.Amount, l.Of, l.Unit
		}
	}
	return amount, of, unit
}

// Renewal is For's renews as routing ranks them: a window not started,
// or whose reset has passed, that says how long it runs is taken to
// renew that long from now — a five hours not started renews within five
// hours, the soonest of an account that has nothing longer (#576). One
// whose credits left run out before it renews (Limit.RunsOut) renews
// then, as what is left is lost then all the same: WorkBuddy's accounts,
// whose credits have no reset, go by the package that ends first
// (cdredfox on X).
func (a Allowance) Renewal(model string, now time.Time) []time.Time {
	ls := a.renewing(model, now)
	out := make([]time.Time, len(ls))
	for i, l := range ls {
		out[i] = l.Resets
	}
	return out
}

// renewing is the windows that count model as Renewal ranks them, the
// biggest first, each Resets its renewal.
func (a Allowance) renewing(model string, now time.Time) []Limit {
	model = strings.ToLower(model)
	restart := a.Restarts(now)
	var ls []Limit
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.After(now) {
			l.Used, l.Amount = 0, 0 // its reset has passed: empty again
			l.Resets = time.Time{}
			if l.Span > 0 {
				l.Resets = now.Add(l.Span)
			}
		}
		if l.RunsOut.After(now) && (l.Resets.IsZero() || l.RunsOut.Before(l.Resets)) {
			l.Resets = l.RunsOut
		}
		l.Resets = restarted(l.Resets, restart)
		ls = append(ls, l)
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Span != ls[j].Span {
			return ls[i].Span > ls[j].Span
		}
		return ls[i].Resets.After(ls[j].Resets)
	})
	return ls
}

// Unreached is how many of Renewal's windows, the biggest first, Smart
// passes over, ranking the account by the next one: a window whose left
// the next shorter window can no longer let through before it renews,
// once that one renews, unless what that one has left is used first.
// Both are counted in one unit (Limit.Of): a Volcengine Agent Plan's
// month of 20,000 with 18,372 left, renewing two weeks after its week of
// 7,000 does, can take at most 14,000 after the week renews, so what the
// week has left is lost at its reset as a weekly plan's is, and it ranks
// by its week (#5639, louisgreen0726). While the weeks still to come can
// carry what the month has left, the month is the real cap, as when the
// month holds less than its weeks add up to (#1778), and it ranks by the
// month as before. A week partly before the month renews counts for the
// share of it that is. Only a budget window ranks so (budgetSpan): what
// five hours leave at their reset is never taken for lost, as
// everywhere in Smart. Windows that don't tell their counts, or tell
// them in different units, are never compared: 0.
func (a Allowance) Unreached(model string, now time.Time) int {
	ls := a.renewing(model, now)
	n := 0
	for ; n+1 < len(ls); n++ {
		big, next := ls[n], ls[n+1]
		if big.Of <= 0 || next.Of <= 0 || big.Unit != next.Unit || next.Span < budgetSpan || big.Span <= next.Span ||
			big.Resets.IsZero() || !next.Resets.Before(big.Resets) {
			break
		}
		left := big.Of * (100 - min(100, max(0, big.Used))) / 100
		after := next.Of * float64(big.Resets.Sub(next.Resets)) / float64(next.Span)
		if left <= after {
			break
		}
	}
	return n
}

// Left is how much a wallet that counts model has left, in unit
// (Limit.Balance): the least of its windows that tell a balance. ok is
// false when none does. Only balances in the same unit of one provider are
// compared (routes.Seat.Left).
func (a Allowance) Left(model string) (left float64, unit string, ok bool) {
	model = strings.ToLower(model)
	for _, l := range a {
		if l.Balance == nil || !l.applies(model) {
			continue
		}
		if !ok || *l.Balance < left {
			left, unit, ok = *l.Balance, l.Unit, true
		}
	}
	return left, unit, ok
}

// Lapse is the soonest moment credits left in a window that counts model
// are lost (Limit.RunsOut), zero when none is told or all have passed.
// Smart goes by it when the renewals tie: two WorkBuddy free accounts
// renew together, at the end of the month every one shares, but the
// credits one was given lapse before the other's, and spent first they
// aren't lost (zhaocai_83020 on Discord). Renewal already takes it when it
// comes before the window renews; this tells apart those that come after.
func (a Allowance) Lapse(model string, now time.Time) time.Time {
	model = strings.ToLower(model)
	var t time.Time
	for _, l := range a {
		if l.applies(model) && l.RunsOut.After(now) && (t.IsZero() || l.RunsOut.Before(t)) {
			t = l.RunsOut
		}
	}
	return t
}

// lapseHorizon is how far ahead credits about to be lost count as spent
// already: they weigh in full when they lapse now and fade to nothing a
// week out (Allowance.Lapsing).
const lapseHorizon = 7 * 24 * time.Hour

// Lapsing is the share of an allowance, in points of the 0–100 used share,
// that is lost soon whatever is done: what a window that counts model
// loses at RunsOut (RunsOutLeft of Of, never more than it has left), less
// the further off that is, to nothing at lapseHorizon. Credits that are
// lost unspent cost nothing to spend, so a ranking by the share used takes
// them off it, and an account whose month is half used but whose check-in
// credits lapse tomorrow is not kept behind a fresh one (kudi3699 on X).
// Zero when no window says how much lapses: the amount is never guessed
// from what is left. The most of any window's, as they overlap.
func (a Allowance) Lapsing(model string, now time.Time) float64 {
	model = strings.ToLower(model)
	best := 0.0
	for _, l := range a {
		if !l.applies(model) || !l.RunsOut.After(now) || l.RunsOutLeft <= 0 || l.Of <= 0 {
			continue
		}
		fade := 1 - float64(l.RunsOut.Sub(now))/float64(lapseHorizon)
		if fade <= 0 {
			continue
		}
		u := min(100, max(0, l.Used))
		best = max(best, fade*min(100*l.RunsOutLeft/l.Of, 100-u))
	}
	return best
}

// Full is when an account used up for model can take it again: the last
// reset of its windows that are full, zero when none is or it isn't known.
func (a Allowance) Full(model string, share float64, now time.Time) time.Time {
	model = strings.ToLower(model)
	var t time.Time
	for _, l := range a {
		if l.applies(model) && l.Used >= share && l.Resets.After(now) && l.Resets.After(t) {
			t = l.Resets
		}
	}
	return t
}

// Scoped says whether some window of the allowance counts some models only
// and counts model: Opus's or Fable's own week. A refusal of model with none
// of its windows read full may be that window's, read before it filled.
func (a Allowance) Scoped(model string) bool {
	model = strings.ToLower(model)
	return slices.ContainsFunc(a, func(l Limit) bool { return (l.Model != "" || l.matches != nil) && l.applies(model) })
}

// Pooled says whether a refusal of model for its allowance leaves the
// account's other models alone: the windows that count model and are full
// at share are each of some models only (Opus's own week, Cursor's Other
// Models pool) — or, none known full, no window of the whole account
// counts it, only pools. Then it is that model which is out, not the
// account: Cursor's Other Models used up leaves Auto and Composer in
// theirs (Xiaopodev on X).
func (a Allowance) Pooled(model string, share float64, now time.Time) bool {
	model = strings.ToLower(model)
	full, pooled, whole := false, false, false
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		scoped := l.Model != "" || l.matches != nil
		if l.Used >= share && (l.Resets.IsZero() || l.Resets.After(now)) {
			if !scoped {
				return false
			}
			full = true
		}
		pooled = pooled || scoped
		whole = whole || !scoped
	}
	if !pooled && !whole {
		// no window counts model, as Antigravity's Gemini models that
		// fetchAvailableModels gives no quota (#3737): when every window
		// the account has is some models' only, nothing is known to be
		// the whole account's, and the refusal is the model's
		return len(a) > 0 && !slices.ContainsFunc(a, func(l Limit) bool { return l.Model == "" && l.matches == nil })
	}
	return full || pooled && !whole
}

// budgetSpan is the shortest window that is a budget rather than a rate
// cap: the week (Kiro's month too), not the five hours in it — what the
// five hours leave at their reset is nothing lost.
const budgetSpan = 24 * time.Hour

// weekSpan is the budget window an account is taken to have when its
// vendor tells none a day or longer, or tells one without saying how long
// it runs or when it renews.
const weekSpan = 7 * 24 * time.Hour

// FreshPace is the pace of an account with a whole week ahead of it:
// what one not known counts as.
const FreshPace = 100 / (7 * 24.0)

// Pace is the share of its week an account has left for model, per hour
// until that week renews: the rate it would have to be used at to spend
// the week just in time, so the account with the highest has the most to
// lose at its reset. Of several budget windows that count the model
// (Opus's own beside the general), the tightest; due is when that one
// renews, zero when it isn't known. A window not started, or whose reset
// isn't known, is taken to run its whole span from now; one that doesn't
// say how long it runs (a plugin may not) is a budget, for as long as its
// reset says, else a week. The hours until a reset are never taken as
// fewer than one, or a window a minute from renewing would outweigh all
// the rest. An account whose vendor tells no budget window, only a short
// one (Claude Enterprise's five hours), has nothing longer to keep for:
// what its five hours have left is lost at their reset, so it goes by
// that, per hour until then — the whole span when not started (#576):
// ahead of nearly every week, behind only one with more of it to lose
// sooner. Where the reading may leave a week out (a Claude account only
// heard of as Claude Code answered, partial) it is instead taken to have
// a week not started with the share its fullest window has used: those
// go by what they have used, among the rest as the fresh are. An
// account whose windows an auto-used reset starts again sooner than they
// renew (Restarts) goes by that instead, and due is then that time: what
// it has left is lost there. Credits in a budget window that expire
// before it renews (Limit.RunsOut) are lost sooner still: the window goes
// by those that expire first, per hour until they do, when that is the
// more — what expires as a share of the window (RunsOutLeft of Of), or
// all it has left when the amount isn't told, as Renewal takes it. A
// WorkBuddy account whose check-in credits expire tomorrow goes ahead of
// one whose month has more left but nothing expiring (lcyksp on
// Discord); due is then the expiry.
func (a Allowance) Pace(model string, now time.Time) (pace float64, due time.Time) {
	model = strings.ToLower(model)
	restart := a.Restarts(now)
	// until is how long a window runs from now and renews when; a restart
	// sooner cuts it short
	cut := func(until time.Duration, renews time.Time) (time.Duration, time.Time) {
		if !restart.IsZero() && restart.Before(now.Add(until)) {
			return restart.Sub(now), restart
		}
		return until, renews
	}
	any, used := false, 0.0
	short, partial, shortPace, shortDue := false, false, 0.0, time.Time{}
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		partial = partial || l.partial
		u := min(100, max(0, l.Used))
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			u = 0 // its reset has passed: empty again
		}
		if l.Span != 0 && l.Span < budgetSpan {
			used = max(used, u)
			until, renews := l.Span, time.Time{}
			if l.Resets.After(now) {
				until, renews = l.Resets.Sub(now), l.Resets
			}
			until, renews = cut(until, renews)
			if p := (100 - u) / max(until, time.Hour).Hours(); !short || p < shortPace {
				shortPace, shortDue, short = p, renews, true
			}
			continue
		}
		until, renews := l.Span, time.Time{}
		if until == 0 {
			until = weekSpan
		}
		if l.Resets.After(now) {
			until, renews = l.Resets.Sub(now), l.Resets
		}
		until, renews = cut(until, renews)
		p := (100 - u) / max(until, time.Hour).Hours()
		if q, at, ok := l.expiring(u, now, now.Add(until)); ok && q > p {
			p, renews = q, at
		}
		if !any || p < pace {
			pace, due, any = p, renews, true
		}
	}
	if !any {
		if short && !partial {
			return shortPace, shortDue
		}
		return (100 - used) / weekSpan.Hours(), time.Time{}
	}
	return pace, due
}

// expiring is the pace of what a window loses at RunsOut, when that
// comes after now and before ends, the window's renewal: the share that
// expires (RunsOutLeft of Of, at most all), else all it has left (100-u),
// per hour until then, never fewer than one.
func (l Limit) expiring(u float64, now, ends time.Time) (pace float64, at time.Time, ok bool) {
	if !l.RunsOut.After(now) || !l.RunsOut.Before(ends) {
		return 0, time.Time{}, false
	}
	share := 100 - u
	if l.RunsOutLeft > 0 && l.Of > 0 {
		share = min(100, 100*l.RunsOutLeft/l.Of)
	}
	return share / max(l.RunsOut.Sub(now), time.Hour).Hours(), l.RunsOut, true
}

// Allowances is each of an agent's accounts' allowance by user, as last
// known, asked for again in the background when that was over a minute
// ago. Only the very first ask waits, and not for long. An account
// missing is one not known yet.
func Allowances(agent string) map[string]Allowance {
	c := &usedCache
	c.Lock()
	if c.m == nil {
		c.m, c.at, c.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
	}
	done := c.loading[agent]
	if done == nil && time.Since(c.at[agent]) > time.Minute {
		done = make(chan struct{})
		c.loading[agent] = done
		began := c.generation
		goRead(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			share := renewalShare(agent) // read before the lock: it reads providers.json
			all := map[string]Allowance{}
			readings, readAt, states := loginUsageAt(ctx, agent)
			for user, q := range readings {
				if q.Error != "" || len(q.Windows) == 0 {
					continue
				}
				if a, ok := routedAllowance(agent, user, q, time.Now()); ok {
					all[user] = a
				}
			}
			c.Lock()
			now := time.Now()
			// as old as its oldest reading: one the Usage page made 50s
			// ago, taken here, is read again in 10s, not kept a minute
			// more — near its cap an account was sent on a reading two
			// minutes old (#1295)
			at := readAt
			if c.seen == nil {
				c.seen = map[string]reading{}
			}
			if c.read == nil {
				c.read = map[string]readState{}
			}
			for user, st := range states {
				key := agent + "/" + strings.ToLower(user)
				if was := c.read[key].at; st.failed && was.After(st.at) {
					st.at = was // the last good one's time stands
				}
				c.read[key] = st
			}
			var renewed, restarts []string
			for user, a := range all {
				key := agent + "/" + strings.ToLower(user)
				if c.renewed[key] > began {
					// started again while this was read: not known till
					// it is read again, at once
					delete(all, user)
					at = time.Time{}
					continue
				}
				if was, ok := c.seen[key]; ok {
					if a.renewedFrom(was.a, was.at, share, now) {
						renewed = append(renewed, user)
					}
					if a.startedAgain(was.a, was.at, share, now) {
						restarts = append(restarts, user)
					}
				}
				c.seen[key] = reading{a, now}
			}
			// an account no reading told is known from what its replies
			// said, until one does: else Smart and Use before expiry kept
			// it, as not known, behind every account known, its week
			// running out unspent (peterpark8527 on Discord)
			for key, h := range c.heard {
				if !strings.HasPrefix(key, agent+"/") {
					continue
				}
				if slices.ContainsFunc(slices.Collect(maps.Keys(all)), func(u string) bool { return strings.EqualFold(u, h.user) }) {
					delete(c.heard, key)
				} else {
					all[h.user] = h.a
				}
			}
			if c.stale[agent] {
				// kept, as weigh counts an account not known unused, but
				// read again at the next ask
				delete(c.stale, agent)
				at = time.Time{}
			}
			c.m[agent], c.at[agent] = all, at
			delete(c.loading, agent)
			c.Unlock()
			// told once the lock is let go (a hook may ask for these
			// again), and before those waiting for them are let go
			for _, user := range renewed {
				tellRenewed(agent, user)
			}
			for _, user := range restarts {
				tellStarted(agent, user)
			}
			close(done)
		})
	}
	_, known := c.m[agent]
	c.Unlock()
	if !known && done != nil && unwaited.Load() == 0 {
		select {
		case <-done:
		case <-time.After(firstWait):
		}
	}
	c.Lock()
	m, known := c.m[agent]
	c.Unlock()
	if !known {
		// the first reading is still out: what each account said last
		return lastAllowances(agent)
	}
	return m
}

// UsageRead is when user's allowance under agent was last read well, and
// whether the last try to read it failed (or was held back); ok is false
// when it hasn't been tried since magpie started. A careful provider holds
// an account whose reading is too old or failed (Provider.CapCareful).
func UsageRead(agent, user string) (at time.Time, failed, ok bool) {
	c := &usedCache
	c.Lock()
	defer c.Unlock()
	st, ok := c.read[agent+"/"+strings.ToLower(user)]
	return st.at, st.failed, ok
}

// ReadUsageNow has agent's accounts users read again now, past the
// minute their readings are kept and though allowances are read only when
// asked, and waits, till ctx ends, for a reading begun after it asked. It
// says whether one came back (UsageRead then tells how it went).
func ReadUsageNow(ctx context.Context, agent string, users ...string) bool {
	for _, u := range users {
		StaleAllowance(agent, u)
	}
	c := &usedCache
	for range 3 {
		Allowances(agent)
		c.Lock()
		done, back := c.loading[agent], !c.at[agent].IsZero()
		c.Unlock()
		if done == nil {
			return back // read already, the first ask waiting for it; or nothing to read it with
		}
		select {
		case <-done:
		case <-ctx.Done():
			return false
		}
		c.Lock()
		back = !c.at[agent].IsZero() // zero: asked before it went stale
		c.Unlock()
		if back {
			return true
		}
	}
	return false
}

// OnRenewed has f told when an account's usage windows were started again
// (a Codex reset spent), or a reading of them (Allowances) finds one it
// was full in full no more, so what sat out waiting for them can come
// back; agent is the one its usage is read under (Account.UsageAgent).
// And, as agent "" and the key's KeyAllowanceID, when a reading of a key's
// own windows finds one it was full in full no more: its limit raised in
// its panel, or its usage reset.
func OnRenewed(f func(agent, user string)) {
	renewedHooks.Lock()
	renewedHooks.fs = append(renewedHooks.fs, f)
	renewedHooks.Unlock()
}

var renewedHooks struct {
	sync.Mutex
	fs []func(agent, user string)
}

// OnStarted has f told as OnRenewed is, and by the same tellings, but
// only when a window stands started again in fact: a reading finding one
// it was full in full no more with its reset gone by, or standing a new,
// later cycle, or a reset spent on them. What a vendor itself gave the
// time of waits for that, not for a reading that merely finds its window
// less than full while the vendor's own word stands (#2718).
func OnStarted(f func(agent, user string)) {
	startedHooks.Lock()
	startedHooks.fs = append(startedHooks.fs, f)
	startedHooks.Unlock()
}

var startedHooks struct {
	sync.Mutex
	fs []func(agent, user string)
}

// renewedNow tells those OnRenewed asked, and forgets the account's
// allowance as last read: its windows are not known till read again, and
// what holds an account at a share of them (a cap, credits not spent)
// holds it no more. Those OnStarted asked are told too: a reset spent is
// a window started again in fact.
func renewedNow(agent, user string) {
	forgetAllowance(agent, user)
	tellRenewed(agent, user)
	tellStarted(agent, user)
}

// tellRenewed tells those OnRenewed asked.
func tellRenewed(agent, user string) {
	renewedHooks.Lock()
	fs := renewedHooks.fs
	renewedHooks.Unlock()
	for _, f := range fs {
		f(agent, user)
	}
}

// tellStarted tells those OnStarted asked.
func tellStarted(agent, user string) {
	startedHooks.Lock()
	fs := startedHooks.fs
	startedHooks.Unlock()
	for _, f := range fs {
		f(agent, user)
	}
}

// AwaitAllowance has agent's accounts read again unless a reading is out,
// and waits, till ctx ends, for one that knows user: after a reset is
// spent, routing then counts the account's windows started again, rather
// than not known and behind the rest till a later request reads them
// (#1491). A reading out already, begun before the reset, doesn't count
// it, so another is asked for; three at most. It says whether user is
// known now.
func AwaitAllowance(ctx context.Context, agent, user string) bool {
	c := &usedCache
	knows := func() bool {
		c.Lock()
		defer c.Unlock()
		for u := range c.m[agent] {
			if strings.EqualFold(u, user) {
				return true
			}
		}
		return false
	}
	for range 3 {
		Allowances(agent)
		c.Lock()
		done := c.loading[agent]
		c.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return knows()
			}
		}
		if knows() {
			return true
		}
		if done == nil {
			return false // read lately, and user wasn't in it
		}
	}
	return knows()
}

// forgetAllowance leaves agent's account user out of the allowances last
// read, and has the next Allowances read them again.
func forgetAllowance(agent, user string) {
	c := &usedCache
	c.Lock()
	defer c.Unlock()
	if c.renewed == nil {
		c.renewed = map[string]uint64{}
	}
	c.generation++
	c.renewed[agent+"/"+strings.ToLower(user)] = c.generation
	delete(c.seen, agent+"/"+strings.ToLower(user)) // told already, by renewedNow
	if c.at != nil {
		c.at[agent] = time.Time{}
	}
	m, ok := c.m[agent]
	if !ok {
		return
	}
	// a new map: the one handed out is read without the lock
	kept := make(map[string]Allowance, len(m))
	for u, a := range m {
		if !strings.EqualFold(u, user) {
			kept[u] = a
		}
	}
	c.m[agent] = kept
}

// StaleAllowance makes the next Allowances ask the vendor again for user's
// allowance rather than trust what it last said, or what a reading out
// now, asked before, comes back with: the account just answered that it
// has run out.
func StaleAllowance(agent, user string) {
	key := agent + "/" + strings.ToLower(user)
	if readsAsked() {
		markStaleRead(agent, user) // read though allowances are read only when asked (#1518)
	}
	loginUsageCache.Lock()
	delete(loginUsageCache.m, key)
	delete(loginUsageCache.pending, key) // nor a reading asked for before
	loginUsageCache.Unlock()
	usedCache.Lock()
	if usedCache.at != nil {
		usedCache.at[agent] = time.Time{}
	}
	// a reading out now was asked before: read again once it is back
	if usedCache.loading[agent] != nil {
		if usedCache.stale == nil {
			usedCache.stale = map[string]bool{}
		}
		usedCache.stale[agent] = true
	}
	usedCache.Unlock()
}

// restartedBy marks every window as started again by a reset spent
// before it runs out at runsOut (Limit.ResetRunsOut); zero leaves a as it
// is.
func (a Allowance) restartedBy(runsOut time.Time) Allowance {
	for i := range a {
		a[i].ResetRunsOut = runsOut
	}
	return a
}

// routedAllowance is the allowance routing goes by from an account's
// reading. ok is false when it tells no share at all — every window its
// text or set aside, as a plugin's that doesn't say how much is used
// (#3650): the account is then not known, as one never read, rather than
// one with all of it left, ahead of the rest.
func routedAllowance(agent, user string, q SubscriptionQuota, now time.Time) (Allowance, bool) {
	a := allowanceOf(q.Windows, now)
	if len(a) == 0 {
		return nil, false
	}
	return a.restartedBy(resetRunsOut(agent, user, q.Windows, q.Resets)), true
}

// allowanceOf keeps the windows that can stop an account.
func allowanceOf(ws []QuotaWindow, now time.Time) Allowance {
	// Antigravity keeps the vendor's ids in its windows (and on disk),
	// while requests can name the collapsed model. Use the same families
	// as the picker, without needing the live catalog to have been saved.
	var raw []catalog.Model
	for _, w := range ws {
		if w.Family != "" && w.Model != "" && !w.Aside {
			raw = append(raw, catalog.Model{ID: w.Model})
		}
	}
	families := map[string]map[string]bool{}
	for _, f := range antigravityFamilies(raw) {
		if len(f.variants) > 1 {
			ids := map[string]bool{f.id: true}
			for _, v := range f.variants {
				ids[v.ID] = true
			}
			for _, v := range f.variants {
				families[v.ID] = ids
			}
		}
	}
	resets := func(w QuotaWindow) time.Time {
		switch {
		case w.ResetsAt != nil:
			return *w.ResetsAt
		case w.ResetSecs > 0:
			return now.Add(time.Duration(w.ResetSecs) * time.Second)
		}
		return time.Time{}
	}
	var a Allowance
	at := map[string]int{} // a model's own window, by its model
	for _, w := range ws {
		if w.Aside || w.Text {
			continue
		}
		l := Limit{Used: w.Used, Span: w.Span, Model: w.Model, Amount: w.Amount, Of: w.Limit, Unit: w.Unit, matches: w.matches, partial: w.partial, Name: w.Name, Credits: w.Credits, Resets: resets(w)}
		if ids := families[w.Model]; ids != nil && w.Family != "" && w.matches == nil {
			l.Model = ""
			l.matches = func(model string) bool { return ids[model] }
		}
		if w.Family != "" && w.Model != "" {
			at[w.Model] = len(a)
		}
		if w.RunsOutAt != nil {
			l.RunsOut, l.RunsOutLeft = *w.RunsOutAt, w.RunsOut
		}
		if w.Balance != nil {
			// a wallet is known by its balance, with no share invented:
			// one with nothing left is used up, as the vendor refuses it
			b := *w.Balance
			l.Balance = &b
			if b <= 0 {
				l.Used = max(l.Used, 100)
			}
		}
		a = append(a, l)
	}
	// a pool's own windows, set aside for routing, hold its models at
	// their caps (pluviophile714 on Discord), under their LineName
	for _, w := range ws {
		if !w.PoolWindow() || w.Text {
			continue
		}
		pl := Limit{Used: w.Used, Span: w.Span, Name: w.LineName(), Resets: resets(w)}
		for _, m := range PoolMembers(ws, w) {
			if i, ok := at[m.Model]; ok {
				a[i].pool = append(a[i].pool, pl)
			}
		}
	}
	// a window set aside since the user capped it (WorkBuddy's "Credits"
	// once the plan's and the extra credits are counted together, ferock
	// on Discord) holds at that cap still: the user's value outlives the
	// plugin's change of which window stops the account
	for _, w := range ws {
		if !w.Aside || w.Text || w.Model != "" || w.PoolWindow() {
			continue
		}
		al := Limit{Used: w.Used, Span: w.Span, Name: w.Name, Resets: resets(w), aside: true}
		for i := range a {
			a[i].pool = append(a[i].pool, al)
		}
	}
	return a
}

// capLimits are the windows of a that count model, each with the windows
// of the pool it draws on, which hold it at caps only.
func (a Allowance) capLimits(model string) []Limit {
	var out []Limit
	for _, l := range a {
		if l.applies(model) {
			out = append(append(out, l), l.pool...)
		}
	}
	return out
}
