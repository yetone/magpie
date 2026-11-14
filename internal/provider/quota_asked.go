package provider

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// Allowances read only when asked (#1518, RooobinYe): with Settings →
// Usage → Allowance reads at "When I ask", magpie asks no vendor for an
// account's allowance or a key's balance on its own — not for the Usage
// page's or tray's timed refresh, the menu bar, alerts, routing, or the
// switch to an account with room. Each shows the reading kept from the
// last time it was read, as of then (keepLast). A read the user asked
// for (Asked) goes out as before, and so does one of an account the
// vendor just turned away for its quota (StaleAllowance), which routing
// must not keep sending to.
//
// magpie started at login comes up before the proxy app does, and its own
// reads went out direct from the user's real address.

type askedKey struct{}

// Asked is ctx for a read the user asked for: the Usage page opened or
// refreshed, a card's refresh, `magpie quota`, `magpie accounts`.
func Asked(ctx context.Context) context.Context {
	return context.WithValue(ctx, askedKey{}, true)
}

func wasAsked(ctx context.Context) bool {
	b, _ := ctx.Value(askedKey{}).(bool)
	return b
}

// errNotAsked is what a read held back says: "not read yet", so keepLast
// gives the reading kept from the last time instead.
var errNotAsked = errors.New("not read yet: magpie reads allowances only when you ask (Settings → Usage)")

// readsAsked is whether the user has allowances read only when asked.
func readsAsked() bool { return settings.Shared().QuotaReads == "asked" }

// heldRead is whether a read made with ctx is held back: the user has
// allowances read only when asked, and ctx is not one they asked for.
func heldRead(ctx context.Context) bool { return !wasAsked(ctx) && readsAsked() }

// holdUnasked is ctx, its requests held (netproxy.Hold) when it is a read
// held back: whatever a vendor's read goes through, nothing of it leaves.
func holdUnasked(ctx context.Context) context.Context {
	if netproxy.Held(ctx) == nil && heldRead(ctx) {
		return netproxy.Hold(ctx, errNotAsked)
	}
	return ctx
}

// staleReads are the accounts (agent/user) the vendor turned away for
// their quota since they were last read (StaleAllowance): read even while
// allowances are read only when asked.
var staleReads struct {
	sync.Mutex
	m map[string]bool
}

func markStaleRead(agent, user string) {
	staleReads.Lock()
	defer staleReads.Unlock()
	if staleReads.m == nil {
		staleReads.m = map[string]bool{}
	}
	staleReads.m[agent+"/"+strings.ToLower(user)] = true
}

// takeStaleRead says whether key is to be read though not asked, and
// forgets it: one read answers it.
func takeStaleRead(key string) bool {
	staleReads.Lock()
	defer staleReads.Unlock()
	if staleReads.m[key] {
		delete(staleReads.m, key)
		return true
	}
	return false
}

func isStaleRead(key string) bool {
	staleReads.Lock()
	defer staleReads.Unlock()
	return staleReads.m[key]
}

// holding is whether a read made with ctx sends nothing: held back
// (heldRead), or made under one that was (quotaReading put the hold on).
func holding(ctx context.Context) bool {
	return netproxy.Held(ctx) != nil || heldRead(ctx)
}

// unheld is ctx for a read that goes out though not asked for: an
// account the vendor turned away for its quota (StaleAllowance).
func unheld(ctx context.Context) context.Context {
	return netproxy.Hold(Asked(ctx), nil)
}
