package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What each of a search API's keys has left (SlouchyQuill on Discord):
// Tavily says it at GET /usage, by the key it is asked with. A read that
// fails is said as unknown with why, never as 0 used.

// SearchKeyQuota is what one key of a search API has used of its limit.
type SearchKeyQuota struct {
	Key string `json:"key"` // masked (provider.MaskSearchKey)
	Ref string `json:"ref"` // provider.SearchKeyRef, its row's in the list
	// Known: Used and Limit were read. Unlimited: read, and the key and
	// its plan have no limit (Used is what the key has used).
	Known     bool  `json:"known"`
	Unlimited bool  `json:"unlimited,omitempty"`
	Used      int64 `json:"used"`
	Limit     int64 `json:"limit"`
	// Of is what Limit is the limit of: "key", a limit set on the key
	// itself, or "plan", its account's plan (which every key of the
	// account spends)
	Of   string `json:"of,omitempty"`
	Plan string `json:"plan,omitempty"` // the plan's name
	// PaygoLeft is the pay-as-you-go credit still there past the plan's
	// limit, 0 when there is none
	PaygoLeft int64 `json:"paygoLeft,omitempty"`
	// UsedUp: Used reached Limit, with no pay-as-you-go credit left
	UsedUp bool `json:"usedUp,omitempty"`
	// Err is why it couldn't be read: Known is false
	Err string `json:"err,omitempty"`
	// Resting is why searches ask the key last now (searchKeyOrder), and
	// RestUntil until when; "" when they don't
	Resting   string    `json:"resting,omitempty"`
	RestUntil time.Time `json:"restUntil,omitzero"`
}

// readTavilyUsage reads Tavily's GET /usage reply into what the key has
// left: the key's own limit when it has one, else its account plan's (the
// plan's usage beside it, not the key's, as the account's other keys spend
// it too). key.limit is null for a key with no limit of its own, which a
// free account's keys are; with plan_limit null too it has none. A reply
// missing what it takes, or saying it in a way not known, is an error:
// unknown, never 0.
func readTavilyUsage(b []byte) (SearchKeyQuota, error) {
	var u struct {
		Key, Account map[string]json.RawMessage
	}
	if err := json.Unmarshal(b, &u); err != nil {
		return SearchKeyQuota{}, err
	}
	if u.Key == nil {
		return SearchKeyQuota{}, errors.New("no key in the reply")
	}
	// num is a field's number; null is whether it is null; ok is false
	// when it is missing or neither
	num := func(o map[string]json.RawMessage, f string) (n int64, null, ok bool) {
		v, there := o[f]
		if !there {
			return 0, false, false
		}
		if string(v) == "null" {
			return 0, true, true
		}
		var x float64
		if json.Unmarshal(v, &x) != nil {
			return 0, false, false
		}
		return int64(x), false, true
	}
	var q SearchKeyQuota
	json.Unmarshal(u.Account["current_plan"], &q.Plan)
	if pl, null, ok := num(u.Account, "paygo_limit"); ok && !null {
		if pu, null, ok := num(u.Account, "paygo_usage"); ok && !null {
			q.PaygoLeft = max(0, pl-pu)
		}
	}
	keyUsed, keyUsedNull, keyUsedOK := num(u.Key, "usage")
	keyLimit, keyLimitNull, ok := num(u.Key, "limit")
	if !ok {
		return SearchKeyQuota{}, errors.New("no key limit in the reply")
	}
	switch planLimit, planLimitNull, planOK := num(u.Account, "plan_limit"); {
	case !keyLimitNull:
		if !keyUsedOK || keyUsedNull {
			return SearchKeyQuota{}, errors.New("no key usage in the reply")
		}
		q.Used, q.Limit, q.Of = keyUsed, keyLimit, "key"
	case planOK && !planLimitNull:
		planUsed, null, ok := num(u.Account, "plan_usage")
		if !ok || null {
			return SearchKeyQuota{}, errors.New("no plan usage in the reply")
		}
		q.Used, q.Limit, q.Of = planUsed, planLimit, "plan"
	case planOK && planLimitNull:
		q.Unlimited, q.Used = true, keyUsed
	default:
		return SearchKeyQuota{}, errors.New("no plan limit in the reply")
	}
	q.Known = true
	// a key's own limit is a hard stop; a plan's goes on into
	// pay-as-you-go while there is some
	q.UsedUp = !q.Unlimited && q.Used >= q.Limit && (q.Of == "key" || q.PaygoLeft == 0)
	return q, nil
}

// searchQuotaKeep is how long a quota read is kept before it is read
// again: Tavily allows /usage 10 times in 10 minutes.
const searchQuotaKeep = 2 * time.Minute

type keptQuota struct {
	at time.Time
	q  SearchKeyQuota
}

// searchQuotas is, by searchKeyID, the last quota read of each key.
var searchQuotas sync.Map

// quotaClient asks for quotas through the proxy settings searches use.
var quotaClient = &http.Client{Transport: upstreamTransport()}

// CanReadSearchQuota is whether magpie can read what a search API's keys
// have left: Tavily's, with a key.
func CanReadSearchQuota(a provider.SearchAPI) bool {
	return a.Vendor == "tavily" && len(a.Keys()) > 0
}

// SearchQuotas reads what each of a search API's keys has left, in the
// order of its keys, nil for an API magpie can't ask (CanReadSearchQuota).
// A read newer than searchQuotaKeep is used again unless fresh is set. A
// key it finds used up rests (a search asks it last); one with credit
// that rests for being out of it wakes.
func SearchQuotas(ctx context.Context, a provider.SearchAPI, fresh bool) []SearchKeyQuota {
	if !CanReadSearchQuota(a) {
		return nil
	}
	keys := a.Keys()
	out := make([]SearchKeyQuota, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Go(func() { out[i] = searchQuota(ctx, a, k, fresh) })
	}
	wg.Wait()
	now := time.Now()
	for i, k := range keys {
		if r, ok := searchKeyRest(a.Vendor, k, now); ok {
			out[i].Resting, out[i].RestUntil = r.Why, r.Until
		}
	}
	return out
}

// searchQuota is one key's quota, read or kept.
func searchQuota(ctx context.Context, a provider.SearchAPI, key string, fresh bool) SearchKeyQuota {
	id := searchKeyID(a.Vendor+" "+a.Base(), key)
	if v, ok := searchQuotas.Load(id); ok && !fresh && time.Since(v.(keptQuota).at) < searchQuotaKeep {
		return v.(keptQuota).q
	}
	q, err := askTavilyUsage(ctx, a, key)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), key, provider.MaskSearchKey(key))
		q = SearchKeyQuota{Err: msg}
	}
	q.Key, q.Ref = provider.MaskSearchKey(key), provider.SearchKeyRef(key)
	searchQuotas.Store(id, keptQuota{time.Now(), q})
	switch {
	case q.UsedUp:
		restSearchKey(a.Vendor, key, outOfCreditRest, "used up", time.Now())
	case q.Known:
		if r, ok := searchKeyRest(a.Vendor, key, time.Now()); ok && (r.Why == "used up" || r.Why == "out of credit") {
			wakeSearchKey(a.Vendor, key)
		}
	}
	return q
}

// askTavilyUsage asks Tavily's /usage with one key; the key goes only to
// the address the API is set at, as a search's does.
func askTavilyUsage(ctx context.Context, a provider.SearchAPI, key string) (SearchKeyQuota, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base()+"/usage", nil)
	if err != nil {
		return SearchKeyQuota{}, err
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	c := *quotaClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return SearchKeyQuota{}, errors.New("no answer in time")
		}
		return SearchKeyQuota{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return SearchKeyQuota{}, errors.New(provider.APIError(b, res.Status))
	}
	q, err := readTavilyUsage(b)
	if err != nil {
		return SearchKeyQuota{}, fmt.Errorf("unexpected reply: %w", err)
	}
	return q, nil
}
