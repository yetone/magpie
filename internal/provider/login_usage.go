package provider

// How much of its allowance each of an agent's accounts has used, the one
// the agent is signed in to and every saved one, so picking which to use
// next is a look at the list, not a guess.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
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
	// readAt, when set, is when read was really made: one handed back
	// held (allowances read only when asked) is dated now in at
	readAt time.Time
}

// readState is when an account's allowance was last read well, and
// whether the last try failed or was held back (a careful provider holds
// such an account: unknown is not spent, Provider.CapCareful).
type readState struct {
	at     time.Time
	failed bool
}

// state is what e says of the account's reading: a reading kept from
// before (AsOf) or one that failed counts as failed, as of the good one.
func (e loginUsageEntry) state() readState {
	at := e.at
	if !e.readAt.IsZero() {
		at = e.readAt
	}
	switch {
	case e.read.AsOf != nil:
		return readState{at: *e.read.AsOf, failed: true}
	case e.read.Error != "" || len(e.read.Windows) == 0:
		return readState{failed: true}
	}
	return readState{at: at}
}

// loginRead is an account's allowance being read: whoever asks for it
// meanwhile waits for it rather than asking the vendor again.
type loginRead struct {
	done chan struct{} // closed once e is in
	e    loginUsageEntry
}

// loginUsageFor, when set, stands in for LoginUsage's readings (tests),
// as UsageClaudeVia does for Claude's own. It is held for reading while it
// runs: Allowances reads in the background and returns without waiting,
// so a reading it began can outlast the test that set the stand-in.
var loginUsageFor struct {
	sync.RWMutex
	f func(ctx context.Context, agent string) map[string]SubscriptionQuota
}

// LoginUsageVia has tests stand in for LoginUsage's readings. It returns
// once no reading is still running through the one it replaces, and none
// starts through it after.
func LoginUsageVia(f func(ctx context.Context, agent string) map[string]SubscriptionQuota) {
	loginUsageFor.Lock()
	loginUsageFor.f = f
	loginUsageFor.Unlock()
}

// loginUsageStandIn is the stand-in's reading, and false when there is none.
func loginUsageStandIn(ctx context.Context, agent string) (map[string]SubscriptionQuota, bool) {
	loginUsageFor.RLock()
	defer loginUsageFor.RUnlock()
	if loginUsageFor.f == nil {
		return nil, false
	}
	return loginUsageFor.f(ctx, agent), true
}

// LoginUsage is the allowance used by each of an agent's accounts, by
// user. What was read less than a minute ago, here or for the Usage page,
// comes from the cache; the rest is asked for at once, as long as ctx
// allows.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out, _, _ := loginUsageAt(ctx, agent)
	return out
}

// loginUsageAt is LoginUsage and when the oldest of its readings was made:
// one taken from the cache is up to a minute old already, and Allowances
// counts its own minute from then, not from when it asked (#1295). And
// each account's reading's state: when it was last read well, and
// whether this try failed.
func loginUsageAt(ctx context.Context, agent string) (map[string]SubscriptionQuota, time.Time, map[string]readState) {
	return loginUsageOf(ctx, agent, nil)
}

// someLoginUsage is LoginUsage of the named accounts alone. What a
// background look needs is the account the agent is on, and the others
// only once that one is spent: reading every account at once, every few
// minutes from one address, is a pattern no single official client makes
// (owl on Discord, accounts held in magpie with no agent wired).
func someLoginUsage(ctx context.Context, agent string, users ...string) map[string]SubscriptionQuota {
	want := map[string]bool{}
	for _, u := range users {
		if u != "" {
			want[strings.ToLower(u)] = true
		}
	}
	if len(want) == 0 {
		return map[string]SubscriptionQuota{}
	}
	out, _, _ := loginUsageOf(ctx, agent, want)
	return out
}

// loginUsageOf is loginUsageAt over the accounts whose lower-cased user
// is in only, or over every account when only is nil.
func loginUsageOf(ctx context.Context, agent string, only map[string]bool) (map[string]SubscriptionQuota, time.Time, map[string]readState) {
	// nor of a provider switched off or removed (leftAlone)
	if settings.AccountDiscoveryOff() || leftAlone(agent) {
		return nil, time.Time{}, nil
	}
	asked := time.Now()
	states := map[string]readState{}
	if all, ok := loginUsageStandIn(ctx, agent); ok {
		out := all
		if only != nil {
			out = map[string]SubscriptionQuota{} // the stand-in's map is its own
			for user, q := range all {
				if only[strings.ToLower(user)] {
					out[user] = q
				}
			}
		}
		for user, q := range out {
			states[user] = loginUsageEntry{at: asked, read: q}.state()
		}
		return out, asked, states
	}
	out := map[string]SubscriptionQuota{}
	logins, ok := usageLogins(agent)
	if !ok {
		return out, asked, states
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	oldest := asked
	for _, l := range logins {
		if only != nil && !only[strings.ToLower(l.User)] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := loginReading(ctx, l)
			mu.Lock()
			out[l.User] = e.q
			states[l.User] = e.state()
			if e.q.Error == "" && e.at.Before(oldest) {
				oldest = e.at
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	usageRead(agent, out) // a window not started: the warm-up looks now
	if len(out) > 0 {
		// the batch's readings are points of each account's quota history
		// too, as a Usage-page reading is: a magpie only ever serving
		// other magpies has nobody on its Usage page, and its quota
		// history stayed empty otherwise (#1313). One note for the whole
		// batch: the file is read, parsed and written once for all
		// accounts. The note runs after every reading of the batch has
		// completed and released its in-flight marker, so no waiter on a
		// reading is held by a slow disk; the LoginUsage caller pays for
		// the write, and Allowances already calls it from its own
		// background goroutine. A detached goroutine is deliberately not
		// used: it would outlive the caller and write after tests and
		// commands have moved on (#1318 review). A cached reading notes
		// nothing new, its ReadAt no newer than what is kept; a failed
		// one is skipped.
		qs := make([]SubscriptionQuota, 0, len(out))
		for user, q := range out {
			q.User = user // a login's reading itself carries no user
			qs = append(qs, q)
		}
		noteHistory(qs, time.Now())
	}
	return out, oldest, states
}

// noteHistory notes a batch of accounts' readings in the quota history.
// The write is synchronous: it runs after the batch's readings have all
// completed, so it holds no waiter on a reading. Tests substitute their
// own note or write.
var noteHistory = func(qs []SubscriptionQuota, now time.Time) {
	noteHistoryWrite(qs, now)
}

// noteHistoryWrite is the history write itself; tests count or hold it.
var noteHistoryWrite = noteQuotaHistory

// loginReading is l's allowance as LoginUsage and the Usage page both show
// it, one reading for the two: what was read less than a minute ago comes
// from the cache, and an account being read is waited for, as long as ctx
// allows, rather than asked for again — the vendors' endpoints are
// rate limited, and two readings a moment apart told an account two ways.
func loginReading(ctx context.Context, l Login) loginUsageEntry {
	e := cachedLoginReading(ctx, l)
	if l.Agent == "codex" {
		// what magpie spent of its resets, as of now: a reading kept from
		// before one was spent still says it was
		used := ResetsUsed(l.User)
		e.q.ResetsUsed, e.read.ResetsUsed = used, used
	}
	return e
}

// cachedLoginReading is loginReading as read or kept.
func cachedLoginReading(ctx context.Context, l Login) loginUsageEntry {
	key := l.Agent + "/" + strings.ToLower(l.User)
	c := &loginUsageCache
	// with allowances read only when asked, one nobody asked for is what
	// was read last, however old, unless the vendor just turned the
	// account away for its quota (StaleAllowance), which is read (#1518)
	stale := false
	if holding(ctx) {
		if stale = isStaleRead(key); stale {
			ctx = unheld(ctx)
		}
	}
	held := holding(ctx)
	c.Lock()
	e, ok := c.m[key]
	if ok && (held || time.Since(e.at) < time.Minute) {
		c.Unlock()
		if held {
			if e.readAt.IsZero() {
				e.readAt = e.at // as it really was: a careful provider counts its age
			}
			e.at = time.Now() // as good as it gets until asked: Allowances keeps it its minute
		}
		return e
	}
	entry := func(read SubscriptionQuota) loginUsageEntry {
		q := read
		if q.Error != "" && ok && q.Provider != "claude" {
			q = e.q // a hiccup keeps what was known
		}
		return loginUsageEntry{at: time.Now(), q: q, read: read}
	}
	r := c.pending[key]
	if r == nil && held {
		// nothing read since magpie started: the reading kept on disk,
		// as of when it was made
		c.Unlock()
		return entry(keepLast(SubscriptionQuota{Provider: loginProvider(l), Plan: l.Plan, Windows: []QuotaWindow{}, Error: errNotAsked.Error()}, l.User))
	}
	if r == nil {
		if stale {
			takeStaleRead(key)
		}
		r = &loginRead{done: make(chan struct{})}
		if c.pending == nil {
			c.pending = map[string]*loginRead{}
		}
		c.pending[key] = r
		ctx, _ = quotaReading(ctx)
		asked := claudeAskGeneration.Load()
		go func() {
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
				if l.Agent != "claude" || claudeAskGeneration.Load() == asked {
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
		return entry(keepReading(ctx, SubscriptionQuota{Provider: loginProvider(l), Plan: l.Plan, Windows: []QuotaWindow{}, Error: ctx.Err().Error()}, l.User))
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
	case ChatGPTAPIID:
		logins = siwcLoginList()
	case "gemini", "antigravity":
		logins = googleLoginList(agent)
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
	if l.Agent == "gemini" || l.Agent == "antigravity" {
		return googleLoginQuota(ctx, l)
	}
	if l.Agent == "copilot" {
		for _, c := range copilotLogins(copilotConfigDir()) {
			if strings.EqualFold(c.User, l.User) {
				orig := c.app
				q := copilotSubscriptionUsage(ctx, c.app.Token, c.app.Host)
				// an editor's stale token, the CLI signed in to the same
				// account: read as the requests are sent (#1238)
				if q.Error == http.StatusText(http.StatusUnauthorized) {
					if cli, ok := copilotStandIn(c.app); ok {
						copilotRefuse(c.app.Token, http.StatusUnauthorized)
						c.app = cli
						q = copilotSubscriptionUsage(ctx, cli.Token, cli.Host)
					}
				}
				// GitHub refused the token, with none in its place: the
				// account is signed out (echo_ts on Discord)
				if q.Error == http.StatusText(http.StatusUnauthorized) {
					copilotRefuse(orig.Token, http.StatusUnauthorized)
					copilotRefuse(c.app.Token, http.StatusUnauthorized)
					copilotLapse(orig)
					q.Error = copilotLapsedText
				}
				if q.Error == "" {
					copilotUnlapse(orig)
					refreshCopilotEntitlement(c.app, q.Plan, q.AccessSKU)
				}
				return q
			}
		}
		return SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
	}
	q := SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}}
	if l.Agent == "claude" && !l.Active {
		// a saved account: what Claude Code told of it, or, when its card
		// is refreshed, its /usage run in the account's own directory
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
	var renew codexRenew
	switch {
	case l.Agent == "claude": // Claude Code reads its own (claudeWindows)
	case l.Active && leftAlone(l.Agent):
		// read for the Codex app's hold alone (codexHoldReading): the
		// token as Codex keeps it, never renewed nor written by magpie
		tok, accountID, err = codexTokenAsKept(codexHome())
	case l.Active:
		tok, accountID, err = codexToken(ctx, codexHome())
		renew = func(ctx context.Context, stale string) (string, string, error) {
			return codexTokenRenewing(ctx, codexHome(), stale)
		}
	default:
		tok, accountID, err = savedLoginToken(ctx, l.Agent, l.User)
		agent, user := l.Agent, l.User
		renew = func(ctx context.Context, stale string) (string, string, error) {
			return renewSavedLoginFrom(ctx, agent, user, stale)
		}
	}
	if err == nil {
		if l.Agent == "claude" {
			if q.Windows, err = claudeWindows(ctx, l.User, true); err == nil {
				q.ReadAt = claudeReadAt(l.User)
			}
		} else {
			var plan string
			if plan, q.Windows, q.Resets, q.Balance, q.Held, err = codexWindowsRenewing(ctx, tok, accountID, renew); plan != "" {
				q.Plan = plan
			}
			q.Until = codexUntil(codexLoginAuth(l), time.Now())
		}
	}
	if l.Agent == "codex" {
		err = codexSignInRead(l, tok, err)
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// codexSignInRead keeps what a ChatGPT account's usage read, made with
// access token tok, said of its sign-in (charliepu on Discord: an account
// showing no allowance, OpenAI having signed it out, till it was signed in
// again). Turned away for the sign-in itself with a refresh tried, or the
// refresh of the one Codex is signed in to refused, the account is marked
// signed out as a revoked one is (signin_revoked.go): its row says Sign in
// again, which runs magpie's sign-in for it and replaces its tokens, and
// the gateway passes over it till then. A saved account's refused refresh
// is marked lapsed already (savedRefreshFailed), but not one Codex is
// signed in to in another of its folders (codexHeldRefusal: charliepu's
// C:\Users\…\.codex-db-backup-…), which is marked here. A read that went through
// clears a mark it had. Anything else is unknown and marks nothing.
func codexSignInRead(l Login, tok string, err error) error {
	if err == nil {
		ServedSignIn(&Account{Agent: l.Agent, User: l.User})
		return nil
	}
	var refused refreshRefused
	if !l.Active && errors.As(err, &refused) && !errors.As(err, new(codexHeldRefusal)) {
		return err
	}
	m := bearerMark(codexGoneFor(err, tok, func() []byte { return codexLoginAuth(l) }))
	if m == "" {
		return err
	}
	markRevoked(l.Agent, l.User, m)
	return errors.New(revokedText(l.User))
}

// CodexUsedUp reports whether the ChatGPT account Codex is signed in to is
// out for the Codex app, which then sends nothing for it (codexHeld): out
// of its allowance with no credits to go on with. A window at 100% with
// credits left isn't (the Codex app keeps sending, and the backend
// answers). False when that isn't known.
func CodexUsedUp(ctx context.Context) bool {
	if leftAlone("codex") {
		return codexReadingHeld(codexHoldReading(ctx))
	}
	// read as LoginUsage has it, fetched at most once a minute: the agent
	// package asks on every catalog sync. Only the account Codex is
	// signed in to counts (codexReadingHeld), so only it is read
	var active []string
	for _, l := range Logins("codex") {
		if l.Active {
			active = append(active, l.User)
		}
	}
	return codexReadingHeld(someLoginUsage(ctx, "codex", active...))
}

// CodexHoldMatters is set by the agent package: whether Codex is wired to
// magpie beside its ChatGPT sign-in, so that magpie becomes its provider
// while the Codex app holds the account (codex.out), or is its provider
// for that already. Nil or false: what the app holds changes nothing.
var CodexHoldMatters func() bool

// codexHoldReading is the reading CodexUsedUp goes by for a Codex switched
// off or removed in magpie (leftAlone), which LoginUsage reads nothing
// of: the account Codex is signed in to alone, only while Codex takes
// magpie's models (CodexHoldMatters), at most once a minute (loginReading),
// its sign-in never renewed (loginQuota). The Codex app greys Send for
// every model, magpie's too, while it holds the account, and turning
// Codex off in magpie once its allowance ran out left the app so for good:
// nothing read, magpie never became its provider, and one that was
// stopped being it (sherlock_23333 on X).
func codexHoldReading(ctx context.Context) map[string]SubscriptionQuota {
	if settings.AccountDiscoveryOff() || CodexHoldMatters == nil || !CodexHoldMatters() {
		return nil
	}
	for _, l := range Logins("codex") {
		if l.Active {
			return map[string]SubscriptionQuota{l.User: loginReading(ctx, l).q}
		}
	}
	return nil
}

// CodexUsedUpKnown is CodexUsedUp over what was read already, asking no
// one and waiting for no one. False when nothing was read yet, which is
// CodexUsedUp's own answer for an account it can't tell about.
//
// For a caller on a path that must not do I/O: agent.SyncCatalog runs
// inside a write's own request (a routing group's Save), and the reading
// there is one the vendor is asked for over the network — a save waited
// 1.1s of the miss of its minute's cache. Nothing is lost by not asking:
// KeepOnAnAccountWithRoom reads it every loginSwitchEvery and
// noteCodexUsedUp tells the catalog when the answer changes, which is what
// the sync is reacting to, and the Usage page and routing read it as the
// user looks at them.
func CodexUsedUpKnown() bool {
	return codexReadingHeld(knownLoginUsage("codex"))
}

// codexReadingHeld is whether the reading u has the ChatGPT account Codex
// is signed in to held by the Codex app. The held of one account's
// reading, not codexHeld, which is what one vendor reply says.
func codexReadingHeld(u map[string]SubscriptionQuota) bool {
	for _, l := range Logins("codex") {
		if q, ok := u[l.User]; l.Active && ok && q.Error == "" && q.Held {
			return true
		}
	}
	return false
}

// codexHeldKnown is whether user's ChatGPT account was held by the Codex
// app (codexReadingHeld's held) at its last reading, asking no one; false
// when it was never read or the read failed.
func codexHeldKnown(user string) bool {
	e, ok := loginReadingKnown(Login{Agent: "codex", User: user})
	return ok && e.q.Error == "" && e.q.Held
}

// knownLoginUsage is LoginUsage over the readings already in hand, with
// no reading asked for: an account not read yet is left out, which its
// callers read as "not known".
func knownLoginUsage(agent string) map[string]SubscriptionQuota {
	logins, ok := usageLogins(agent)
	if !ok {
		return map[string]SubscriptionQuota{}
	}
	out := map[string]SubscriptionQuota{}
	for _, l := range logins {
		if e, ok := loginReadingKnown(l); ok {
			out[l.User] = e.q
		}
	}
	return out
}

// loginReadingKnown is what is known of l's allowance without asking:
// loginReading's cache alone, and none of its reading. False when the
// cache has nothing at all for the account.
func loginReadingKnown(l Login) (loginUsageEntry, bool) {
	key := l.Agent + "/" + strings.ToLower(l.User)
	c := &loginUsageCache
	c.Lock()
	defer c.Unlock()
	e, ok := c.m[key]
	return e, ok
}

// codexLoginAuth is the auth.json of a ChatGPT account magpie knows: the
// sign-in Codex goes by (auth.json or its keychain item), or the one kept for it.
func codexLoginAuth(l Login) []byte {
	if l.Active {
		b, _, _ := readCodexLogin(codexHome(), false)
		return b
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, x := range readLogins() {
		if x.Agent == "codex" && strings.EqualFold(x.User, l.User) {
			// as Codex keeps it in its other folder, when it is signed in there
			if home := codexHeldIn(x); home != "" {
				if b, _, err := readCodexLogin(home, false); err == nil {
					return b
				}
			}
			return x.Auth
		}
	}
	return nil
}
