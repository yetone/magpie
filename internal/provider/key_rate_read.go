package provider

// A key's group rate read from its relay (@YIXIE233 on X): a relay built on
// sub2api tells each key the rate its group bills at against the official
// price, at GET /v1/sub2api/billing, and relays sell their credit at
// different prices (¥1 for $1 at one, ¥7 for $100 at another). The user
// says once, on the provider, what a relay dollar cost them (TopUpRate),
// and each key bills at its group's rate times that, read again every few
// hours, so a relay changing its prices needs nothing typed again.
//
// The read is never on a request's path: KeepKeyRatesRead asks each
// relay in the background, and pricing looks the last reading up. A rate
// the user gave a key of its own (KeyPriceRates) stands. A key not read
// yet, or whose read failed with nothing read before, is unknown and
// bills at the provider's PriceRate as before, never at 1× for a guess.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sub2APIKeyBilling is sub2api's query for the rate a key's group bills at
// (backend/internal/handler/gateway_key_billing.go): asked with the key,
// it skips the panel's billing checks and doesn't mark the key used.
const sub2APIKeyBilling = "/v1/sub2api/billing"

// keyRateEvery is how long a key's read rate stands before it is read
// again, keyRateRetry how long after a failed read; keyRateTick how often
// the loop looks for a key that is due.
var (
	keyRateEvery = 6 * time.Hour
	keyRateRetry = 30 * time.Minute
	keyRateTick  = 5 * time.Minute
)

// TopUpRateOK says what is wrong with a provider's top-up rate, "" when
// nothing: above 0 up to 1000, at most four decimals (¥5 for $70 is
// 0.0714). None (0) is no rate: no key's group rate is read.
func TopUpRateOK(r float64) string {
	if math.IsNaN(r) || r < 0 || r > 1000 {
		return "a top-up rate is from 0 to 1000"
	}
	if math.Abs(r*10000-math.Round(r*10000)) > 1e-6 {
		return "a top-up rate has at most four decimals, like 0.0714"
	}
	return ""
}

// keyRateRead is one key's last reading.
type keyRateRead struct {
	// Rate is the group's rate, Known whether one was ever read
	Rate  float64 `json:"rate"`
	Known bool    `json:"known,omitempty"`
	// At is when it was last asked, Err what went wrong then ("" when
	// it answered)
	At     time.Time `json:"at"`
	Err    string    `json:"err,omitempty"`
	Origin string    `json:"origin,omitempty"`
}

var keyRates struct {
	sync.Mutex
	m    map[string]keyRateRead // provider id + " " + key id
	path string                 // the file m was read from
}

func keyRatesPath() string { return filepath.Join(filepath.Dir(Path()), "key-rates.json") }

func keyRateKey(providerID, keyID string) string {
	return providerID + " " + strings.ToLower(keyID)
}

// keyRatesLocked is the map, read from the file the first time (and
// again for another config folder); a file that can't be read or doesn't
// parse is nothing read yet. The caller holds keyRates.
func keyRatesLocked() map[string]keyRateRead {
	path := keyRatesPath()
	if keyRates.m != nil && keyRates.path == path {
		return keyRates.m
	}
	keyRates.m, keyRates.path = map[string]keyRateRead{}, path
	if b, err := os.ReadFile(path); err == nil {
		var saved map[string]keyRateRead
		if json.Unmarshal(b, &saved) == nil {
			for k, v := range saved {
				keyRates.m[k] = v
			}
		}
	}
	return keyRates.m
}

func saveKeyRatesLocked() {
	b, err := json.Marshal(keyRates.m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(keyRatesPath()), 0o700)
	_ = writeFileAtomic(keyRatesPath(), b)
}

// readKeyRate is the group rate last read for the key keyID of the
// provider id, and whether one is known.
func readKeyRate(providerID, keyID string) (float64, bool) {
	if providerID == "" || keyID == "" {
		return 0, false
	}
	keyRates.Lock()
	defer keyRates.Unlock()
	r, ok := keyRatesLocked()[keyRateKey(providerID, keyID)]
	return r.Rate, ok && r.Known
}

// KeyRatesKey names the group rates read so far by what they are, for a
// cache of costs to be built again when one changes: a read that found
// the same rates leaves it as it was.
func KeyRatesKey() string {
	keyRates.Lock()
	defer keyRates.Unlock()
	m := keyRatesLocked()
	if len(m) == 0 {
		return ""
	}
	ks := make([]string, 0, len(m))
	for k, r := range m {
		if r.Known {
			ks = append(ks, k+"="+strconv.FormatFloat(r.Rate, 'g', -1, 64))
		}
	}
	if len(ks) == 0 {
		return ""
	}
	slices.Sort(ks)
	sum := sha256.Sum256([]byte(strings.Join(ks, ";")))
	return "|rates:" + hex.EncodeToString(sum[:8])
}

// KeyGroupRate is the group rate read from the relay for the key keyID,
// and whether one is known, for a page to show; any provider's.
func (p Provider) KeyGroupRate(keyID string) (float64, bool) {
	return readKeyRate(p.ID, keyID)
}

// readsKeyRates says p's keys have their group rates read: a provider of
// API keys the user gave a top-up rate.
func readsKeyRates(p Provider) bool {
	return p.TopUpRate > 0 && p.Account == nil && !p.Off && !p.Hidden
}

// keyRateOrigin is where p's relay is asked: the origin of its sub2api
// key query when it has one, else of the first base it names. "" for a
// host never asked (this computer, a vendor's own).
func keyRateOrigin(p Provider) string {
	if sub2APIKeyLimits(p) {
		u, _ := BalanceQuery(p)
		if pu, err := url.Parse(strings.TrimSpace(u)); err == nil && pu.Host != "" && !notProbed(pu.Hostname()) {
			return pu.Scheme + "://" + strings.ToLower(pu.Host)
		}
		return ""
	}
	if origins := probeOrigins(p); len(origins) > 0 {
		return origins[0]
	}
	return ""
}

// sub2APIBilling is the part of sub2api's reply read: schema 1's object
// and the rate it resolved for the key (its group's, or the user's own on
// that group).
type sub2APIBilling struct {
	Object                 string   `json:"object"`
	ResolvedRateMultiplier *float64 `json:"resolved_rate_multiplier"`
	GroupRateMultiplier    *float64 `json:"group_rate_multiplier"`
}

// parseKeyBilling is the rate in a reply of sub2api's billing query; an
// error for anything else.
func parseKeyBilling(b []byte) (float64, error) {
	var r sub2APIBilling
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, fmt.Errorf("not a sub2api billing reply: %w", err)
	}
	if r.Object != "sub2api.key_billing" {
		return 0, errors.New("not a sub2api billing reply")
	}
	rate := r.ResolvedRateMultiplier
	if rate == nil {
		rate = r.GroupRateMultiplier
	}
	if rate == nil || math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 {
		return 0, errors.New("the reply has no rate")
	}
	return *rate, nil
}

// askKeyRate asks origin's billing query with p's key.
func askKeyRate(ctx context.Context, p Provider, origin string) (float64, error) {
	ctx, cancel := context.WithTimeout(p.Via(ctx), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+sub2APIKeyBilling, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range AuthHeaders(p, Chat) {
		req.Header.Set(k, v)
	}
	for k, v := range p.SentHeaders(ctx) {
		req.Header.Set(k, withBalanceKey(v, p.Key, false))
	}
	req.Header.Set("Accept", "application/json")
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		return 0, fmt.Errorf("%s", res.Status)
	}
	return parseKeyBilling(b)
}

// keyRateJob is one key to read.
type keyRateJob struct {
	p      Provider // with the key as its Key
	key    string   // keyRateKey
	origin string
}

// keyRatesDue are the keys due a read at now.
func keyRatesDue(now time.Time) []keyRateJob {
	var jobs []keyRateJob
	for _, p := range All() {
		if !readsKeyRates(p) {
			continue
		}
		origin := keyRateOrigin(p)
		if origin == "" {
			continue
		}
		keys := []string{p.Key}
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				keys = append(keys, k.Key)
			}
		}
		for _, k := range keys {
			if k == "" {
				continue
			}
			id := keyRateKey(p.ID, keyID(k))
			keyRates.Lock()
			last, seen := keyRatesLocked()[id]
			keyRates.Unlock()
			wait := keyRateEvery
			if last.Err != "" {
				wait = keyRateRetry
			}
			if seen && last.Origin == origin && now.Sub(last.At) < wait {
				continue
			}
			q := p
			q.Key = k
			jobs = append(jobs, keyRateJob{q, id, origin})
		}
	}
	return jobs
}

// readKeyRatesDue reads every key that is due, one after another, and
// keeps what each answered. A failed read keeps the rate read before, if
// any, and is tried again sooner.
func readKeyRatesDue(ctx context.Context) {
	for _, j := range keyRatesDue(time.Now()) {
		if ctx.Err() != nil {
			return
		}
		rate, err := askKeyRate(ctx, j.p, j.origin)
		keyRates.Lock()
		m := keyRatesLocked()
		r := m[j.key]
		if r.Origin != j.origin {
			r = keyRateRead{} // another relay's rate is not this one's
		}
		r.At, r.Origin = time.Now(), j.origin
		if err != nil {
			r.Err = Mask(j.p.Key) + ": " + strings.ReplaceAll(err.Error(), j.p.Key, Mask(j.p.Key))
		} else {
			r.Rate, r.Known, r.Err = rate, true, ""
		}
		m[j.key] = r
		saveKeyRatesLocked()
		keyRates.Unlock()
	}
}

// KeepKeyRatesRead reads the group rate of every key whose provider has a
// top-up rate, when the magpie serving the gateway starts and again as
// each is due (every six hours, half an hour after a failed read), until
// ctx ends.
func KeepKeyRatesRead(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-keyRatesPoke:
		}
		readKeyRatesDue(ctx)
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(keyRateTick)
	}
}

// keyRatesPoke wakes KeepKeyRatesRead before its next tick.
var keyRatesPoke = make(chan struct{}, 1)

// ReadKeyRatesSoon has the keys due a read read now, not at the loop's
// next tick: a top-up rate the user just gave a provider.
func ReadKeyRatesSoon() {
	select {
	case keyRatesPoke <- struct{}{}:
	default:
	}
}
