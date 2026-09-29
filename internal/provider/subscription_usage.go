package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QuotaWindow is one rolling allowance reported by a subscription provider.
type QuotaWindow struct {
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	ResetSecs int64      `json:"resetSecs,omitempty"`
	Display   string     `json:"display,omitempty"`

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
	// Until is when the plan's paid time ends: it renews then when Renew
	// is "auto", is over when "off", and either when "" (the vendor
	// doesn't say which).
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	Error string     `json:"error,omitempty"`
	// AsOf is when an allowance shown in place of one that couldn't be
	// read was read (see keepLast); nil for a reading just made.
	AsOf *time.Time `json:"asOf,omitempty"`
	// Resets are the rate-limit resets a Codex account holds, nil when
	// it holds none (codex_resets.go).
	Resets *ResetCredits `json:"resets,omitempty"`
}

var subscriptionUsageCache struct {
	sync.Mutex
	at      time.Time
	data    []SubscriptionQuota
	pending chan struct{} // closed when the refresh in flight is done
}

// OnSubscriptionUsage is told when a refresh has landed, for what shows a
// stale copy meanwhile (the menu bar's text) to read the new one.
var OnSubscriptionUsage func()

// subscriptionTimeout bounds one refresh; the vendors' endpoints can be
// unreachable without a proxy, and then each fetch would hang to it.
var subscriptionTimeout = 10 * time.Second

// SubscriptionUsage returns rolling quotas for signed-in first-party agents.
// Results are cached because these private account endpoints are aggressively
// rate limited when several CLI sessions are active. Once there is something
// cached it comes back at once, and a stale copy is refreshed in the
// background; only the very first call waits, for as long as ctx allows.
func SubscriptionUsage(ctx context.Context) []SubscriptionQuota {
	c := &subscriptionUsageCache
	c.Lock()
	have, fresh := c.data != nil, time.Since(c.at) < time.Minute
	if !fresh && c.pending == nil {
		done := make(chan struct{})
		c.pending = done
		go func() {
			out := fetchSubscriptionUsage()
			c.Lock()
			c.at, c.data, c.pending = time.Now(), out, nil
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
	for _, w := range ws {
		m := w.Model
		if base != nil && m != "" {
			m = base(m)
		}
		if w.Model == "" || chosen[m] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return ws
	}
	return out
}

// fetchSubscriptionUsage asks every signed-in vendor at once.
func fetchSubscriptionUsage() []SubscriptionQuota {
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionTimeout)
	defer cancel()
	hidden := map[string]bool{}
	for _, p := range load().Providers {
		hidden[p.ID] = p.Hidden || p.Off // switched off: not asked either
	}
	var fetches []func() SubscriptionQuota
	if p, ok := claudeAccount(); ok && !hidden["claude"] {
		if ls := accountsOf("claude"); len(ls) > 1 {
			fetches = append(fetches, perLogin(ctx, ls, "Claude Code", "claude-color")...)
		} else {
			fetches = append(fetches, withUser(p.Account.User, func() SubscriptionQuota { return claudeSubscriptionUsage(ctx) }))
		}
	}
	if user, plan, ok := cursorIdentity(); ok && !hidden["cursor"] {
		fetches = append(fetches, withUser(user, func() SubscriptionQuota { return cursorSubscriptionUsage(ctx, plan) }))
	}
	if _, ok := grokAccount(); ok && !hidden["grok"] {
		fetches = append(fetches, func() SubscriptionQuota { return grokSubscriptionUsage(ctx) })
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p, ok := codexAccount(home); ok && !hidden["codex"] {
			if ls := accountsOf("codex"); len(ls) > 1 {
				fetches = append(fetches, perLogin(ctx, ls, "Codex", "codex-color")...)
			} else {
				auth := filepath.Join(home, ".codex", "auth.json")
				fetches = append(fetches, withUser(p.Account.User, func() SubscriptionQuota { return codexSubscriptionUsage(ctx, auth) }))
			}
		}
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		if app, ok := copilotLogin(cfg); ok && !hidden["copilot"] {
			if ls := copilotLoginList(); len(ls) > 1 {
				fetches = append(fetches, perLogin(ctx, ls, "Copilot", "githubcopilot")...)
			} else {
				fetches = append(fetches, withUser(app.User, func() SubscriptionQuota { return copilotSubscriptionUsage(ctx, app.Token) }))
			}
		}
	}
	if key := kiroKey(); key != "" && !hidden["kiro"] {
		fetches = append(fetches, func() SubscriptionQuota { return kiroQuotaAt(ctx, key, "") })
	} else if !hidden["kiro"] {
		fetches = append(fetches, perLogin(ctx, kiroLoginList(), "Kiro", "kiro-color")...)
	}
	if !hidden["zcode"] {
		fetches = append(fetches, perLogin(ctx, zcodeLoginList(), "ZCode", "zcode")...)
	}
	for _, w := range []*wbSite{wbCN, wbAI} {
		if !hidden[w.id] {
			fetches = append(fetches, perLogin(ctx, wbLoginList(w), w.name, "workbuddy-color")...)
		}
	}
	if !hidden[CommandCodePlanID] {
		fetches = append(fetches, perLogin(ctx, cmdLoginList(), "Command Code", "commandcode")...)
	}
	if !hidden["qoder"] {
		fetches = append(fetches, perLogin(ctx, loginsOf(qoderLogins()), "Qoder", "qoder")...)
	}
	for _, agent := range []string{"gemini", "antigravity"} {
		if hidden[agent] {
			continue
		}
		for _, l := range googleLogins(agent) {
			fetches = append(fetches, func() SubscriptionQuota { return l.acct.quota(ctx, l.Plan) })
		}
	}
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
	token, err := claudeToken(ctx)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	user, plan, _ := claudeIdentity()
	q.Plan = plan
	q.Windows, err = claudeWindows(ctx, user, token)
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// claudeUsage keeps each Claude account's allowance as last read, by user.
// Anthropic's usage endpoint turns away an account asked more than a few
// times in a while (429, rate_limit_error), and the Usage page, the switch
// list and routing all ask for it: they share what was read in the last
// few minutes, and one turned away waits as long as it is told, or five
// minutes, showing what was known until then.
var claudeUsage struct {
	sync.Mutex
	m map[string]claudeUsageEntry
}

type claudeUsageEntry struct {
	at, retry time.Time
	ws        []QuotaWindow
	heard     time.Time // when Claude Code last told it, answering
}

const claudeUsageTTL = 3 * time.Minute

// claudeWindows is the allowance of the Claude account user, token signs
// in to.
func claudeWindows(ctx context.Context, user, token string) ([]QuotaWindow, error) {
	key := strings.ToLower(user)
	c := &claudeUsage
	c.Lock()
	e, ok := c.m[key]
	c.Unlock()
	now := time.Now()
	if ok && e.ws != nil && (now.Sub(e.at) < claudeUsageTTL || now.Before(e.retry)) {
		return elapsed(e.ws, now), nil
	}
	if ok && now.Before(e.retry) {
		return []QuotaWindow{}, claudeLimited(e.retry.Sub(now))
	}
	ws, err := readClaudeWindows(ctx, token)
	var st *accountStatusError
	if errors.As(err, &st) && st.status == http.StatusTooManyRequests {
		wait := st.retryAfter
		if wait <= 0 {
			wait = 5 * time.Minute
		}
		c.Lock()
		if c.m == nil {
			c.m = map[string]claudeUsageEntry{}
		}
		e.retry = now.Add(wait)
		c.m[key] = e
		c.Unlock()
		if e.ws != nil {
			return elapsed(e.ws, now), nil
		}
		return []QuotaWindow{}, claudeLimited(wait)
	}
	if err != nil {
		if e.ws != nil && now.Sub(e.heard) < claudeHeard {
			return elapsed(e.ws, now), nil // what Claude Code said stands
		}
		return ws, err
	}
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	c.m[key] = claudeUsageEntry{at: now, ws: ws, heard: e.heard}
	c.Unlock()
	return ws, nil
}

// claudeLimited is Anthropic turning the usage endpoint away for wait.
func claudeLimited(wait time.Duration) error {
	return fmt.Errorf("Anthropic is rate limiting its usage endpoint; magpie asks again in %s", wait.Round(time.Minute).String())
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

// readClaudeWindows asks Anthropic for the allowance of the account token
// signs in to.
func readClaudeWindows(ctx context.Context, token string) ([]QuotaWindow, error) {
	var data struct {
		FiveHour       *quotaWire `json:"five_hour"`
		SevenDay       *quotaWire `json:"seven_day"`
		SevenDayOpus   *quotaWire `json:"seven_day_opus"`
		SevenDaySonnet *quotaWire `json:"seven_day_sonnet"`
		// a week's allowance per model (Fable), which the fields above
		// don't carry; one with no scope is seven_day again
		Limits []struct {
			Kind     string   `json:"kind"`
			Percent  *float64 `json:"percent"`
			ResetsAt string   `json:"resets_at"`
			Scope    *struct {
				Model *struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
		} `json:"limits"`
	}
	err := accountJSON(ctx, claudeBase+"/api/oauth/usage", token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20", "user-agent": "magpie",
	}, &data)
	if err != nil {
		return []QuotaWindow{}, err
	}
	out := []QuotaWindow{}
	const week = 7 * 24 * time.Hour
	for _, x := range []struct {
		name, model string
		span        time.Duration
		w           *quotaWire
	}{{"5 hours", "", 5 * time.Hour, data.FiveHour}, {"7 days", "", week, data.SevenDay},
		{"7 days · Opus", "opus", week, data.SevenDayOpus}, {"7 days · Sonnet", "sonnet", week, data.SevenDaySonnet}} {
		if x.w != nil {
			w := x.w.window(x.name)
			w.Span, w.Model = x.span, x.model
			out = append(out, w)
		}
	}
	for _, l := range data.Limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil || l.Percent == nil {
			continue
		}
		name := strings.TrimSpace(l.Scope.Model.DisplayName)
		model := claudeScopeModel(name)
		if model == "" || slices.ContainsFunc(out, func(w QuotaWindow) bool { return w.Model == model }) {
			continue
		}
		w := quotaWire{Utilization: *l.Percent, ResetsAt: l.ResetsAt}.window("7 days · " + name)
		w.Span, w.Model = week, model
		out = append(out, w)
	}
	return out, nil
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
