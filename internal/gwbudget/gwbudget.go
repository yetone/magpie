// Package gwbudget is the gateway's budget point (P3 of
// docs/subsystems/plugin-runtime.md): what holds a gateway key to the
// limits its owner set (#585). The gateway asks it before a key's request
// goes to any provider; plugins/budget serves internal/budget's limiter
// while it is on.
//
// It fails closed: a request of a key held to a limit is not sent while
// no limiter serves it (ErrUnlimited). A request without a key, or of a
// key with no limit, never asks it.
package gwbudget

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/access"
)

// Refusal is why a key's request was turned away, as the client is told.
type Refusal interface {
	error
	// RetryAfter is how long until the limit resets, in whole seconds.
	RetryAfter(now time.Time) int
	// ResetAt is when it resets.
	ResetAt() time.Time
}

// Limiter lets a key's request in, holding what it is expected to use
// until release, or refuses it. body is the request's size in bytes (-1
// unknown) and model the model it named.
type Limiter interface {
	Reserve(who access.Identity, body int64, model string, now time.Time) (release func(), refused Refusal)
}

// Spender is a Limiter that can tell which of a key's limits of some
// models (access.Limit.Models) have nothing left: the gateway leaves
// their models out of the request's candidates (access.Identity.Spent).
type Spender interface {
	Spent(who access.Identity, now time.Time) []access.SpentScope
}

var (
	mu       sync.Mutex
	limiters []*Limiter
	refresh  func()
	asking   atomic.Bool
	lastAsk  atomic.Int64 // when the last refresh began, UnixNano
)

// Limit serves l until remove. Registering one while another is served
// stacks: the last serves until it is removed.
func Limit(l Limiter) (remove func()) {
	e := &l
	mu.Lock()
	limiters = append(limiters, e)
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			for i, x := range limiters {
				if x == e {
					limiters = append(limiters[:i:i], limiters[i+1:]...)
					break
				}
			}
		})
	}
}

// Refresh sets what the gateway asks when a key held to a limit comes and
// no limiter is served: pluginrt's refresh, which turns the plugin on when
// a key has a limit. stop takes it away.
func Refresh(f func()) (stop func()) {
	mu.Lock()
	refresh = f
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			refresh = nil
			mu.Unlock()
		})
	}
}

func served() (Limiter, func()) {
	mu.Lock()
	defer mu.Unlock()
	if n := len(limiters); n > 0 {
		return *limiters[n-1], nil
	}
	return nil, refresh
}

// ForgetRefreshForTest lets the next Limiting refresh at once, as it
// would a second after the last one.
func ForgetRefreshForTest() { lastAsk.Store(0) }

// ErrUnlimited is the error a key's request gets when the key is held to
// a limit and nothing can hold it.
var ErrUnlimited = errors.New("this gateway key is held to a limit, but magpie's Key limits plugin isn't serving it, so nothing was sent; turn the plugin on in magpie's Settings → Plugins")

// Limiting is the limiter for who's request: nil, nil when who has no
// limit; ErrUnlimited when it has one and none serves. With none, one
// refresh is asked first (at most once a second, never from inside one),
// so a limit set a moment ago turns the plugin on for the request that
// needs it. Fail closed.
func Limiting(who access.Identity) (Limiter, error) {
	if !who.Limited() {
		return nil, nil
	}
	l, f := served()
	if l == nil && f != nil {
		now := time.Now().UnixNano()
		if now-lastAsk.Load() >= int64(time.Second) && asking.CompareAndSwap(false, true) {
			lastAsk.Store(now)
			f()
			asking.Store(false)
		}
		l, _ = served()
	}
	if l == nil {
		return nil, ErrUnlimited
	}
	return l, nil
}
