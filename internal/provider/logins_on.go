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
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// savedTokenMu keeps two requests from refreshing one saved account at
// once: the second would spend a refresh token the first just rotated. It
// holds across magpies too (#1674): the gateway's refresh and one by a
// magpie run from a terminal would each spend the same refresh token.
var savedTokenMu = filelock.Mutex{Path: func() string { return loginsPath() + ".renew.lock" }}

// SetLoginOn puts a saved account in use beside the agent's own, or takes
// it out. The account the agent is signed in to is always in use.
func SetLoginOn(agent, user string, on bool) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	if pp, ok := pluginOfAgent(agent); ok {
		return setPluginLoginOn(pp, user, on)
	}
	switch agent {
	case "copilot":
		return setCopilotLoginOn(user, on)
	case ChatGPTAPIID:
		return setSIWCLoginOn(user, on)
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
// gateway passes over it, the agent staying signed in to it. A request's
// plan asks it of every account provider, each ask its own read of
// logins.json under its lock: listed once a request, as Logins is (#5104),
// and a write lists it anew.
func (p Provider) OwnPaused() bool {
	if p.Account == nil || p.Account.token != nil && !p.Account.standIn || !slices.Contains(loginAgents, p.Account.Agent) {
		return false
	}
	return heldOf("ownPaused\x00"+p.Account.Agent+"\x00"+p.Account.User, func() bool {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		return pausedOwn(readLogins(), p.Account.Agent, p.Account.User)
	})
}

// AlsoOn is a signed-in agent's other accounts that are on, each as a
// provider of its own, in the order they were saved. Each keeps what the
// user set on the provider, as p has it from All: its queue, limits,
// routing and models (withPicks). A Claude or Codex account is p with
// another sign-in, and had them always; one a sign-in of its own makes
// afresh had none, and a second WorkBuddy AI account queued 62s with the
// wait set to 1s (#1899).
func (p Provider) AlsoOn() []Provider {
	out := p.alsoOn()
	for i, q := range out {
		out[i] = withPicks(q, p)
	}
	return out
}

// alsoOn is AlsoOn as each sign-in makes its accounts, without the
// provider's settings.
func (p Provider) alsoOn() []Provider {
	if p.IsPlugin() {
		return pluginAlsoOn(*p.Account.plugin)
	}
	if p.Account != nil && p.Account.Agent == "copilot" {
		return copilotAlsoOn()
	}
	if p.Account != nil && p.Account.Agent == ChatGPTAPIID {
		return siwcAlsoOn()
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
			a.renew = func(ctx context.Context, stale string) (string, string, error) {
				return renewSavedLoginFrom(ctx, agent, user, stale)
			}
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
	return renewSaved(ctx, agent, user, force, "")
}

// renewSavedLoginFrom refreshes the saved sign-in whose access token stale
// the backend just called expired while it had time left (#2973), unless
// it was renewed since: the token saved by now is returned then, its
// refresh token being the one spent already.
func renewSavedLoginFrom(ctx context.Context, agent, user, stale string) (tok, accountID string, err error) {
	return renewSaved(ctx, agent, user, false, stale)
}

func renewSaved(ctx context.Context, agent, user string, force bool, stale string) (tok, accountID string, err error) {
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
	if agent == "codex" && heldByCodex(*l) {
		// the account Codex is signed in to now (switched to since this
		// was asked for, or the copy kept of it as it was): one sign-in in
		// two places, whose refresh token a refresh here would spend under
		// Codex. Codex's own auth.json then holds a dead one, and the app
		// of an edu or business workspace, which reads its workspace's
		// settings with it at start, stays paused on "无法加载组织设置"
		// (万贤凯 on Discord). So it is renewed where Codex keeps it, as
		// Codex's own (codexTokenRenewing), and the copy is left as it is:
		// a switch away saves it afresh from Codex's.
		return codexTokenRenewing(ctx, codexHome(), stale)
	}
	if home := codexHeldIn(*l); home != "" {
		// the same for an account Codex is signed in to in another of
		// its folders (CODEX_HOME=~/.codex-work): Codex there refreshes
		// it too, so magpie renews it in that folder, never in a copy
		// whose refresh token would sign that Codex out
		tok, accountID, err := codexTokenRenewing(ctx, home, stale)
		if err != nil {
			// said with no command to run (charliepu on Discord, Windows:
			// "run CODEX_HOME=… codex login"): a refusal marks the account
			// signed out on its row (codexSignInRead), whose Sign in again
			// runs magpie's own sign-in and writes it back to home (addLogin)
			err = fmt.Errorf("%s, signed in to Codex in %s: %w", user, home, err)
			if errors.As(err, new(refreshRefused)) {
				err = codexHeldRefusal{err}
			}
			return "", "", err
		}
		return tok, accountID, nil
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
		if stale != "" {
			if a.Tokens.AccessToken != stale {
				return a.Tokens.AccessToken, accountID, nil
			}
		} else if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); !force && (exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute) {
			return a.Tokens.AccessToken, accountID, nil
		}
		reason := codexRefreshExpiry
		if stale != "" {
			reason = codexRefreshRecovery
		} else if force {
			reason = forcedReason(ctx)
		}
		if tok, err = codexRefresh(ctx, raw, reason); err != nil {
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

// heldByCodex says the saved ChatGPT account l is the one Codex is signed
// in to: the same login, or the same refresh token. Asked under
// savedTokenMu, which a switch holds too, so it can't change before the
// refresh it decides is done.
func heldByCodex(l savedLogin) bool {
	live, ok := liveLogin("codex")
	if !ok {
		return false
	}
	if sameLogin(live, l) {
		return true
	}
	return sameRefresh(live.Auth, l.Auth)
}

// codexHeldRefusal is a refresh OpenAI refused for an account Codex is
// signed in to in another of its folders (codexHeldIn). savedRefreshFailed
// doesn't mark it, magpie's copy not being the sign-in, so codexSignInRead
// marks it signed out as it does Codex's own.
type codexHeldRefusal struct{ error }

func (e codexHeldRefusal) Unwrap() error { return e.error }

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
