package gateway

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A search API given several keys (#1477) spreads its searches over them
// (SlouchyQuill on Discord, after dsh-tavily-pool): each search starts
// from the next key, or from the first when the API is set to use them in
// order (provider.SearchPickOrder). A key the API refused lately, or one a
// quota read found used up, rests: it is asked only after every key that
// isn't resting, the one whose rest ends first before the others, so a
// search still has every key to fall back on.

// keyRest is why a key rests, and until when.
type keyRest struct {
	Until time.Time
	Why   string // "turned away", "out of credit", "rate limited", "used up"
}

var (
	// searchRests is, by searchKeyID, the keyRest of each key resting
	searchRests sync.Map
	// searchTurns is, by vendor, how many searches have taken a turn: the
	// key the next one starts from
	searchTurns sync.Map
)

// searchKeyID is how a key is known here: its vendor and a hash, so no
// map, log or dump holds the key itself.
func searchKeyID(vendor, key string) string {
	sum := sha256.Sum256([]byte(key))
	return vendor + " " + hex.EncodeToString(sum[:8])
}

// The rests a refusal earns. A rate limit says when to come back
// (Retry-After), held between its bounds; a key turned away (401/403) may
// be fixed by the user, so it is tried again in a few minutes; one out of
// credit gets its credit back no sooner than a top-up, so it rests longer.
const (
	rateLimitRest    = time.Minute
	rateLimitRestMin = 30 * time.Second
	rateLimitRestMax = 10 * time.Minute
	turnedAwayRest   = 10 * time.Minute
	outOfCreditRest  = time.Hour
)

// searchRestFor is how long a key a search API refused with this status rests,
// and why. 432 and 433 are Tavily's own (its plan's and its pay-as-you-go
// limit), out of credit there; from any other API they are only a refusal,
// and rest as a rate limit does.
func searchRestFor(vendor string, status int, retryAfter string, now time.Time) (time.Duration, string) {
	switch {
	case status == 401 || status == 403:
		return turnedAwayRest, "turned away"
	case status == 402, vendor == "tavily" && (status == 432 || status == 433):
		return outOfCreditRest, "out of credit"
	}
	d := rateLimitRest
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 {
		d = time.Duration(s) * time.Second
	} else if at, err := http.ParseTime(retryAfter); err == nil {
		d = at.Sub(now)
	}
	return min(max(d, rateLimitRestMin), rateLimitRestMax), "rate limited"
}

// restSearchKey sets a key resting for d; a rest it has that ends later
// is kept.
func restSearchKey(vendor, key string, d time.Duration, why string, now time.Time) {
	r := keyRest{Until: onWall(now.Add(d)), Why: why}
	if was, ok := searchKeyRest(vendor, key, now); ok && was.Until.After(r.Until) {
		return
	}
	searchRests.Store(searchKeyID(vendor, key), r)
}

// wakeSearchKey ends a key's rest: it answered, or a quota read found it
// has credit.
func wakeSearchKey(vendor, key string) { searchRests.Delete(searchKeyID(vendor, key)) }

// searchKeyRest is the rest a key is in now, if it is.
func searchKeyRest(vendor, key string, now time.Time) (keyRest, bool) {
	v, ok := searchRests.Load(searchKeyID(vendor, key))
	if !ok {
		return keyRest{}, false
	}
	r := v.(keyRest)
	if !now.Before(r.Until) {
		return keyRest{}, false
	}
	return r, true
}

// SearchKeyRest is why a search API's key rests now (searchKeyOrder asks
// it last), and until when; "" when it doesn't.
func SearchKeyRest(vendor, key string) (string, time.Time) {
	if r, ok := searchKeyRest(vendor, key, time.Now()); ok {
		return r.Why, r.Until
	}
	return "", time.Time{}
}

// searchKeyOrder is the order a search asks a's keys in, by their index:
// from the key whose turn it is (the first, for SearchPickOrder), each
// key resting put after those that aren't, the soonest awake first.
// Taking turns, each call moves the next search on by one.
func searchKeyOrder(a provider.SearchAPI, keys []string, now time.Time) []int {
	n := len(keys)
	start := 0
	if a.Pick != provider.SearchPickOrder && n > 1 {
		v, _ := searchTurns.LoadOrStore(a.Vendor, new(atomic.Uint64))
		start = int((v.(*atomic.Uint64).Add(1) - 1) % uint64(n))
	}
	order := make([]int, 0, n)
	var resting []int
	until := map[int]time.Time{}
	for k := range n {
		i := (start + k) % n
		if r, ok := searchKeyRest(a.Vendor, keys[i], now); ok {
			resting, until[i] = append(resting, i), r.Until
			continue
		}
		order = append(order, i)
	}
	slices.SortStableFunc(resting, func(x, y int) int { return until[x].Compare(until[y]) })
	return append(order, resting...)
}

// forgetSearchKeys forgets every rest and turn: a test's fresh start.
func forgetSearchKeys() {
	searchRests.Clear()
	searchTurns.Clear()
}

// keyLabel is how an error names one of several keys: its place in the
// list and the key masked.
func keyLabel(i int, key string) string {
	return "key " + strconv.Itoa(i+1) + " (" + cmp.Or(provider.MaskSearchKey(key), "none") + ")"
}
