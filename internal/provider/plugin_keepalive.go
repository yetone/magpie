package provider

// Keeping the plugins' sign-ins alive while nothing uses them (#5080). A
// plugin that renews its sign-ins through magpie (auth.refresh) had each
// renewed only when something used the account: a request, its models,
// its usage or its check-in, within the plugin's refreshLead of expiry.
// An account left idle past its token's life lapsed, and for a vendor
// whose session dies with its token the user had to sign in again:
// OrcaTerm's WeChat token lives two hours, and its refresh after that
// answers SESSION_EXPIRED for good (orinsid, plugins#675).
//
// So the magpie serving the gateway renews each such sign-in as it comes
// due, on a timer, through the host's own renewal (plugin.KeepSignedIn):
// the one a request would make, once at a time per account whoever asks
// (a request arriving meanwhile waits for the same one) and under the
// lock file every magpie process renews under. Only the accounts switched
// on are kept, of a plugin provider not switched off, not hidden and not
// being moved; one the vendor turned away (Lapsed) waits for the user to
// sign in again. A plugin that would rather not (a renewal that costs the
// account something, or a refresh token another client holds too) says
// auth.keepAlive: false.
//
// Nothing is asked until an account is due: a look reads plugin-auth.json
// (parsed once per change, filememo) and sleeps until the next account
// comes due, no longer than keepAliveLookMost, so an idle magpie with no
// account due starts no plugin host. A renewal that fails is tried again
// later and later (keepAliveBackoff), for that sign-in only, and never
// marks the account: a renewal that couldn't reach its vendor says
// nothing about the sign-in. Only the plugin saying the vendor turned it
// away for good marks it, as a request's renewal does.

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

var (
	// keepAliveLookFirst is how long after the gateway starts the first
	// look is made, once its start has listed the plugins
	keepAliveLookFirst = 30 * time.Second
	// keepAliveLookMost is the longest a look waits for the next: a
	// sign-in made meanwhile is seen by then (the shortest refreshLead a
	// plugin says is a minute, and its token is due a lead early)
	keepAliveLookMost = 5 * time.Minute
	// keepAliveLookLeast is the shortest: a token due already but held
	// back (keepAliveBackoff) isn't looked at in a busy loop
	keepAliveLookLeast = 15 * time.Second
	// keepAliveRenewWait bounds one renewal: the host's own lock is held
	// at most a minute (RENEW_LOCK_STALE) before another takes it over
	keepAliveRenewWait = 2 * time.Minute
)

// keepAliveBackoff is how long a sign-in whose renewal failed n times in
// a row waits before it is tried again: a minute, doubled each time, at
// most half an hour. An outage of the vendor's isn't asked every look,
// and its accounts are renewed soon after it ends.
func keepAliveBackoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < 30*time.Minute; i++ {
		d *= 2
	}
	return min(d, 30*time.Minute)
}

// pluginSignIn is a plugin account kept alive: its provider, its key in
// plugin-auth.json, the account as magpie lists it (for its proxy) and
// when it comes due.
type pluginSignIn struct {
	ID, Key, User string
	At            time.Time
}

// pluginSignInsKept are the plugins' sign-ins kept alive in the
// background, each with when it is next due: of the plugins that renew
// through magpie and keep alive (plugin.Provider.Renews), the accounts
// switched on and not refused, of a provider neither switched off, hidden
// nor being moved. Tests replace it.
var pluginSignInsKept = func() []pluginSignIn {
	var out []pluginSignIn
	for _, pp := range heldPlugins() {
		if !pp.Renews || movingNow(pp.ID) || leftAlone(PluginID(pp.ID)) {
			continue
		}
		for _, l := range pluginLogins(pp) {
			if !l.On || l.Lapsed != "" {
				continue
			}
			if at, ok := plugin.RenewAt(pp, l.acct.Key); ok {
				out = append(out, pluginSignIn{ID: pp.ID, Key: l.acct.Key, User: l.User, At: at})
			}
		}
	}
	return out
}

// keepPluginSignedIn renews one sign-in if it is due; tests replace it.
var keepPluginSignedIn = func(ctx context.Context, s pluginSignIn) (plugin.KeptSignedIn, error) {
	if !plugin.Running() && !plugin.HasBun() {
		return plugin.KeptSignedIn{}, errNoBun
	}
	return plugin.KeepSignedIn(ViaLogin(ctx, PluginID(s.ID), s.User), s.ID, s.Key)
}

var errNoBun = errors.New("no Bun to run the plugins on")

// signInKeeper keeps the plugins' sign-ins alive, a look at a time, from
// one goroutine.
type signInKeeper struct {
	// held is each sign-in held back, by its key and the time it was due
	// (a sign-in renewed or made again since has another due time, and
	// starts afresh): its renewal failed n times in a row, or, n 0, the
	// host found it not due yet
	held map[signInDue]keptFailure
}

type signInDue struct {
	key string
	at  int64
}

type keptFailure struct {
	n     int
	until time.Time
}

// look renews each sign-in due at now, one after another, but those held
// back after a failure, and says how long until the next look.
func (k *signInKeeper) look(ctx context.Context, now time.Time) time.Duration {
	next := now.Add(keepAliveLookMost)
	soon := func(t time.Time) {
		if t.Before(next) {
			next = t
		}
	}
	due := map[signInDue]bool{}
	for _, s := range pluginSignInsKept() {
		if ctx.Err() != nil {
			break
		}
		d := signInDue{s.Key, s.At.UnixMilli()}
		due[d] = true
		if s.At.After(now) {
			soon(s.At)
			continue
		}
		if f, ok := k.held[d]; ok && now.Before(f.until) {
			soon(f.until)
			continue
		}
		c, cancel := context.WithTimeout(ctx, keepAliveRenewWait)
		r, err := keepPluginSignedIn(c, s)
		cancel()
		if err == nil && r.Err != "" {
			err = errors.New(r.Err)
		}
		hold := func(f keptFailure, d2 time.Duration) {
			f.until = now.Add(d2)
			if k.held == nil {
				k.held = map[signInDue]keptFailure{}
			}
			k.held[d] = f
			soon(f.until)
		}
		switch {
		case err != nil && ctx.Err() != nil:
			// magpie is stopping: nothing is known of the sign-in
		case err != nil || r.Due && !r.Renewed:
			f := k.held[d]
			f.n++
			hold(f, keepAliveBackoff(f.n))
			if err != nil && !r.Waiting {
				log.Printf("%s: renewing a sign-in nothing used: %v (tried again in %s)", s.ID, err, keepAliveBackoff(f.n))
			}
		case !r.Due:
			// the host reckons it otherwise (a plugin updated to another
			// lead, not listed again yet): not asked again every look
			hold(keptFailure{}, keepAliveLookMost)
		default:
			delete(k.held, d)
		}
	}
	// a sign-in renewed, made again or gone is forgotten
	for d := range k.held {
		if !due[d] {
			delete(k.held, d)
		}
	}
	return min(max(next.Sub(now), keepAliveLookLeast), keepAliveLookMost)
}

// KeepPluginsSignedIn renews the plugins' sign-ins as they come due, in
// the magpie serving the gateway, while nothing uses them (#5080).
func KeepPluginsSignedIn(ctx context.Context) {
	var k signInKeeper
	t := time.NewTimer(keepAliveLookFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(k.look(ctx, time.Now()))
	}
}
