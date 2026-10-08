package provider

// PLUGIN-SERVED (see AGENTS.md): Grok ("grok") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-grok-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/grok) and raise the mover's
// min in internal/provider/migrate_side.go.

// Several Grok subscriptions. The Grok CLI keeps one account, in its own
// home (~/.grok), which magpie only ever reads. Each further account gets a
// home of magpie's own, signed in there by the CLI's own `login`, so the
// user's sign-in is never touched and each refresh token has one holder.
// logins.json names those homes; the CLI's own account is remembered there
// too, with no home, so it can stand behind another or be turned off.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// grokAccountsDir holds the homes of the Grok accounts magpie signed in.
func grokAccountsDir() string { return filepath.Join(filepath.Dir(Path()), "grok") }

// grokLogin is a Grok account and the home it is signed in in.
type grokLogin struct {
	Login
	Home string
}

// grokLogins lists the Grok accounts that are signed in, the first in use
// first, then the rest as they were added.
func grokLogins() []grokLogin {
	own := ""
	if c, ok := readGrokCredential(GrokHome()); ok {
		own = c.Email
	}
	var out []grokLogin
	for _, l := range sideLogins("grok", own, func(l savedLogin) bool {
		_, ok := readGrokCredential(l.Home)
		return ok
	}) {
		home := l.saved.Home
		if home == "" {
			home = GrokHome()
		}
		out = append(out, grokLogin{l.Login, home})
	}
	return out
}

func grokSide() []sideLogin {
	var out []sideLogin
	for _, g := range grokLogins() {
		out = append(out, sideLogin{Login: g.Login})
	}
	return out
}

// grokLoginList is the Grok accounts as Logins lists them.
func grokLoginList() []Login { return loginsOf(grokSide()) }

// switchGrokLogin puts a Grok account first. The CLI's own sign-in stays
// as it is: magpie only changes which account its gateway uses first.
func switchGrokLogin(user string) error { return switchSideLogin("grok", user, grokSide()) }

func setGrokLoginOn(user string, on bool) error {
	return setSideLoginOn("grok", user, on, grokSide())
}

// forgetGrokLogin drops an account magpie signed in, with its home. The
// CLI's own is only hidden (forgetSideLogin).
func forgetGrokLogin(user string) error {
	return forgetSideLogin("grok", user, grokSide(),
		func(l savedLogin) { removeGrokHome(l.Home) })
}

// removeGrokHome deletes a home magpie made, and nothing else.
func removeGrokHome(home string) {
	if rel, err := filepath.Rel(grokAccountsDir(), home); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		_ = os.RemoveAll(home)
	}
}

// newGrokHome makes the home a further Grok account is signed in in.
func newGrokHome() (string, error) {
	home := filepath.Join(grokAccountsDir(), strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(randomToken(9))))
	return home, os.MkdirAll(home, 0o700)
}

// addGrokLogin keeps the account just signed in in home, in use beside
// the others. Signed in again, an account keeps the newer home.
func addGrokLogin(home string) (string, error) {
	c, ok := readGrokCredential(home)
	if !ok || c.Email == "" {
		removeGrokHome(home)
		return "", errors.New("grok login finished without an account")
	}
	own, _ := readGrokCredential(GrokHome())
	return c.Email, addSideLogin(savedLogin{Agent: "grok", User: c.Email, Home: home}, own.Email,
		func(l savedLogin) { removeGrokHome(l.Home) })
}

// grokAlsoOn is the Grok accounts in use behind the first.
func grokAlsoOn(p Provider) []Provider {
	var out []Provider
	for _, g := range grokLogins() {
		if g.Active || !g.On {
			continue
		}
		a := *p.Account
		a.User, a.Plan, a.Home = g.User, g.Plan, g.Home
		grokSigned(&a, g.Home)
		q := p
		q.Account = &a
		out = append(out, q)
	}
	return out
}

var grokHomeUsage struct {
	sync.Mutex
	m       map[string]loginUsageEntry // home
	pending map[string]uint64          // home: the reading out now
	reads   uint64
}

// grokLoginUsage is each Grok account's allowance, by user, as LoginUsage
// answers it.
func grokLoginUsage(ctx context.Context) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	u := &grokHomeUsage
	for _, g := range grokLogins() {
		u.Lock()
		e, ok := u.m[g.Home]
		u.Unlock()
		if ok && time.Since(e.at) < time.Minute {
			mu.Lock()
			out[g.User] = e.q
			mu.Unlock()
			continue
		}
		u.Lock()
		if u.pending == nil {
			u.pending = map[string]uint64{}
		}
		u.reads++
		read := u.reads
		u.pending[g.Home] = read
		u.Unlock()
		wg.Add(1)
		go func(g grokLogin) {
			defer wg.Done()
			q := keepLast(grokUsageAt(ctx, g.Home), g.User)
			if q.Error != "" && ok {
				q = e.q // a hiccup keeps what was known
			}
			u.Lock()
			// one dropped meanwhile (StaleAllowance) was asked for too soon,
			// and one a later reading took over from is older: either goes
			// to its caller but isn't kept, as in loginReading
			if u.pending[g.Home] == read {
				delete(u.pending, g.Home)
				if u.m == nil {
					u.m = map[string]loginUsageEntry{}
				}
				u.m[g.Home] = loginUsageEntry{at: time.Now(), q: q}
			}
			u.Unlock()
			mu.Lock()
			out[g.User] = q
			mu.Unlock()
		}(g)
	}
	wg.Wait()
	return out
}

// grokUsageAt is the allowance of the account signed in in home.
func grokUsageAt(ctx context.Context, home string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "grok", Name: "Grok (SuperGrok)", Icon: "xai", Windows: []QuotaWindow{}}
	c, err := grokAccessToken(home, GrokExecutable(), false)
	if err == nil {
		q.Windows, err = grokWindows(ctx, c.Key)
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}
