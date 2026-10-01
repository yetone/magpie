package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// QuotaWindow is one rolling allowance reported by a subscription provider.
type QuotaWindow struct {
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	ResetSecs int64      `json:"resetSecs,omitempty"`
	Display   string     `json:"display,omitempty"`
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
	// matches further scopes pools whose membership isn't one model word.
	matches func(string) bool
}

// SubscriptionQuota is provider-reported allowance usage. This is separate
// from Dial's local token log: vendors expose percentages, not token totals.
type SubscriptionQuota struct {
	Provider string        `json:"provider"`
	Name     string        `json:"name"`
	Icon     string        `json:"icon"`
	Plan     string        `json:"plan,omitempty"`
	User     string        `json:"user,omitempty"` // the account, so two of one vendor tell apart
	Windows  []QuotaWindow `json:"windows"`
	Balance  string        `json:"balance,omitempty"` // what is left on an API key, instead of windows
	// BalanceParts are the Balance's amounts each apart, when the balance
	// field the user wrote has several or a percent (cardParts)
	BalanceParts []BalancePart `json:"balanceParts,omitempty"`
	// Until is when the plan's paid time ends: it renews then when Renew
	// is "auto", is over when "off", and either when "" (the vendor
	// doesn't say which).
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	Error string     `json:"error,omitempty"`
	// AsOf is when an allowance shown in place of one that couldn't be
	// read was read (see keepLast); nil for a reading just made.
	AsOf *time.Time `json:"asOf,omitempty"`
	// ReadAt is when a key's balance was read, which the minute it is
	// kept for (KeyBalances) leaves behind the page's asking.
	ReadAt *time.Time `json:"readAt,omitempty"`
	// Resets are the rate-limit resets a Codex account holds, nil when it
	// holds none (codex_resets.go).
	Resets *ResetCredits `json:"resets,omitempty"`
}

var subscriptionUsageCache struct {
	sync.Mutex
	at      time.Time
	data    []SubscriptionQuota
	pending chan struct{} // closed when the refresh in flight is done
	asked   bool          // the user asked (AskClaudeUsage): wait for the refresh
}

// OnSubscriptionUsage is told when a refresh has landed, for what shows a
// stale copy meanwhile (the menu bar's text) to read the new one.
var OnSubscriptionUsage func()

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
		go func() {
			start := time.Now()
			out := fetchSubscriptionUsage()
			c.Lock()
			c.at, c.data, c.pending = time.Now(), out, nil
			if claudeAsked.Load() > start.UnixNano() {
				c.at = time.Time{} // asked meanwhile: read again
			}
			c.Unlock()
			close(done)
			if f := OnSubscriptionUsage; f != nil {
				f()
			}
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
	defer c.Unlock()
	return visibleQuotas(c.data)
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
func fetchSubscriptionUsage() []SubscriptionQuota {
	ctx0, cancel := context.WithTimeout(context.Background(), subscriptionTimeout)
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
			fetches = append(fetches, perLogin(via("claude"), ls, "Claude Code", "claude-color")...)
		} else {
			fetches = append(fetches, withUser(p.Account.User, func() SubscriptionQuota { return claudeSubscriptionUsage(viaLogin("claude", p.Account.User)) }))
		}
	}
	if user, plan, ok := cursorIdentity(); !moved("cursor") && ok && !hidden["cursor"] {
		fetches = append(fetches, withUser(user, func() SubscriptionQuota { return cursorSubscriptionUsage(viaLogin("cursor", user), plan) }))
	}
	if _, ok := grokAccount(); !moved("grok") && ok && !hidden["grok"] {
		fetches = append(fetches, func() SubscriptionQuota { return grokSubscriptionUsage(via("grok")) })
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p, ok := codexAccount(home); ok && !hidden["codex"] {
			if ls := accountsOf("codex"); len(ls) > 1 {
				fetches = append(fetches, perLogin(via("codex"), ls, "Codex", "codex-color")...)
			} else {
				auth := filepath.Join(home, ".codex", "auth.json")
				fetches = append(fetches, withUser(p.Account.User, func() SubscriptionQuota { return codexSubscriptionUsage(viaLogin("codex", p.Account.User), auth) }))
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
			fetches = append(fetches, perLogin(via("copilot"), ls, "Copilot", "githubcopilot")...)
		}
	}
	if moved("kiro") {
	} else if key := kiroKey(); key != "" && !hidden["kiro"] {
		fetches = append(fetches, func() SubscriptionQuota { return kiroQuotaAt(via("kiro"), key, "") })
	} else if !hidden["kiro"] {
		fetches = append(fetches, perLogin(via("kiro"), kiroLoginList(), "Kiro", "kiro-color")...)
	}
	if !moved("zcode") && !hidden["zcode"] {
		fetches = append(fetches, perLogin(via("zcode"), zcodeLoginList(), "ZCode", "zcode")...)
	}
	for _, w := range []*wbSite{wbCN, wbAI} {
		if !moved(w.id) && !hidden[w.id] {
			fetches = append(fetches, perLogin(via(w.id), wbLoginList(w), w.name, "workbuddy-color")...)
		}
	}
	if !moved(CommandCodePlanID) && !hidden[CommandCodePlanID] {
		fetches = append(fetches, perLogin(via(CommandCodePlanID), cmdLoginList(), "Command Code", "commandcode")...)
	}
	if !moved("qoder") && !hidden["qoder"] {
		fetches = append(fetches, perLogin(via("qoder"), loginsOf(qoderLogins()), "Qoder", "qoder")...)
	}
	if !moved(QoderCNID) && !hidden[QoderCNID] {
		fetches = append(fetches, perLogin(via(QoderCNID), loginsOf(qoderLoginsOf(QoderCNID)), "Qoder CN", "qoder")...)
	}
	if !moved("zed") && !hidden["zed"] {
		fetches = append(fetches, perLogin(via("zed"), zedLoginList(), "Zed", "zed")...)
	}
	if !moved("devin") && !hidden["devin"] {
		fetches = append(fetches, perLogin(via("devin"), devinLoginList(), "Devin", "devin")...)
	}
	if !moved("factory") && !hidden["factory"] {
		fetches = append(fetches, perLogin(via("factory"), factoryLoginList(), "Factory", "factory")...)
	}
	if !moved(MiMoID) && !hidden[MiMoID] {
		fetches = append(fetches, perLogin(via(MiMoID), mimoLoginList(), "Xiaomi MiMo", "mimocode")...)
	}
	for _, agent := range []string{"gemini", "antigravity"} {
		if hidden[agent] {
			continue
		}
		for _, l := range googleLogins(agent) {
			fetches = append(fetches, func() SubscriptionQuota { return l.acct.quota(viaLogin(agent, l.User), l.Plan) })
		}
	}
	fetches = append(fetches, pluginUsageFetches(via, hidden, placed)...)
	out := make([]SubscriptionQuota, len(fetches))
	var wg sync.WaitGroup
	for i, f := range fetches {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = keepLast(f(), "") }()
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

// withUser names the account a fetch is for.
func withUser(user string, f func() SubscriptionQuota) func() SubscriptionQuota {
	return func() SubscriptionQuota {
		q := f()
		q.User = user
		return q
	}
}

// perLogin fetches each account's allowance on a card of its own.
func perLogin(ctx context.Context, ls []Login, name, icon string) []func() SubscriptionQuota {
	var out []func() SubscriptionQuota
	for _, l := range ls {
		out = append(out, func() SubscriptionQuota {
			q := loginQuota(ctx, l)
			q.Name, q.Icon, q.User = name, icon, l.User
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

func claudeSubscriptionUsage(ctx context.Context) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Windows: []QuotaWindow{}}
	if _, _, ok := claudeCredential(); !ok {
		q.Error = "Claude Code is signed out; run claude auth login"
		return q
	}
	user, plan, _ := claudeIdentity()
	q.Plan = plan
	var err error
	q.Windows, err = claudeWindows(ctx, user, true)
	if err != nil {
		q.Error = err.Error()
	}
	return q
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
	c := &subscriptionUsageCache
	c.Lock()
	c.at, c.asked = time.Time{}, true
	c.Unlock()
	l := &loginUsageCache
	l.Lock()
	for k := range l.m {
		if strings.HasPrefix(k, "claude/") {
			delete(l.m, k)
		}
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
	if !read {
		switch {
		case ok && e.ws != nil:
			return elapsed(e.ws, now), nil
		case ok && e.err != nil:
			return []QuotaWindow{}, e.err
		case active:
			return []QuotaWindow{}, errClaudeNotAsked
		default:
			return []QuotaWindow{}, errClaudeSaved
		}
	}
	ws, err := readClaudeUsage(ctx)
	if err != nil {
		c.Lock()
		if f, ok := c.m[key]; ok && f.tried.Equal(now) {
			f.err = err
			c.m[key] = f
		}
		c.Unlock()
		if e.ws != nil && now.Sub(e.heard) < claudeHeard {
			return elapsed(e.ws, now), nil // what Claude Code said stands
		}
		return ws, err
	}
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	c.m[key] = claudeUsageEntry{at: now, ws: ws, heard: e.heard, tried: now, wait: e.wait}
	c.Unlock()
	return ws, nil
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

type quotaWire struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

func (w quotaWire) window(name string) QuotaWindow {
	out := QuotaWindow{Name: name, Used: w.Utilization}
	if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
		out.ResetsAt = &t
	}
	return out
}

func codexSubscriptionUsage(ctx context.Context, path string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Windows: []QuotaWindow{}}
	token, accountID, err := codexToken(ctx, path)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan, q.Windows, q.Resets, err = codexWindows(ctx, token, accountID)
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

// codexWindows is the plan, allowance and rate-limit resets of the
// ChatGPT account token signs in to.
func codexWindows(ctx context.Context, token, accountID string) (plan string, out []QuotaWindow, resets *ResetCredits, err error) {
	var data struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		Resets *struct {
			Available int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	base := strings.TrimSuffix(CodexBase, "/codex")
	out = []QuotaWindow{}
	if err = accountJSON(ctx, base+"/wham/usage", token, map[string]string{"chatgpt-account-id": accountID}, &data); err != nil {
		return "", out, nil, err
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
	return data.PlanType, out, resets, nil
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

func copilotSubscriptionUsage(ctx context.Context, githubToken string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "copilot", Name: "Copilot", Icon: "githubcopilot", Windows: []QuotaWindow{}}
	var data struct {
		Plan      string                      `json:"copilot_plan"`
		Snapshots map[string]copilotQuotaWire `json:"quota_snapshots"`
		Reset     string                      `json:"quota_reset_date_utc"`
		ResetDay  string                      `json:"quota_reset_date"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotUserURL, nil)
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
	q.Plan = data.Plan
	// the allowances renew with the month, on the day GitHub says
	var resets *time.Time
	if t, err := time.Parse(time.RFC3339, data.Reset); err == nil {
		resets = &t
	} else if t, err := time.Parse("2006-01-02", data.ResetDay); err == nil {
		resets = &t
	}
	for _, x := range []struct{ id, name string }{{"chat", "Chat requests"}, {"completions", "Completions"}, {"premium_interactions", "Premium requests"}} {
		w, ok := data.Snapshots[x.id]
		if !ok || !w.HasQuota || w.Entitlement <= 0 {
			continue
		}
		used := w.Entitlement - w.Remaining
		q.Windows = append(q.Windows, QuotaWindow{Name: x.name, Used: 100 * used / w.Entitlement, ResetsAt: resets,
			Display: fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(w.Entitlement)),
			Span:    30 * 24 * time.Hour, Aside: x.id == "completions"})
	}
	return q
}

type copilotQuotaWire struct {
	HasQuota    bool    `json:"has_quota"`
	Entitlement float64 `json:"entitlement"`
	Remaining   float64 `json:"quota_remaining"`
}

func compactNumber(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d", int64(n))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", n), "0"), ".")
}
