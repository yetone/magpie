package provider

// Several subscriptions in use at once. The agent is signed in to one
// account; any other saved account that's on stands behind it, and the
// gateway sends a request there when the first is out of quota or rate
// limited. Such an account is never put into the agent's own store: its
// tokens stay in logins.json, refreshed there, so each refresh token still
// has exactly one holder.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// savedTokenMu keeps two requests from refreshing one saved account at
// once: the second would spend a refresh token the first just rotated.
var savedTokenMu sync.Mutex

// SetLoginOn puts a saved account in use beside the agent's own, or takes
// it out. The account the agent is signed in to is always in use.
func SetLoginOn(agent, user string, on bool) error {
	if pp, ok := pluginOfAgent(agent); ok {
		return setPluginLoginOn(pp, user, on)
	}
	switch agent {
	case "grok":
		return setGrokLoginOn(user, on)
	case "copilot":
		return setCopilotLoginOn(user, on)
	case "zcode":
		return setZCodeLoginOn(user, on)
	case "kiro":
		return setKiroLoginOn(user, on)
	case "devin":
		return setDevinLoginOn(user, on)
	case "workbuddy", WorkBuddyAIID:
		return setWorkBuddyLoginOn(wbSiteOf(agent), user, on)
	case CommandCodePlanID:
		return setCommandCodeLoginOn(user, on)
	case "qoder", QoderCNID:
		return setSideLoginOn(agent, user, on, qoderLoginsOf(agent))
	case "zed":
		return setZedLoginOn(user, on)
	case "factory":
		return setFactoryLoginOn(user, on)
	case MiMoID:
		return setMiMoLoginOn(user, on)
	case "gemini", "antigravity":
		return setGoogleLoginOn(agent, user, on)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	live, ok := liveLogin(agent)
	if !ok && agent == "claude" {
		// Claude Code signed out, the account served in its place is as its own
		if u := claudeStandIn(ls); u != "" {
			live, ok = savedLogin{Agent: agent, User: u}, true
		}
	}
	if ok && strings.EqualFold(live.User, user) {
		// the account the agent is signed in to stays so: off, it is
		// paused, passed over while another is on (#263) — the user's
		// own ChatGPT sign-in kept for Codex's remote control, a shared
		// Pro account doing the work
		if !on && !slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Agent == agent && l.On && !strings.EqualFold(l.User, user) }) {
			return fmt.Errorf("%s is the only %s account in use; turn another on first to pause it", user, agent)
		}
		for i := range ls {
			if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
				ls[i].Paused = !on
				return writeLogins(ls)
			}
		}
		if on {
			return nil
		}
		live.Paused, live.Seen = true, time.Now().UTC().Truncate(time.Second)
		return writeLogins(append(ls, live))
	}
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			ls[i].On = on
			return writeLogins(ls)
		}
	}
	return fmt.Errorf("no saved %s account %q", agent, user)
}

// pausedOwn says the account user, the one the agent is signed in to, is
// paused and another of the agent's accounts is on to take its place; the
// last other one off, it is used again.
func pausedOwn(ls []savedLogin, agent, user string) bool {
	paused, others := false, false
	for _, l := range ls {
		if l.Agent != agent {
			continue
		}
		if strings.EqualFold(l.User, user) {
			paused = l.Paused
		} else {
			others = others || l.On
		}
	}
	return paused && others
}

// OwnPaused says the provider is the account a Claude Code or Codex agent
// is signed in to, paused while another of its accounts is on (#263): the
// gateway passes over it, the agent staying signed in to it.
func (p Provider) OwnPaused() bool {
	if p.Account == nil || p.Account.token != nil && !p.Account.standIn || !slices.Contains(loginAgents, p.Account.Agent) {
		return false
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	return pausedOwn(readLogins(), p.Account.Agent, p.Account.User)
}

// AlsoOn is a signed-in agent's other accounts that are on, each as a
// provider of its own, in the order they were saved.
func (p Provider) AlsoOn() []Provider {
	if p.IsPlugin() {
		return pluginAlsoOn(*p.Account.plugin)
	}
	if p.Account != nil && p.Account.Agent == "grok" && p.Account.token == nil {
		return grokAlsoOn(p)
	}
	if p.Account != nil && p.Account.Agent == "copilot" {
		return copilotAlsoOn()
	}
	if p.Account != nil && p.Account.Agent == "zcode" {
		return zcodeAlsoOn()
	}
	if p.Account != nil && p.Account.Agent == "kiro" {
		return kiroAlsoOn(p)
	}
	if p.Account != nil && p.Account.Agent == "devin" {
		return devinAlsoOn()
	}
	if p.Account != nil && wbSiteOf(p.Account.Agent) != nil {
		return workBuddyAlsoOn(wbSiteOf(p.Account.Agent))
	}
	if p.Account != nil && p.Account.Agent == CommandCodePlanID {
		return commandCodeAlsoOn()
	}
	if p.Account != nil && (p.Account.Agent == "qoder" || p.Account.Agent == QoderCNID) {
		return qoderAlsoOn(p.Account.Agent)
	}
	if p.Account != nil && p.Account.Agent == "zed" {
		return zedAlsoOn()
	}
	if p.Account != nil && p.Account.Agent == "factory" {
		return factoryAlsoOn()
	}
	if p.Account != nil && p.Account.Agent == MiMoID {
		return mimoAlsoOn()
	}
	if p.Account != nil && (p.Account.Agent == "gemini" || p.Account.Agent == "antigravity") {
		return googleAlsoOn(p.Account.Agent)
	}
	if p.Account == nil || p.Account.token != nil && !p.Account.standIn || (p.Account.Agent != "claude" && p.Account.Agent != "codex") {
		return nil
	}
	var out []Provider
	for _, l := range Logins(p.Account.Agent) {
		if l.Active || l.first || !l.On {
			continue
		}
		agent, user := l.Agent, l.User
		a := *p.Account
		a.User, a.Plan, a.standIn = user, l.Plan, false
		a.token = func(ctx context.Context) (string, error) {
			tok, _, err := savedLoginToken(ctx, agent, user)
			return tok, err
		}
		if agent == "claude" {
			// Claude Code runs on it in a config directory of its own
			// (claude_dirs.go), and keeps the sign-in there itself
			a.token = func(context.Context) (string, error) { return claudeSavedDir(user) }
		}
		if agent == "codex" {
			a.sign = codexSign(func(ctx context.Context) (string, string, error) { return savedLoginToken(ctx, agent, user) })
		}
		q := p
		q.Account = &a
		out = append(out, q)
	}
	return out
}

// Token is the access token of a saved account in use beside the agent's
// own — for a Claude account, the config directory Claude Code runs on it
// in; ok is false for the agent's own, which the agent signs itself. A
// Claude account that can't be used, Anthropic having refused its sign-in,
// is an error, whichever it is (claude_auth.go).
func (a *Account) Token(ctx context.Context) (tok string, ok bool, err error) {
	if a == nil {
		return "", false, nil
	}
	if a.token == nil {
		if a.Agent == "claude" {
			if why := claudeOwnRefused(a.User); why != "" {
				return "", false, errors.New(why)
			}
		}
		return "", false, nil
	}
	tok, err = a.token(ctx)
	return tok, true, err
}

// savedLoginToken is a usable access token for a saved account, refreshed
// when it's about to expire and written back to logins.json.
func savedLoginToken(ctx context.Context, agent, user string) (tok, accountID string, err error) {
	return renewSavedLogin(ctx, agent, user, false)
}

// renewSavedLogin is savedLoginToken, with force to refresh the sign-in
// even while its access token is good (keepalive.go). A refresh the vendor
// refuses is kept on the account as lapsed, until one goes through or it is
// signed in again.
func renewSavedLogin(ctx context.Context, agent, user string, force bool) (tok, accountID string, err error) {
	ctx = ViaLogin(ctx, agent, user) // refreshed through the account's own proxy
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	loginsMu.Lock()
	var l *savedLogin
	for _, x := range readLogins() {
		if x.Agent == agent && strings.EqualFold(x.User, user) {
			x := x
			l = &x
		}
	}
	loginsMu.Unlock()
	if l == nil {
		return "", "", fmt.Errorf("no saved %s account %q", agent, user)
	}
	var auth []byte
	switch agent {
	case "codex":
		var raw map[string]any
		var a codexAuth
		if json.Unmarshal(l.Auth, &raw) != nil || json.Unmarshal(l.Auth, &a) != nil || a.Tokens.AccessToken == "" {
			return "", "", errors.New("the saved ChatGPT sign-in of " + user + " is unreadable")
		}
		accountID = a.Tokens.AccountID
		if accountID == "" {
			accountID = claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
		}
		if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); !force && (exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute) {
			return a.Tokens.AccessToken, accountID, nil
		}
		if tok, err = codexRefresh(ctx, raw); err != nil {
			return "", "", savedRefreshFailed(agent, user, err)
		}
		if auth, err = json.MarshalIndent(raw, "", "  "); err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("%s accounts can't be used side by side", agent)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			ls[i].Auth = auth
			ls[i].Renewed = time.Now().UTC().Truncate(time.Second)
			ls[i].Lapsed = ""
		}
	}
	return tok, accountID, writeLogins(ls)
}

// savedRefreshFailed words a saved account's failed refresh. One the vendor
// refused is marked lapsed on the account: that sign-in is gone and has to be
// made again, in magpie; the agent's own login command would sign the agent
// in, not this account. A refresh that never got an answer marks nothing.
func savedRefreshFailed(agent, user string, err error) error {
	var refused refreshRefused
	if !errors.As(err, &refused) {
		return fmt.Errorf("%s: %w", user, err)
	}
	msg := user + "'s sign-in has expired — add the account again to use it"
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			ls[i].Lapsed = msg
		}
	}
	_ = writeLogins(ls)
	return refreshRefused(msg)
}
