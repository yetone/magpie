package gateway

// Routing spreads a provider's requests over the keys or accounts it has
// on (see provider.Provider.Routing): smartly, in order, in turn, or the
// least used first. Whichever goes first, a key that suits the request
// still goes before one that doesn't, and one resting after a failure
// waits at the back for as long as its failure says: out of credit for
// half an hour, out of quota until it says it resets — or, for a
// subscription, until the window it filled does — rate limited until it
// says to try again, and otherwise a minute, longer each time it fails
// again. A rest's length and what set it go into the trace as they are, so
// what the Routing page says is when the account really comes back.

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// usageHalfLife is how fast the tokens a key served stop counting against
// it when choosing by use: half of them after an hour.
const usageHalfLife = time.Hour

var routed = struct {
	sync.Mutex
	turn     map[string]int      // provider → requests routed in turn
	used     map[string]tokenUse // candidate restKey → tokens it served
	failures map[string]int      // candidate restKey → failures since it last answered
}{turn: map[string]int{}, used: map[string]tokenUse{}, failures: map[string]int{}}

type tokenUse struct {
	n  float64
	at time.Time
}

func (u tokenUse) now(t time.Time) float64 {
	return u.n * math.Exp2(-t.Sub(u.at).Seconds()/usageHalfLife.Seconds())
}

// served counts what a candidate just answered against it, by who it is:
// rest for a key, an account by its user whichever the agent is signed in
// to (restKey, as key is), so a switch doesn't hand one account's count to
// another (#209).
func served(rest, key string, tokens int) {
	if tokens <= 0 {
		tokens = 1 // it answered, whether or not it said how much
	}
	now := time.Now()
	routed.Lock()
	routed.used[rest] = tokenUse{routed.used[rest].now(now) + float64(tokens), now}
	delete(routed.failures, rest)
	routed.Unlock()
	clearRest(key)
}

// servedCandidate records an answer and ends both the provider's rest and,
// when it has one, the candidate's model-specific rest.
func servedCandidate(c candidate, tokens int) {
	served(c.restKey(), c.restKey(), tokens)
	if id := c.restID(); id != c.restKey() {
		clearRest(id)
	}
}

// clearRest wakes a candidate that just answered, whether it was tried after
// a provider rest or after a model-specific rest.
func clearRest(key string) {
	restingUntil.Lock()
	delete(restingUntil.m, key)
	delete(restingUntil.note, key)
	restingUntil.Unlock()
}

// How long a candidate sits out, by why it failed.
const (
	creditRest  = 30 * time.Minute // out of credit: until someone tops it up
	quotaRest   = 15 * time.Minute // out of quota, with no word of when it resets
	longestWait = time.Hour        // the most a vendor's own "try again at" is trusted
	// longestQuota is the most an account out of quota sits out, when it
	// says when it's back: a week's window, and a day over
	longestQuota = 8 * 24 * time.Hour
	longestRetry = 10 * time.Minute // failing again and again
	// verifyRest: an account Google wants verified (#152) is out until
	// someone does, or lifts its rest in the app
	verifyRest = 30 * time.Minute
	// verifyHold is how long the last one left, refused for verification,
	// is answered that way by magpie without asking again: an agent's
	// reconnects don't all land on the account Google has stopped
	verifyHold = time.Minute
)

var (
	// creditWords: the account or key has no money left.
	creditWords = regexp.MustCompile(`(?i)insufficient.?(balance|credit|fund)|balance|credit|billing|payment|arrear|overdue|suspended|余额|欠费|充值|账户.*(不足|停)`)
	// quotaWords: it has used up what its plan allows for now.
	usedUpWords = regexp.MustCompile(`(?i)quota|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|exceeded.*(plan|limit)|额度|用量|套餐|上限`)
	// rateWords: a 429 that is a short rate limit — requests or tokens per
	// minute — which usedUpWords took for a used-up plan ("Rate limit
	// exceeded", "reached"): as magpie words an upstream error, with
	// "rate_limit_error" for its type, every such 429 was, and rested a
	// quarter of an hour rather than a minute (#153).
	rateWords = regexp.MustCompile(`(?i)rate.?limit|too many requests|per.?(second|sec|minute|min)\b|\b[rt]pm\b|频率|太频繁`)
	// plannedWords: a 429 that says the plan's own allowance is used, rate
	// words or not — a day's free requests, say.
	plannedWords = regexp.MustCompile(`(?i)quota|usage.?limit|hit your .*limit|limit.{0,24}resets|per.?(day|week|month)|daily|weekly|monthly|额度|用量|套餐`)
	// resetsWords: Claude Code's "usage limit reached|<when it resets>".
	resetsWords = regexp.MustCompile(`(?i)limit reached\|(\d{10})\b`)
)

// Why a candidate failed, as rest tells it.
const (
	failCredit = "credit"
	failQuota  = "quota"
	failRate   = "rate"
	failOther  = "other"
	// failCanceled: the agent went away before the answer came
	failCanceled = "canceled"
	// failForeign: the conversation's reasoning was sealed by another
	// account, and is sent again without it
	failForeign = "foreign"
	// failFloor: the request asked for a shorter reply than the provider
	// gives, and is sent again asking for the least it takes
	failFloor = "floor"
	// failVerify: the account must be verified with its vendor (Google's
	// VALIDATION_REQUIRED) before it is served again
	failVerify = "verify"
	// failRefused: the vendor's safety filter refused the request before
	// anything was said (#248) — the next one is asked, and nobody rests
	failRefused = "refused"
	// failShape: the vendor couldn't read the request's shape (#350) — the
	// next one is asked, and nobody rests
	failShape = "shape"
	// failProxy: the proxy magpie sends through (its own setting, the
	// *_PROXY variables, the system's) didn't take the connection (#381) —
	// nothing reached the vendor, so the next one is asked, and nobody rests
	failProxy = "proxy"
)

// proxyDown is the error Go gives when the proxy itself can't be reached,
// over HTTP (proxyconnect) or SOCKS (socks connect).
var proxyDown = regexp.MustCompile(`proxyconnect |socks connect `)

// failure says why a reply failed.
func failure(status int, body []byte) string {
	if status == http.StatusBadGateway && proxyDown.Match(body) {
		return failProxy
	}
	if _, ok := provider.Verification(body); ok && (status == 401 || status == 403) {
		return failVerify
	}
	switch {
	case status == 402, creditWords.Match(body) && status != 429 || strings.Contains(string(body), "insufficient_quota"):
		return failCredit
	case status == 429 && rateWords.Match(body) && !plannedWords.Match(body):
		return failRate
	case usedUpWords.Match(body), status == 429 && plannedWords.Match(body):
		return failQuota
	case status == 429:
		return failRate
	}
	return failOther
}

// openRouterSharedPool says an OpenRouter free model was refused by the
// provider's shared pool, rather than by OpenRouter's account-wide free tier.
func openRouterSharedPool(body []byte) bool {
	var reply struct {
		Error struct {
			Metadata struct {
				LimitSource string `json:"limit_source"`
			} `json:"metadata"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &reply) == nil && reply.Error.Metadata.LimitSource == "upstream_provider_shared_pool"
}

// Rest is why a candidate sits out after a failure, and until when.
type Rest struct {
	Why    string    `json:"why"`    // failCredit, failQuota, failRate, failOther
	Status int       `json:"status"` // what it answered
	Until  time.Time `json:"until"`
	// By is what set how long: "credit" (half an hour), "retry-after" (the
	// vendor's own headers), "resets" (the time Claude Code gave), "window"
	// (the allowance window it filled renews), "quota" (no word of when),
	// "cooldown" (a minute), "backoff" (longer each time it fails again).
	By       string `json:"by"`
	Failures int    `json:"failures,omitempty"` // in a row, for a backoff
	// Key is what it rests by, for the app to lift the rest (Unrest)
	Key string `json:"key,omitempty"`
	// Link: where the vendor said to verify the account, for failVerify
	Link string `json:"link,omitempty"`

	agent, user string    // the subscription account resting, when it is one
	said        string    // the error it gave, for a failVerify held
	hold        time.Time // until when a failVerify is answered without asking
}

// renewed lifts the rests of a subscription account whose windows were
// just started again (a Codex reset spent): out of quota no longer.
func renewed(agent, user string) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	for k, r := range restingUntil.note {
		if r.agent == agent && strings.EqualFold(r.user, user) {
			delete(restingUntil.m, k)
			delete(restingUntil.note, k)
		}
	}
}

func init() { provider.OnRenewed(renewed) }

// staleAllowance is provider.StaleAllowance, swapped in tests.
var staleAllowance = provider.StaleAllowance

// Unrest lifts the rest of what rests by key — an account just verified
// with its vendor, say — so the next request asks it again. False when it
// wasn't resting.
func (s *Server) Unrest(key string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	_, ok := restingUntil.m[key]
	delete(restingUntil.m, key)
	delete(restingUntil.note, key)
	return ok
}

// verifyHeld is the error to give again, without asking, for a candidate
// its vendor refused a moment ago until the account is verified.
func verifyHeld(key string) (string, bool) {
	r, ok := restOf(key)
	if !ok || r.Why != failVerify || r.said == "" || !time.Now().Before(r.hold) {
		return "", false
	}
	return r.said, true
}

// resetsIn is how long until a used-up ChatGPT account is back, as its
// refusal says: {"error":{"type":"usage_limit_reached","resets_at":<unix>,
// "resets_in_seconds":<n>}}. Zero when it doesn't say.
func resetsIn(body []byte, now time.Time) time.Duration {
	var e struct {
		Error struct {
			At int64 `json:"resets_at"`
			In int64 `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return 0
	}
	if t := time.Unix(e.Error.At, 0); e.Error.At > 0 && t.After(now) {
		return t.Sub(now)
	}
	return time.Duration(max(e.Error.In, 0)) * time.Second
}

// resetsAt is how long until a used-up subscription is back, as its
// refusal says: Claude Code's "usage limit reached|<unix>", or ChatGPT's
// resets_at / resets_in_seconds. Zero when it doesn't say.
func resetsAt(body []byte, now time.Time) time.Duration {
	if m := resetsWords.FindSubmatch(body); m != nil {
		if n, _ := strconv.ParseInt(string(m[1]), 10, 64); time.Unix(n, 0).After(now) {
			return time.Unix(n, 0).Sub(now)
		}
	}
	return resetsIn(body, now)
}

// restAfter sets a failed candidate aside for as long as its failure says.
func (s *Server) restAfter(c candidate, status int, header http.Header, body []byte) Rest {
	return s.restAfterMarked(c, status, header, body, openRouterSharedPool(body))
}

// restAfterMarked keeps an upstream routing fact through gateway error
// translation without putting it in the response sent to the client.
func (s *Server) restAfterMarked(c candidate, status int, header http.Header, body []byte, sharedPool bool) Rest {
	now := time.Now()
	d := fallbackCooldown
	why := failure(status, body)
	r := Rest{Why: why, Status: status, By: "cooldown"}
	if why == failProxy {
		// the account is as good as it was; the proxy is the user's to start
		return r
	}
	switch why {
	case failCredit:
		d, r.By = creditRest, "credit"
	case failQuota:
		// out of quota is out until the quota comes back: when the refusal
		// says (Claude Code's time, ChatGPT's resets_at — noted by
		// keepRetry once the error is put in words), else when the window
		// it filled renews, else as long as the vendor's headers ask — and
		// only a header is held to the longest wait, as a rate limit's
		// would be (#147: a ChatGPT account out until 21:34 read as back
		// "in 59 minutes", the resets_at turned Retry-After and cut to an
		// hour, then was tried, refused and benched again)
		d, r.By = quotaRest, "quota"
		if w := resetsAt(body, now); w > 0 {
			d, r.By = w, "resets"
		} else if w := resetsNoted(header, now); w > 0 {
			d, r.By = w, "resets"
		} else if t := c.full(now); !t.IsZero() {
			d, r.By = t.Sub(now), "window"
		} else if w := retryAfter(header, now); w > 0 {
			d, r.By = w, "retry-after"
		}
		d = min(d, longestQuota)
	case failRate:
		if w := retryAfter(header, now); w > 0 {
			d, r.By = w, "retry-after"
		}
	case failVerify:
		d, r.By = verifyRest, "verify"
		// the error as the agent was given it, and the link in it
		r.said, r.hold = provider.APIError(body, ""), now.Add(verifyHold)
		if !json.Valid(body) {
			link, _ := provider.Verification(body)
			r.said = provider.VerifyMessage(string(body), link)
		}
		r.Link, _ = provider.Verification([]byte(r.said))
	default:
		routed.Lock()
		routed.failures[c.restKey()]++
		n := routed.failures[c.restKey()]
		routed.Unlock()
		d, r.By, r.Failures = min(fallbackCooldown<<min(n-1, 10), longestRetry), "backoff", n
		// a subscription that failed with a window full is out of it,
		// whatever it said
		if t := c.full(now); !t.IsZero() {
			d, r.By = t.Sub(now), "window"
		}
	}
	// an account is kept by the agent its usage is asked of: a plugin's
	// by the plugin's provider ("plugin:grok"), not by "plugin"
	if a := c.p.Account; a != nil && why != failOther && why != failVerify {
		staleAllowance(a.UsageAgent(), a.User) // ask again what it has left
	}
	r.Until = now.Add(d)
	if a := c.p.Account; a != nil {
		r.agent, r.user = a.UsageAgent(), a.User
	}
	id := c.restKey()
	// OpenRouter identifies a provider's shared pool separately from its
	// account-wide free-tier limit. Only the former leaves sibling models ready.
	if why == failRate && c.isOpenRouterFree() && sharedPool {
		id = c.restID()
	}
	r.Key = id
	restingUntil.Lock()
	restingUntil.m[id] = r.Until
	restingUntil.note[id] = r
	restingUntil.Unlock()
	return r
}

// full is when a subscription whose allowance, as last known, has a window
// used up for the candidate's model renews; zero otherwise.
func (c candidate) full(now time.Time) time.Time {
	if c.p.Account == nil {
		return time.Time{}
	}
	return allowances(c.p.Account.UsageAgent())[c.p.Account.User].Full(c.model, usedShare, now)
}

// keepRetry passes on, with a vendor's error, what it said about when to
// try again: restAfter reads it to rest the candidate that long, and an
// agent given the error reads it too. When it said so in the error itself
// (ChatGPT's resets_at), that goes on as Retry-After.
func keepRetry(dst, src http.Header, body []byte) {
	for k, vs := range src {
		l := strings.ToLower(k)
		if l == "retry-after" || strings.Contains(l, "ratelimit") && strings.Contains(l, "reset") {
			dst[k] = vs
		}
	}
	now := time.Now()
	if d := resetsAt(body, now); d > 0 {
		// the error the agent gets says it in words, not resets_at: the
		// time goes on beside it, for restAfter to rest it that long
		dst.Set(resetsHeader, strconv.FormatInt(now.Add(d).Unix(), 10))
		if dst.Get("Retry-After") == "" {
			dst.Set("Retry-After", strconv.Itoa(int(d.Seconds())))
		}
	}
	if note, ok := policyRefusal(body); ok {
		// the error the agent gets keeps the vendor's words but not its
		// code (bio_policy): that it was the safety filter goes on beside
		// it, for settle to try the next account (#248)
		dst.Set(refusedHeader, note)
	}
}

// refusedHeader carries, from keepRetry to settle, that an error status
// was the safety filter refusing the request, and what it said.
const refusedHeader = "X-Magpie-Refused"

// resetsHeader carries, from keepRetry to restAfter, when a subscription
// out of quota said it's back. A held error that is passed on after all
// leaves it out; one written straight to the agent carries it, harmlessly.
const resetsHeader = "X-Magpie-Resets-At"

// resetsNoted is how long until the time keepRetry noted, if it did.
func resetsNoted(h http.Header, now time.Time) time.Duration {
	n, err := strconv.ParseInt(h.Get(resetsHeader), 10, 64)
	if err != nil || !time.Unix(n, 0).After(now) {
		return 0
	}
	return time.Unix(n, 0).Sub(now)
}

// retryAfter is when a vendor says to try again: Retry-After, in seconds
// or as a date, or the reset headers of OpenAI's and Anthropic's kind.
func retryAfter(h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			d = time.Duration(n * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
	}
	for k, vs := range h {
		k = strings.ToLower(k)
		if d > 0 || len(vs) == 0 || !strings.Contains(k, "reset") || !strings.Contains(k, "ratelimit") {
			continue
		}
		v := vs[0]
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			d = max(d, t.Sub(now))
		} else if w, err := time.ParseDuration(v); err == nil {
			d = max(d, w)
		} else if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 1e9 {
			d = max(d, time.Unix(n, 0).Sub(now))
		}
	}
	if d <= 0 {
		return 0
	}
	return min(d, longestWait)
}

var allowances = provider.Allowances

// Shares of an allowance past which an account is kept for when the others
// can't take a request: low, and all but used up — where the agent signed
// in to it is signed in to another, too (provider.SpentShare).
const (
	lowShare  = 90
	usedShare = provider.SpentShare
)

// route orders one provider's candidates as its routing says.
func route(p provider.Provider, cs []candidate, model string, from provider.Protocol) []candidate {
	cs, _ = weigh(p, cs, model, from)
	return cs
}

// weighing is what one provider's candidates were ordered by: the share of
// its allowance each account has used and when that renews, and the tokens
// each served lately. The routing trace shows the same.
type weighing struct {
	lefts map[allowanceKey]left // the allowance of each account and model

	tokens map[string]float64 // least used: tokens each served lately
}

type allowanceKey struct {
	rest, model string
}

func (c candidate) allowanceKey() allowanceKey {
	return allowanceKey{c.rest, c.model}
}

type left struct {
	used   float64
	renews []time.Time
}

// learns: c is a subscription whose allowance isn't known yet, of an agent
// that tells it as it answers (Claude Code's rate_limit_event).
func learns(c candidate, lefts map[allowanceKey]left) bool {
	_, known := lefts[c.allowanceKey()]
	return !known && c.p.Account != nil && c.p.Account.Agent == "claude"
}

// weigh orders one provider's candidates as its routing says, and tells
// what it went by.
func weigh(p provider.Provider, cs []candidate, model string, from provider.Protocol) ([]candidate, weighing) {
	var wg weighing
	if len(cs) < 2 {
		return cs, wg
	}
	// each account's own agent's: a group weighs accounts of several
	known := map[string]map[string]provider.Allowance{}
	now := time.Now()
	wg.lefts = map[allowanceKey]left{}
	for _, c := range cs {
		if c.p.Account == nil {
			continue
		}
		ag := c.p.Account.UsageAgent()
		if _, ok := known[ag]; !ok {
			known[ag] = allowances(ag)
		}
		if a, ok := known[ag][c.p.Account.User]; ok {
			u, r := a.For(c.model, now)
			wg.lefts[c.allowanceKey()] = left{u, r} // one not known counts as unused
		}
	}
	lefts := wg.lefts
	shareOf := func(c candidate) float64 { return lefts[c.allowanceKey()].used }
	switch p.Routing {
	case "":
		// of those with quota to spare, the one whose allowance renews
		// soonest, since what it has left is lost then, while one renewing
		// later keeps: the biggest window decides — the week, not the five
		// hours in it — and the next one only when that renews in the
		// same hour. Those alike stay in their order, keeping the vendor's
		// prompt cache warm. Past that, whichever has the most left, and
		// one all but used up only when nothing else can take it. Only the
		// windows that count the model do: Opus's own weekly allowance
		// being used up leaves Sonnet alone.
		var fine, low, spent []candidate
		for _, c := range cs {
			switch v := shareOf(c); {
			case v >= usedShare:
				spent = append(spent, c)
			case v >= lowShare:
				low = append(low, c)
			default:
				fine = append(fine, c)
			}
		}
		// one not known that tells what it has left as it answers goes
		// first, once: else, kept after those known, it would never answer
		// and never be known (Anthropic's usage endpoint can turn
		// magpie away for hours)
		sort.SliceStable(fine, func(i, j int) bool {
			if li, lj := learns(fine[i], lefts), learns(fine[j], lefts); li != lj {
				return li
			}
			ri, rj := lefts[fine[i].allowanceKey()].renews, lefts[fine[j].allowanceKey()].renews
			for k := 0; k < len(ri) || k < len(rj); k++ {
				var a, b time.Time // to the hour, so a few minutes don't reorder
				if k < len(ri) {
					a = ri[k].Truncate(time.Hour)
				}
				if k < len(rj) {
					b = rj[k].Truncate(time.Hour)
				}
				switch {
				case a.Equal(b):
					continue
				case a.IsZero() || b.IsZero(): // not known: after those known
					return b.IsZero()
				}
				return a.Before(b)
			}
			return false
		})
		for _, l := range [][]candidate{low, spent} {
			sort.SliceStable(l, func(i, j int) bool {
				return shareOf(l[i]) < shareOf(l[j])
			})
		}
		cs = append(append(fine, low...), spent...)
	case provider.Ordered:
		return cs, wg
	case provider.Rotate:
		routed.Lock()
		n := routed.turn[p.ID] % len(cs)
		routed.turn[p.ID]++
		routed.Unlock()
		cs = append(append([]candidate{}, cs[n:]...), cs[:n]...)
	case provider.LeastUsed:
		// a subscription by the share of its allowance used, as the vendor
		// says; then, and for keys, by what magpie sent it lately
		routed.Lock()
		tokens := make([]float64, len(cs))
		wg.tokens = map[string]float64{}
		for i, c := range cs {
			tokens[i] = routed.used[c.restKey()].now(now)
			wg.tokens[c.rest] = tokens[i]
		}
		routed.Unlock()
		idx := make([]int, len(cs))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			ca, cb := cs[idx[a]], cs[idx[b]]
			if sa, sb := shareOf(ca), shareOf(cb); sa != sb {
				return sa < sb
			}
			return tokens[idx[a]] < tokens[idx[b]]
		})
		out := make([]candidate, len(cs))
		for i, j := range idx {
			out[i] = cs[j]
		}
		cs = out
	default:
		return cs, wg
	}
	if p.Account == nil {
		sort.SliceStable(cs, func(i, j int) bool { return keyFit(cs[i].p, cs[i].model, from) < keyFit(cs[j].p, cs[j].model, from) })
	}
	return cs, wg
}
