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
	m map[string]loginUsageEntry // agent/user
}

type loginUsageEntry struct {
	at time.Time
	q  SubscriptionQuota
}

// LoginUsage is the allowance used by each of an agent's accounts, by
// user. What was fetched less than a minute ago comes from the cache; the
// rest is asked for at once, as long as ctx allows.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	if agent == "grok" {
		if _, ok := pluginOfAgent(agent); !ok {
			return grokLoginUsage(ctx)
		}
	}
	logins, known, ok := usageLogins(agent)
	if !ok {
		return out
	}
	c := &loginUsageCache
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range logins {
		key := known + "/" + strings.ToLower(l.User)
		c.Lock()
		e, ok := c.m[key]
		c.Unlock()
		if ok && time.Since(e.at) < time.Minute {
			mu.Lock()
			out[l.User] = e.q
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(l Login) {
			defer wg.Done()
			q := keepLast(loginQuota(ctx, l), l.User)
			if q.Error != "" && ok && q.Provider != "claude" {
				q = e.q // a hiccup keeps what was known
			}
			c.Lock()
			if c.m == nil {
				c.m = map[string]loginUsageEntry{}
			}
			c.m[key] = loginUsageEntry{time.Now(), q}
			c.Unlock()
			mu.Lock()
			out[l.User] = q
			mu.Unlock()
		}(l)
	}
	wg.Wait()
	usageRead(agent, out) // a window not started: the warm-up looks now
	return out
}

// usageLogins are the accounts whose allowance LoginUsage asks for, and
// the agent they are read once a minute as: a plugin's accounts are asked
// for by the provider's id or as plugin:<id>, whichever names them. False
// for an agent that tells none, and for the built-in Grok, read by home.
func usageLogins(agent string) (logins []Login, known string, ok bool) {
	if pp, ok := pluginOfAgent(agent); ok {
		return pluginUsageLogins(pp), pluginAgent(pp), true
	}
	if agent == "grok" {
		return nil, agent, false
	}
	logins, ok = builtinLogins(agent)
	return logins, agent, ok
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
				q := copilotSubscriptionUsage(ctx, c.app.Token)
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
			q.Windows, err = claudeWindows(ctx, l.User, true)
		} else {
			var plan string
			if plan, q.Windows, q.Resets, err = codexWindows(ctx, tok, accountID); plan != "" {
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

// CodexUsedUp reports whether the ChatGPT account Codex is signed in to has
// used up its allowance for now; false when that isn't known.
func CodexUsedUp(ctx context.Context) bool {
	// read as LoginUsage has it, fetched at most once a minute: the agent
	// package asks on every catalog sync
	u := LoginUsage(ctx, "codex")
	for _, l := range Logins("codex") {
		if q, ok := u[l.User]; l.Active && ok && q.Error == "" && usedUp(q) {
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
