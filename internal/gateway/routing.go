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
	provider.NoteServed(c.p, time.Now())
	if id := c.restID(); id != c.restKey() {
		routed.Lock()
		delete(routed.failures, id)
		routed.Unlock()
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
	// longestQuota is the most an account out of quota, or failing with a
	// window full, sits out, when it says when it's back: a week's window,
	// and a day over
	longestQuota = 8 * 24 * time.Hour
	longestRetry = 10 * time.Minute // failing again and again
	// longestRateRest is the most a rate limit that keeps coming back as
	// soon as its rest is over is waited out, when the vendor says nothing
	// of when (rateRest)
	longestRateRest = 30 * time.Minute
	// rateForget: a rate limit this long after the last one's rest ended
	// is a new one, a minute again
	rateForget = 30 * time.Minute
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
	// brokeWords: a 429 that says the balance itself is spent, as Zhipu
	// answers a GLM Coding Plan's key at the pay-as-you-go endpoint (1113
	// "余额不足或无可用资源包，请充值") — out of credit, not a rate limit
	brokeWords = regexp.MustCompile(`(?i)insufficient.?(balance|credit|fund)|余额不足|欠费|请充值`)
	// quotaWords: it has used up what its plan allows for now.
	usedUpWords = regexp.MustCompile(`(?i)quota|usage.?limit|out of budget|budget (exceeded|exhausted)|limit.?reached|hit your .*limit|limit.{0,24}resets|exceeded.*(plan|limit)|额度|用量|套餐|上限`)
	// rateWords: a 429 that is a short rate limit — requests or tokens per
	// minute — which usedUpWords took for a used-up plan ("Rate limit
	// exceeded", "reached"): as magpie words an upstream error, with
	// "rate_limit_error" for its type, every such 429 was, and rested a
	// quarter of an hour rather than a minute (#153).
	rateWords = regexp.MustCompile(`(?i)rate.?limit|too many requests|per.?(second|sec|minute|min)\b|\b[rt]pm\b|频率|太频繁`)
	// plannedWords: a 429 that says the plan's own allowance is used, rate
	// words or not — a day's free requests, say, or a sub2api key's own
	// 5-hour, day or 7-day limit ("api key 7天限额已用完", sent as
	// rate_limit_exceeded).
	plannedWords = regexp.MustCompile(`(?i)quota|usage.?limit|hit your .*limit|limit.{0,24}resets|per.?(day|week|month)|daily|weekly|monthly|额度|用量|套餐|限额已用完`)
	// resetsWords: Claude Code's "usage limit reached|<when it resets>".
	resetsWords = regexp.MustCompile(`(?i)limit reached\|(\d{10})\b`)
)

// Why a candidate failed, as rest tells it.
const (
	failCredit = "credit"
	failQuota  = "quota"
	failRate   = "rate"
	failOther  = "other"
	failAuth   = "auth"
	// failCanceled: the agent went away before the answer came
	failCanceled = "canceled"
	// failForeign: the conversation's reasoning was sealed by another
	// account, and is sent again without it
	failForeign = "foreign"
	// failFloor: the request asked for a shorter reply than the provider
	// gives, and is sent again asking for the least it takes
	failFloor = "floor"
	// failUpdate: the upstream turned away the configuration_update items
	// a changed effort went as (#617), and the request is sent again
	// without them
	failUpdate = "update"
	// failVerify: the account must be verified with its vendor (Google's
	// VALIDATION_REQUIRED) before it is served again
	failVerify = "verify"
	// failRefused: the vendor's safety filter refused the request before
	// anything was said (#248) — the next one is asked, and nobody rests
	failRefused = "refused"
	// failShape: the vendor couldn't read the request's shape (#350) — the
	// next one is asked, and nobody rests
	failShape = "shape"
	// failPrompt: the vendor turns away the agent's system prompt, as
	// WorkBuddy's "unapproved channel" does Claude Code's — the next one is
	// asked, its own mates last, and nobody rests
	failPrompt = "prompt"
	// failEffort: the account's plan doesn't take the reasoning level
	// asked for (#520) — another account is asked, and nobody rests
	failEffort = "effort"
	// failProxy: the proxy magpie sends through (its own setting, the
	// *_PROXY variables, the system's) didn't take the connection (#381) —
	// nothing reached the vendor, so the next one is asked, and nobody rests
	failProxy = "proxy"
	// failOverflow: the conversation is too long for the member's model
	// (#700) — another member, with room for it, is asked, and nobody rests
	failOverflow = "overflow"
	// failSlow: the member hadn't begun its reply when its group's
	// FirstToken ran out, nothing of it sent — the next is asked, and
	// nobody rests, as a long prompt is slow anywhere
	failSlow = "slow"
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
	case overflowed(status, body):
		// Kimi's "exceeded model token limit" is the conversation's
		// length, not the plan's
		return failOther
	case status == 402, creditWords.Match(body) && (status != 429 || brokeWords.Match(body)) || strings.Contains(string(body), "insufficient_quota"):
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

// failureOf says why c's reply failed: a Claude account's sign-in that
// Anthropic refused is told as one (provider/claude_auth.go), passed over
// till it is signed in again; another vendor saying the same words fails
// as failure says, and rests as before.
func failureOf(c candidate, status int, body []byte) string {
	if status >= 400 && c.p.Account != nil && c.p.Account.Agent == "claude" && provider.ClaudeSignInRequired(string(body)) {
		return failAuth
	}
	return failure(status, body)
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
	// "cooldown" (a minute), "backoff" (longer each time it fails again,
	// or is rate limited again as soon as it is back).
	By       string `json:"by"`
	Failures int    `json:"failures,omitempty"` // in a row, for a backoff
	// Key is what it rests by, for the app to lift the rest (Unrest)
	Key string `json:"key,omitempty"`
	// Link: where the vendor said to verify the account, for failVerify
	Link string `json:"link,omitempty"`

	// the subscription account resting, when it is one; agent "" and its
	// provider.KeyAllowanceID for a key out of its own windows
	agent, user string
	said        string    // the error it gave, for a failVerify held
	hold        time.Time // until when a failVerify is answered without asking
}

// renewed lifts the rests of a subscription account whose windows were
// just started again (a Codex reset spent, or a reading finding a window
// it was full in full no more): out of quota no longer. Only its rests out
// of quota, or failed with a window full ("window"), are lifted; one for a
// rate limit, its credit or a verification stays. Told agent "", it is a
// key (provider.KeyAllowanceID) whose windows a reading found full no
// more — its limit raised, or its usage reset — and its rest out of them
// is lifted; a key's other rests aren't noted by it.
func renewed(agent, user string) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	for k, r := range restingUntil.note {
		if r.agent == agent && strings.EqualFold(r.user, user) && (r.Why == failQuota || r.By == "window") {
			delete(restingUntil.m, k)
			delete(restingUntil.note, k)
		}
	}
}

func init() { provider.OnRenewed(renewed) }

// staleAllowance is provider.StaleAllowance, swapped in tests.
var staleAllowance = provider.StaleAllowance

// keyAllowance and staleKeyAllowance are provider.KeyAllowance and
// provider.StaleKeyAllowance, swapped in tests.
var (
	keyAllowance      = provider.KeyAllowance
	staleKeyAllowance = provider.StaleKeyAllowance
)

// RestOf is why what rests by key — a provider's id, or its id#key — is
// passed over now, while it is.
func RestOf(key string) (Rest, bool) { return restOf(key) }

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
	why := failureOf(c, status, body)
	// read before the allowance is asked again below: out of a pool that
	// counts some models only, the account's others are still in theirs
	// (Cursor's Auto with Other Models used up)
	pooled := why == failQuota && c.pooled(now)
	// a model the key's plan doesn't serve rests alone, counted by itself
	// (#1235): the key's other models are still served
	unserved := why == failOther && modelRefused(status, body)
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
		d, r.By, r.Failures = s.rateRest(c, header, body, sharedPool, now)
		// rate limited with quota left: to the back, where the order sinks
		if c.full(now).IsZero() && !(c.isOpenRouterFree() && sharedPool) {
			sink(c.restKey(), now)
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
		counted := c.restKey()
		if unserved {
			counted = c.restID()
		}
		routed.Lock()
		routed.failures[counted]++
		n := routed.failures[counted]
		routed.Unlock()
		d, r.By, r.Failures = min(fallbackCooldown<<min(n-1, 10), longestRetry), "backoff", n
		// a subscription that failed with a window full is out of it,
		// whatever it said; a key isn't: a relay out of accounts for
		// everyone (sub2api's 503) says nothing of the key's own windows.
		// Held to the longest an account out of quota sits out: the window
		// is the vendor's reading, and one read a year off (a /usage date
		// with no year) would bench the account until someone noticed
		if t := c.full(now); !t.IsZero() && c.p.Account != nil && !unserved {
			d, r.By = min(t.Sub(now), longestQuota), "window"
		}
	}
	// an account is kept by the agent its usage is asked of: a plugin's
	// by the plugin's provider ("plugin:grok"), not by "plugin"
	if a := c.p.Account; a != nil && why != failOther && why != failVerify {
		staleAllowance(a.UsageAgent(), a.User) // ask again what it has left
	} else if a == nil && why != failOther && why != failVerify {
		staleKeyAllowance(c.p) // a key that reads its windows, too
	}
	r.Until = now.Add(d)
	if a := c.p.Account; a != nil {
		r.agent, r.user = a.UsageAgent(), a.User
	} else if why == failQuota {
		// a key out of its own windows is back as soon as they are read
		// full no more, not only when they start again: its owner may
		// raise its limit, or reset its usage
		r.user = provider.KeyAllowanceID(c.p)
	}
	id := c.restKey()
	// OpenRouter identifies a provider's shared pool separately from its
	// account-wide free-tier limit. Only the former leaves sibling models ready.
	if why == failRate && c.isOpenRouterFree() && sharedPool {
		id = c.restID()
	}
	if pooled || unserved {
		id = c.restID()
	}
	r.Key = id
	restingUntil.Lock()
	restingUntil.m[id] = r.Until
	restingUntil.note[id] = r
	restingUntil.Unlock()
	return r
}

// rateRest is how long a rate-limited candidate sits out: as long as the
// vendor says (Retry-After, a reset header or a reset time in its error),
// and a minute when it says nothing. One limited again as soon as its rest
// is over sits out twice as long as the last time, up to longestRateRest,
// until it answers: a free model WorkBuddy keeps refusing with 429 while
// the account still has credits was asked every minute, a request lost
// each time before the next model of the group was (01huadalang). Failing
// again while still resting — tried anyway, the last one left — keeps the
// count where it is, so one request's own retries don't stretch it.
func (s *Server) rateRest(c candidate, header http.Header, body []byte, sharedPool bool, now time.Time) (time.Duration, string, int) {
	id := c.restKey()
	if c.isOpenRouterFree() && sharedPool {
		id = c.restID()
	}
	n := 1
	restingUntil.Lock()
	if prev, ok := restingUntil.note[id]; ok && prev.Why == failRate {
		until := restingUntil.m[id]
		switch {
		case now.Before(until):
			n = max(prev.Failures, 1)
		case now.Sub(until) < rateForget:
			n = max(prev.Failures, 1) + 1
		}
	}
	restingUntil.Unlock()
	d, by := min(fallbackCooldown<<min(n-1, 10), longestRateRest), "cooldown"
	if n > 1 {
		by = "backoff"
	}
	said := retryAfter(header, now)
	if w := resetsNoted(header, now); w > said {
		said = min(w, longestWait)
	}
	if w := resetsAt(body, now); w > said {
		said = min(w, longestWait)
	}
	if said > 0 && said >= d {
		d, by = said, "retry-after"
	}
	return d, by, n
}

// full is when a subscription — or a key that reads its own windows —
// whose allowance, as last known, has a window used up for the
// candidate's model renews; zero otherwise. Used up is all but
// (usedShare), except In order: there an account at 98% is still tried in
// its turn, and one that fails then isn't benched until its week renews
// unless the vendor said it was out (#530).
func (c candidate) full(now time.Time) time.Time {
	a, _ := c.allowance()
	return a.Full(c.model, provider.SpentShareOf(c.p.Routing), now)
}

// allowance is the candidate's allowance as last known: a subscription
// account's, or a key's own windows when magpie reads them (a sub2api
// key's limits, provider.KeyAllowance). ok is false when it isn't known.
func (c candidate) allowance() (provider.Allowance, bool) {
	if a := c.p.Account; a != nil {
		al, ok := allowances(a.UsageAgent())[a.User]
		return al, ok
	}
	return keyAllowance(c.p)
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
	soon   []time.Time // renews as Smart ranks them (Allowance.Renewal)
	pace   float64     // weekly pace: share of its week left per hour until it renews
	due    time.Time   // when the window that pace went by renews; zero when not known
	// restarts: when an auto-used reset starts its windows again
	// (Allowance.Restarts), zero when none will; dueRestart: due is that,
	// sooner than the window renews by itself
	restarts   time.Time
	dueRestart bool
	// the count used is the share of, when the vendor counts it so
	// (Allowance.Count): amount of of, in unit
	amount, of float64
	unit       string
}

// learns: c is a subscription whose allowance isn't known yet, of an agent
// that tells it as it answers (Claude Code's rate_limit_event).
func learns(c candidate, lefts map[allowanceKey]left) bool {
	_, known := lefts[c.allowanceKey()]
	return !known && c.p.Account != nil && c.p.Account.Agent == "claude"
}

// weigh orders one provider's candidates as its routing says, and tells
// what it went by: with Sink on, those rate limited with quota left behind
// the others (sink.go).
func weigh(p provider.Provider, cs []candidate, model string, from provider.Protocol) ([]candidate, weighing) {
	cs, wg := weighRouted(p, cs, model, from)
	if sinks(p.Sink, p.Routing) {
		cs = sinkLast(cs)
	}
	return cs, wg
}

// weighRouted is weigh by the routing alone.
func weighRouted(p provider.Provider, cs []candidate, model string, from provider.Protocol) ([]candidate, weighing) {
	var wg weighing
	if len(cs) == 0 {
		return cs, wg
	}
	// each account's own agent's: a group weighs accounts of several. One
	// alone is weighed against nobody, but what it has left is still
	// told: the Routing page shows it (#1016)
	known := map[string]map[string]provider.Allowance{}
	now := time.Now()
	wg.lefts = map[allowanceKey]left{}
	for _, c := range cs {
		var a provider.Allowance
		var ok bool
		if c.p.Account == nil {
			// a key that reads its own windows is weighed by them as an
			// account is; any other key isn't known
			a, ok = keyAllowance(c.p)
		} else {
			ag := c.p.Account.UsageAgent()
			if _, had := known[ag]; !had {
				known[ag] = allowances(ag)
			}
			a, ok = known[ag][c.p.Account.User]
		}
		if ok {
			u, r := a.For(c.model, now)
			pc, due := a.Pace(c.model, now)
			amt, of, unit := a.Count(c.model, now)
			rs := a.Restarts(now)
			wg.lefts[c.allowanceKey()] = left{used: u, renews: r, soon: a.Renewal(c.model, now), pace: pc, due: due, amount: amt, of: of, unit: unit,
				restarts: rs, dueRestart: !rs.IsZero() && due.Equal(rs)} // one not known counts as unused
		}
	}
	if len(cs) < 2 {
		return cs, wg
	}
	lefts := wg.lefts
	shareOf := func(c candidate) float64 { return lefts[c.allowanceKey()].used }
	switch p.Routing {
	case "":
		// of those with quota to spare, the one whose allowance renews
		// soonest, since what it has left is lost then, while one renewing
		// later keeps: the biggest window decides — the week, not the five
		// hours in it — and the next one only when that renews in the
		// same hour. An account with no week (Claude Enterprise's five
		// hours alone) goes by its five hours, so ahead of every week
		// but one renewing sooner; a window not started renews its whole
		// span from now (#576). A Codex account that spends a reset about
		// to run out by itself (auto-use on, its windows used) renews when
		// that is spent, if sooner: what it has left is lost there just the
		// same (#718). Only holding one, or one that runs out after the
		// week renews, changes nothing. Those alike stay in their order, keeping the vendor's
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
			ri, rj := lefts[fine[i].allowanceKey()].soon, lefts[fine[j].allowanceKey()].soon
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
	case provider.Weighted:
		cs = weightedFirst(p.ID, cs)
	case provider.LeastUsed:
		// a subscription, or a key that reads its own windows, by the
		// share of its allowance used, as the vendor says; then, and for
		// other keys, by what magpie sent it lately
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
	case provider.Pace:
		// a subscription by what it has left of its week per hour until
		// that renews, the most first: it has the most to lose at the
		// reset, where the most left alone (least used) sends a fresh
		// account ahead of one with 80% left and an hour to go, and the
		// soonest reset alone (smart) sends one with 3% left and an hour
		// to go ahead of one with 90% and two. The tiers are Smart's: one
		// at 90% or more of any window that counts the model — the five
		// hours too, a rate cap it would be sent into before its
		// allowance was read again — waits until the others can't answer,
		// one all but used up until nothing else can; each tier by the
		// share used. Among the rest, one not known that tells what it
		// has left as it answers goes first while it isn't, as in Smart,
		// else it would never be known; then the pace, those alike within
		// a tenth by what magpie sent them lately, then in their order,
		// keeping the vendor's prompt cache warm. An account not known
		// counts as a whole week ahead of it; a key has no week, unless it
		// reads its own windows (a sub2api key's limits). A Codex
		// account that spends a reset about to run out by itself goes by
		// the hours until then when that is sooner (Allowance.Pace, #717).
		paceOf := func(c candidate) float64 {
			if l, ok := lefts[c.allowanceKey()]; ok {
				return l.pace
			}
			if c.p.Account != nil {
				return provider.FreshPace
			}
			return 0
		}
		tier := func(share float64) int {
			switch {
			case share >= usedShare:
				return 2
			case share >= lowShare:
				return 1
			}
			return 0
		}
		// the bands of pace alike within a tenth, the highest first: drawn
		// before sorting, from each band's highest down, so that sorting
		// by them is consistent — "within a tenth of each other" alone is
		// not, three paces a twelfth apart each going round in a circle.
		// Drawn among those the pace orders alone — one running low
		// would otherwise set a band's top and part two alike behind it
		var byPace []int
		for i, c := range cs {
			if tier(shareOf(c)) == 0 && !learns(c, lefts) {
				byPace = append(byPace, i)
			}
		}
		sort.SliceStable(byPace, func(a, b int) bool { return paceOf(cs[byPace[a]]) > paceOf(cs[byPace[b]]) })
		band, top := make([]int, len(cs)), 0.0
		for k, i := range byPace {
			if p := paceOf(cs[i]); k == 0 || p < 0.9*top {
				top = p
				if k > 0 {
					band[i] = band[byPace[k-1]] + 1
					continue
				}
			} else {
				band[i] = band[byPace[k-1]]
			}
		}
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
			sa, sb := shareOf(ca), shareOf(cb)
			if ta, tb := tier(sa), tier(sb); ta != tb {
				return ta < tb
			} else if ta > 0 {
				if sa != sb {
					return sa < sb
				}
			} else if la, lb := learns(ca, lefts), learns(cb, lefts); la != lb {
				return la
			} else if band[idx[a]] != band[idx[b]] {
				return band[idx[a]] < band[idx[b]]
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
