package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

// A remote magpie's gateway keys (#1547, PennyVibe): a computer that uses
// another magpie with a gateway key that magpie gave it saw the other
// magpie's subscriptions on its Usage page, but not what the key itself
// may still spend there. Each key of a remote-magpie provider now has a
// card of its own: its limit, what it used and has left of it, the period
// and when it resets, or "no limit".
//
// The other magpie tells a key its own limit and nothing else
// (RemoteKeyLimitPath, answered by the gateway's keyLimit for the key the
// request carries, and only that one). So each key is asked with itself:
// a key never sees another key's numbers, here or there.

// RemoteKeyLimitPath is the gateway's answer of the calling key's limit.
const RemoteKeyLimitPath = "/v1/magpie/limit"

// remoteKeyCard is the card id the keys' cards go under, after the
// remote's ("office/~keys"): no provider id has a "~", so it never meets
// a card the remote has.
const remoteKeyCard = "~keys"

// errRemoteNoKeyLimit is a remote magpie too old to tell a key its limit.
const errRemoteNoKeyLimit = "remote magpie is too old to tell this key's limit; update magpie on that computer"

// GatewayKeyLimit is a remote gateway key's limit as its card carries it.
// Windows on the card show each cap; this says what they don't.
type GatewayKeyLimit struct {
	// Limited is false for a key the remote holds to no limit.
	Limited bool `json:"limited"`
	// Period is "day", "week", "month", "days" (a cycle of Days days) or
	// "hours" (of Hours hours).
	Period string    `json:"period,omitempty"`
	Days   int       `json:"days,omitempty"`
	Hours  int       `json:"hours,omitempty"`
	Start  time.Time `json:"start,omitzero"`
	Reset  time.Time `json:"reset,omitzero"`
	Calls  int       `json:"calls,omitempty"`
	// TokensLeft and CostLeft are what is left of each cap there is.
	TokensLeft *int64   `json:"tokensLeft,omitempty"`
	CostLeft   *float64 `json:"costLeft,omitempty"`
	// Spent: the remote refuses the key until Reset.
	Spent bool `json:"spent,omitempty"`
	// Unpriced calls add nothing to the cost, so it reads low.
	Unpriced int `json:"unpriced,omitempty"`
}

// remoteKeyAnswer is RemoteKeyLimitPath's answer (budget.Status, as the
// gateway's keyLimit sends it): Limit the one holding the key most,
// Limits each of its limits (#2227), from a magpie that has them.
type remoteKeyAnswer struct {
	Limited *bool            `json:"limited"`
	Key     string           `json:"key"`
	Limit   *remoteKeyLimit  `json:"limit"`
	Limits  []remoteKeyLimit `json:"limits"`
}

type remoteKeyLimit struct {
	Period     string    `json:"period"`
	Days       int       `json:"days"`
	Hours      int       `json:"hours"`
	Start      time.Time `json:"start"`
	Reset      time.Time `json:"reset"`
	Calls      int       `json:"calls"`
	Tokens     int64     `json:"tokens"`
	Cost       float64   `json:"cost"`
	Unpriced   int       `json:"unpriced"`
	TokenLimit int64     `json:"tokenLimit"`
	CostLimit  float64   `json:"costLimit"`
	TokensLeft int64     `json:"tokensLeft"`
	CostLeft   float64   `json:"costLeft"`
	Spent      bool      `json:"spent"`
	// Models are the models a limit counts, none for the whole key
	Models []string `json:"models"`
}

type remoteKeyRead struct {
	at     time.Time // when it was last asked
	readAt time.Time // when got was read; zero before any
	got    *remoteKeyAnswer
	err    string
}

var remoteKeyCache struct {
	sync.Mutex
	m map[string]remoteKeyRead // by provider id and key fingerprint
}

// remoteKeys are p's keys in use, each with the name its card goes by:
// the name the user gave it here, else the key masked; a name two keys
// share has each one's masked key after it.
func remoteKeys(p Provider) (names []string, keys []string) {
	on := p.KeysOn()
	seen := map[string]int{}
	for _, k := range on {
		seen[k.Name]++
	}
	for _, k := range on {
		n := k.Name
		if n == "" {
			n = Mask(k.Key)
		} else if seen[n] > 1 {
			n += " · " + Mask(k.Key)
		}
		names, keys = append(names, n), append(keys, k.Key)
	}
	return names, keys
}

// remoteKeyCards are p's keys' cards: each read from the remote when
// what is kept here is older than remoteCardsFor, or fresh is asked for
// the key named only (every key when only is "").
func remoteKeyCards(ctx context.Context, p Provider, fresh bool, only string) []SubscriptionQuota {
	names, keys := remoteKeys(p)
	out := make([]SubscriptionQuota, len(keys))
	var wg sync.WaitGroup
	var slots keySlots
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer slots.take(ctx, p.ID)()
			out[i] = remoteKeyCardOf(ctx, p, names[i], keys[i], fresh && (only == "" || only == names[i]))
		}()
	}
	wg.Wait()
	return out
}

func remoteKeyCardOf(ctx context.Context, p Provider, name, key string, fresh bool) SubscriptionQuota {
	id := p.ID + "\x00" + keyID(key)
	c := &remoteKeyCache
	c.Lock()
	r, ok := c.m[id]
	c.Unlock()
	if !ok || fresh || time.Since(r.at) >= remoteCardsFor {
		got, err := askRemoteKeyLimit(ctx, p, key)
		r.at = time.Now()
		if err != nil {
			// what was read before stays, dated: a failed read is not
			// "no limit" and not "nothing used"
			r.err = err.Error()
		} else {
			r.got, r.readAt, r.err = got, r.at, ""
		}
		c.Lock()
		if c.m == nil {
			c.m = map[string]remoteKeyRead{}
		}
		c.m[id] = r
		c.Unlock()
	}
	from := remoteName(p)
	q := SubscriptionQuota{Provider: p.ID + "/" + remoteKeyCard, Name: from, Icon: "magpie", User: name, Kind: "plan", From: from, Windows: []QuotaWindow{}}
	if r.got == nil {
		q.Error = r.err
		return q
	}
	at := r.readAt
	q.ReadAt = &at
	if r.err != "" {
		q.AsOf = &at
	}
	q.GatewayKey, q.Windows = keyLimitCard(r.got)
	return q
}

// keyLimitCard is what a key's card shows of the remote's answer: a
// window for each cap of each of its limits, named for its period, or one
// window with no limit. What the card says of the key itself is of the
// limit holding it most.
func keyLimitCard(a *remoteKeyAnswer) (*GatewayKeyLimit, []QuotaWindow) {
	l := a.Limit
	if !*a.Limited || l == nil {
		return &GatewayKeyLimit{}, []QuotaWindow{{Name: "Limit", Unlimited: true}}
	}
	g := &GatewayKeyLimit{Limited: true, Period: l.Period, Days: l.Days, Hours: l.Hours, Start: l.Start, Reset: l.Reset, Calls: l.Calls, Spent: l.Spent, Unpriced: l.Unpriced}
	all := a.Limits
	if len(all) == 0 { // a magpie from before #2227 tells one
		all = []remoteKeyLimit{*l}
	}
	var ws []QuotaWindow
	share := func(used, limit float64) float64 { return math.Min(100, math.Round(used/limit*1000)/10) }
	for _, x := range all {
		if len(x.Models) > 0 {
			// a limit of some models: a request for one of them is
			// refused with a 429 that rests that model here, and the
			// key's others are served; as a window it would read as the
			// key's
			continue
		}
		reset := x.Reset
		if x.TokenLimit > 0 {
			ws = append(ws, QuotaWindow{Name: keyWindowName("Tokens", x.Period), Used: share(float64(x.Tokens), float64(x.TokenLimit)), ResetsAt: &reset,
				Amount: float64(x.Tokens), Limit: float64(x.TokenLimit), Unit: "tokens"})
		}
		if x.CostLimit > 0 {
			ws = append(ws, QuotaWindow{Name: keyWindowName("Spend", x.Period), Used: share(x.Cost, x.CostLimit), ResetsAt: &reset,
				Amount: math.Round(x.Cost*100) / 100, Limit: x.CostLimit, Unit: "USD"})
		}
	}
	if l.TokenLimit > 0 {
		left := l.TokensLeft
		g.TokensLeft = &left
	}
	if l.CostLimit > 0 {
		left := l.CostLeft
		g.CostLeft = &left
	}
	if ws == nil { // limited, with no cap it tells: an answer we can't read
		return &GatewayKeyLimit{}, []QuotaWindow{{Name: "Limit", Unlimited: true}}
	}
	return g, ws
}

// keyWindowName is a cap's window as the GUI translates it: "Tokens
// today", "Spend this week", "Tokens this cycle".
func keyWindowName(what, period string) string {
	switch period {
	case "day":
		return what + " today"
	case "week":
		return what + " this week"
	case "month":
		return what + " this month"
	}
	return what + " this cycle"
}

// askRemoteKeyLimit asks p's gateway, with key, for that key's limit.
func askRemoteKeyLimit(ctx context.Context, p Provider, key string) (*remoteKeyAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Anthropic+RemoteKeyLimitPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	p.signRemote(req, key)
	res, err := p.Do(http.DefaultClient, req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", errRemoteUnreachable, remoteName(p), err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed:
		return nil, errors.New(errRemoteNoKeyLimit)
	case res.StatusCode == http.StatusForbidden:
		return nil, errors.New(errRemoteNotShared)
	case res.StatusCode == http.StatusUnauthorized:
		return nil, errors.New(errRemoteKey)
	case res.StatusCode < 200 || res.StatusCode >= 300:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s", remoteName(p), e.Error.Message)
		}
		return nil, fmt.Errorf("%s answered %s", remoteName(p), res.Status)
	}
	var a remoteKeyAnswer
	if err := json.Unmarshal(b, &a); err != nil || a.Limited == nil {
		// not the answer of a magpie's keyLimit: something else at that
		// address, or a reply cut short
		return nil, fmt.Errorf("%s answered its key's limit in a form magpie doesn't read", remoteName(p))
	}
	return &a, nil
}
