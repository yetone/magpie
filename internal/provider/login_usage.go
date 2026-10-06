package provider

// How much of its allowance each of an agent's accounts has used, the one
// the agent is signed in to and every saved one, so picking which to use
// next is a look at the list, not a guess.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

var loginUsageCache struct {
	sync.Mutex
	m       map[string]loginUsageEntry // agent/user
	pending map[string]*loginRead      // agent/user: being read now
}

type loginUsageEntry struct {
	at time.Time
	q  SubscriptionQuota
	// read is the reading itself, as the Usage page shows it: q is the
	// same, but where a hiccup kept what was known
	read SubscriptionQuota
}

// loginRead is an account's allowance being read: whoever asks for it
// meanwhile waits for it rather than asking the vendor again.
type loginRead struct {
	done chan struct{} // closed once e is in
	e    loginUsageEntry
}

// loginUsageFor, when set, stands in for LoginUsage's readings (tests),
// as UsageClaudeVia does for Claude's own.
var loginUsageFor func(ctx context.Context, agent string) map[string]SubscriptionQuota

// LoginUsageVia has tests stand in for LoginUsage's readings.
func LoginUsageVia(f func(ctx context.Context, agent string) map[string]SubscriptionQuota) {
	loginUsageFor = f
}

// LoginUsage is the allowance used by each of an agent's accounts, by
// user. What was read less than a minute ago, here or for the Usage page,
// comes from the cache; the rest is asked for at once, as long as ctx
// allows.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	if loginUsageFor != nil {
		return loginUsageFor(ctx, agent)
	}
	out := map[string]SubscriptionQuota{}
	if agent == "grok" {
		if _, ok := pluginOfAgent(agent); !ok {
			return grokLoginUsage(ctx)
		}
	}
	logins, ok := usageLogins(agent)
	if !ok {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := loginReading(ctx, l).q
			mu.Lock()
			out[l.User] = q
			mu.Unlock()
		}()
	}
	wg.Wait()
	usageRead(agent, out) // a window not started: the warm-up looks now
	return out
}

// loginReading is l's allowance as LoginUsage and the Usage page both show
// it, one reading for the two: what was read less than a minute ago comes
// from the cache, and an account being read is waited for, as long as ctx
// allows, rather than asked for again — the vendors' endpoints are
// rate limited, and two readings a moment apart told an account two ways.
func loginReading(ctx context.Context, l Login) loginUsageEntry {
	key := l.Agent + "/" + strings.ToLower(l.User)
	c := &loginUsageCache
	c.Lock()
	e, ok := c.m[key]
	if ok && time.Since(e.at) < time.Minute {
		c.Unlock()
		return e
	}
	entry := func(read SubscriptionQuota) loginUsageEntry {
		q := read
		if q.Error != "" && ok && q.Provider != "claude" {
			q = e.q // a hiccup keeps what was known
		}
		return loginUsageEntry{time.Now(), q, read}
	}
	r := c.pending[key]
	if r == nil {
		r = &loginRead{done: make(chan struct{})}
		if c.pending == nil {
			c.pending = map[string]*loginRead{}
		}
		c.pending[key] = r
		ctx, _ = quotaReading(ctx)
		go func() {
			start := time.Now()
			// read for all who wait for it: no one's ctx cuts it short, but
			// it is bounded as the Usage page's refresh is
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subscriptionTimeout)
			defer cancel()
			r.e = entry(keepReading(rctx, readNow(loginQuota(rctx, l)), l.User))
			c.Lock()
			// one dropped meanwhile (StaleAllowance) read too soon, and a
			// Claude account the user asked to see meanwhile is read again
			// (AskClaudeUsage), as SubscriptionUsage reads it: neither is kept
			if c.pending[key] == r {
				delete(c.pending, key)
				if l.Agent != "claude" || claudeAsked.Load() <= start.UnixNano() {
					if c.m == nil {
						c.m = map[string]loginUsageEntry{}
					}
					c.m[key] = r.e
				}
			}
			c.Unlock()
			close(r.done)
		}()
	}
	c.Unlock()
	select {
	case <-r.done:
		return r.e
	case <-ctx.Done():
		// given up on: as a reading cut short would have said
		return entry(keepLast(SubscriptionQuota{Provider: loginProvider(l), Plan: l.Plan, Windows: []QuotaWindow{}, Error: ctx.Err().Error()}, l.User))
	}
}

// usageLogins are the accounts whose allowance LoginUsage asks for, each
// read once a minute as its Agent: a plugin's accounts, asked for by the
// provider's id or as plugin:<id>, whichever names them, as plugin:<id>.
// False for an agent that tells none, and for the built-in Grok, read by
// home.
func usageLogins(agent string) (logins []Login, ok bool) {
	if pp, ok := pluginOfAgent(agent); ok {
		return pluginUsageLogins(pp), true
	}
	if agent == "grok" {
		return nil, false
	}
	return builtinLogins(agent)
}

// builtinLogins are the accounts of a built-in subscription whose
// allowance can be asked; false for an agent that tells none.
func builtinLogins(agent string) (logins []Login, ok bool) {
	switch agent {
	case "claude", "codex":
		logins = Logins(agent)
	case "copilot":
		logins = copilotLoginList()
	case "zcode":
		logins = zcodeLoginList()
	case "kiro":
		logins = kiroLoginList()
	case "workbuddy", WorkBuddyAIID:
		logins = wbLoginList(wbSiteOf(agent))
	case CommandCodePlanID:
		logins = cmdLoginList()
	case "qoder", QoderCNID:
		logins = loginsOf(qoderLoginsOf(agent))
	case "zed":
		logins = zedLoginList()
	case "devin":
		logins = devinLoginList()
	case "factory":
		logins = factoryLoginList()
	case MiMoID:
		logins = mimoLoginList()
	case ChatGPTAPIID:
		logins = siwcLoginList()
	case "gemini", "antigravity":
		logins = googleLoginList(agent)
	case "cursor": // one account, the one cursor-agent is signed in to
		if user, plan, ok := cursorIdentity(); ok {
			logins = []Login{{Agent: agent, User: user, Plan: plan, Active: true, On: true}}
		}
	default:
		return nil, false
	}
	return logins, true
}

// loginProvider is the id of the provider l signs in: a plugin's
// ("plugin:grok") is its provider's, grok for a moved Grok, whose proxy
// picks are kept under it.
func loginProvider(l Login) string {
	if id, ok := strings.CutPrefix(l.Agent, "plugin:"); ok {
		return PluginID(id)
	}
	return l.Agent
}

func loginQuota(ctx context.Context, l Login) SubscriptionQuota {
	ctx = ViaLogin(ctx, loginProvider(l), l.User) // asked through the account's own proxy
	if strings.HasPrefix(l.Agent, "plugin:") {
		return pluginLoginQuota(ctx, l)
	}
	if l.Agent == "qoder" || l.Agent == QoderCNID {
		return qoderLoginQuota(ctx, l)
	}
	if l.Agent == "zed" {
		return zedLoginQuota(ctx, l)
	}
	if l.Agent == "devin" {
		return devinLoginQuota(ctx, l)
	}
	if l.Agent == "factory" {
		return factoryLoginQuota(ctx, l)
	}
	if l.Agent == MiMoID {
		return mimoLoginQuota(ctx, l)
	}
	if l.Agent == "cursor" {
		return cursorSubscriptionUsage(ctx, l.Plan)
	}
	if l.Agent == "gemini" || l.Agent == "antigravity" {
		return googleLoginQuota(ctx, l)
	}
	if l.Agent == "zcode" {
		return zcodeLoginQuota(ctx, l)
	}
	if l.Agent == "kiro" {
		return kiroLoginQuota(ctx, l)
	}
	if w := wbSiteOf(l.Agent); w != nil {
		return wbLoginQuota(ctx, w, l)
	}
	if l.Agent == CommandCodePlanID {
		return cmdLoginQuota(ctx, l)
	}
	if l.Agent == "copilot" {
		for _, c := range copilotLogins(copilotConfigDir()) {
			if strings.EqualFold(c.User, l.User) {
				q := copilotSubscriptionUsage(ctx, c.app.Token, c.app.Host)
				if q.Error == "" {
					refreshCopilotEntitlement(c.app, q.Plan, q.AccessSKU)
				}
				return q
			}
		}
		return SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
	}
	q := SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}}
	if l.Agent == "claude" && !l.Active {
		// a saved account is never asked: what Claude Code told of it
		ws, err := claudeWindows(ctx, l.User, false)
		q.Windows = ws
		if err != nil {
			q.Error = err.Error()
		} else {
			q.ReadAt = claudeReadAt(l.User)
		}
		return q
	}
	var tok, accountID string
	var err error
	switch {
	case l.Agent == "claude": // Claude Code reads its own (claudeWindows)
	case l.Active:
		tok, accountID, err = codexToken(ctx, codexAuthPath())
	default:
		tok, accountID, err = savedLoginToken(ctx, l.Agent, l.User)
	}
	if err == nil {
		if l.Agent == "claude" {
			if q.Windows, err = claudeWindows(ctx, l.User, true); err == nil {
				q.ReadAt = claudeReadAt(l.User)
			}
		} else {
			var plan string
			if plan, q.Windows, q.Resets, q.Balance, q.Held, err = codexWindows(ctx, tok, accountID); plan != "" {
				q.Plan = plan
			}
			q.Until = codexUntil(codexLoginAuth(l), time.Now())
		}
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// CodexUsedUp reports whether the ChatGPT account Codex is signed in to is
// out for the Codex app, which then sends nothing for it (codexHeld): out
// of its allowance with no credits to go on with. A window at 100% with
// credits left isn't (the Codex app keeps sending, and the backend
// answers). False when that isn't known.
func CodexUsedUp(ctx context.Context) bool {
	// read as LoginUsage has it, fetched at most once a minute: the agent
	// package asks on every catalog sync
	u := LoginUsage(ctx, "codex")
	for _, l := range Logins("codex") {
		if q, ok := u[l.User]; l.Active && ok && q.Error == "" && q.Held {
			return true
		}
	}
	return false
}

// codexLoginAuth is the auth.json of a ChatGPT account magpie knows: the
// file Codex is signed in with, or the one kept for it.
func codexLoginAuth(l Login) []byte {
	if l.Active {
		b, _ := os.ReadFile(codexAuthPath())
		return b
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, x := range readLogins() {
		if x.Agent == "codex" && strings.EqualFold(x.User, l.User) {
			return x.Auth
		}
	}
	return nil
}
