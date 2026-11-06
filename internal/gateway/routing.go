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
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// usageHalfLife is how fast the tokens a key served stop counting against
// it when choosing by use: half of them after an hour.
const usageHalfLife = time.Hour

var routed = struct {
	sync.Mutex
	used     map[string]tokenUse  // candidate restKey → tokens it served
	failures map[string]int       // candidate restKey → failures since it last answered
	slow     map[string]slowRun   // candidate restID → its slow starts since it last answered (restSlow)
	answered map[string]time.Time // provider ID → when one of its candidates last answered (answeringLately)
}{used: map[string]tokenUse{}, failures: map[string]int{}, slow: map[string]slowRun{}, answered: map[string]time.Time{}}

// slowRun is a model's slow starts in a row on one account or key, and
// when the last of them was.
type slowRun struct {
	n  int
	at time.Time
}

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
	probeAnswered(key)
}

// servedCandidate records an answer and ends both the provider's rest and,
// when it has one, the candidate's model-specific rest.
func servedCandidate(c candidate, tokens int) {
	served(c.restKey(), c.restKey(), tokens)
	routed.Lock()
	routed.answered[c.p.ID] = time.Now()
	routed.Unlock()
	provider.NoteServed(c.p, time.Now())
	provider.Unretire(c.p.ID, c.model) // back in service, if it was said retired
	if id := c.restID(); id != c.restKey() {
		routed.Lock()
		delete(routed.failures, id)
		delete(routed.slow, id)
		routed.Unlock()
		clearRest(id)
	}
}

// forbidden is a plain 403, or a plain 5xx (plainUnavailable), one
// request was given, and the rest it took: what forbiddenAlike and
// unforbid read.
type forbidden struct {
	c     candidate
	rest  Rest
	try   int    // its index in the route's Tries, -1 untraced
	words string // a 5xx's status and words (refusalWords); "" for a 403
}

// plainForbidden says a reply is a 403 that says nothing of why: no
// quota, credit, rate, verification, restriction, a model not served.
// Such a 403 may be the key's or the request's.
func plainForbidden(c candidate, status int, body []byte) bool {
	return status == http.StatusForbidden && failureOf(c, status, body) == failOther &&
		!modelRefused(status, body) && !codeAssistModelMissing(c.p, status, body)
}

// plainUnavailable says a reply is a 500, 502 or 503 that says nothing
// the account is at fault for, nor that the vendor is busy: no quota,
// credit, rate, overload (vendorOverloaded), no proxy down, not ChatGPT's
// reset (codexReset) or its protection refusal, each read on its own
// path. Such a 5xx may be an outage or the request's: an upstream, or a
// plugin putting its upstream's error in its own words, refusing a
// request it will always refuse (mycf_711 on Discord: 127 of one
// provider's 128 503s, "AIError AI服务异常" word for word, were one
// agent's request with an empty tool schema, and each walked every
// account into backoff). forbiddenAlike tells them apart: the same words
// from two accounts, while the provider answers other requests.
func plainUnavailable(c candidate, status int, body []byte) bool {
	switch carriedStatus(status, body) {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
	default:
		return false
	}
	return failureOf(c, status, body) == failOther && !vendorOverloaded(status, body) &&
		!codexReset(status, body) && !protectionRefused(status, body) &&
		!bytes.Contains(body, []byte(didNotStream)) && refusalWords(status, body) != ""
}

// idRun is a run in an error's words that differs from one try to the
// next: a request or trace id, a timestamp.
var idRun = regexp.MustCompile(`[0-9a-fA-F][0-9a-fA-F-]{11,}|\d{6,}`)

// refusalWords is a 5xx's status and the words it said, ids left out
// (idRun), for two tries' to be compared; "" when it said nothing, as a
// bare 502 from a proxy down does, which proves nothing alike.
func refusalWords(status int, body []byte) string {
	w := strings.Join(strings.Fields(string(wordsOf(body))), " ")
	if w == "" {
		return ""
	}
	w = idRun.ReplaceAllString(w, "#")
	if len(w) > 512 {
		w = w[:512]
	}
	return strconv.Itoa(carriedStatus(status, body)) + " " + w
}

// answeringLately is how recently a provider must have answered a request
// for the same 5xx from two of its accounts to read as the request's: a
// provider answering others is up. One that answered nothing in it may be
// down, and rests as before, so an outage is backed off once this has
// passed since its last answer.
const answeringLately = 5 * time.Minute

// answeredLately says one of provider id's candidates answered a request
// within answeringLately.
func answeredLately(id string) bool {
	routed.Lock()
	at := routed.answered[id]
	routed.Unlock()
	return !at.IsZero() && time.Since(at) < answeringLately
}

// forbiddenAlike is the plain 403s of c's mates earlier in the request:
// another key or account of its provider, asked the same model at the
// same reasoning. Two of them refusing one request alike, it is the
// request that is refused, not each account (plugins#243,
// zygs1083-dotcom: Factory's 403 for a Claude Code wrapper it refused
// rested every account in turn, and the next requests found them all in
// backoff). A plain 5xx (words, refusalWords) is alike only to one of
// the same status and words; a 403 (words "") only to a 403.
// The rest of seen is returned beside them, for the request to keep.
func forbiddenAlike(seen []forbidden, c candidate, words string) (alike, others []forbidden) {
	for _, f := range seen {
		if f.c.p.ID == c.p.ID && f.c.model == c.model && f.c.effort == c.effort && f.c.who() != c.who() && f.words == words {
			alike = append(alike, f)
		} else {
			others = append(others, f)
		}
	}
	return alike, others
}

// unforbid takes back the rest a plain 403 took and the failures it
// counted, when a mate's alike 403 said the request was what was refused.
// A rest taken since, which isn't this one, stays.
func unforbid(f forbidden) {
	if f.rest.By == "off" || f.rest.Key == "" {
		return // it neither rested nor counted
	}
	routed.Lock()
	for _, k := range []string{f.c.restKey(), f.c.restID()} {
		if n := routed.failures[k]; n > 1 {
			routed.failures[k] = n - 1
		} else {
			delete(routed.failures, k)
		}
		if f.c.model == "" {
			break // restID is restKey: counted once
		}
	}
	routed.Unlock()
	restingUntil.Lock()
	if until, ok := restingUntil.m[f.rest.Key]; ok && until.Equal(f.rest.Until) {
		delete(restingUntil.m, f.rest.Key)
		delete(restingUntil.note, f.rest.Key)
	}
	restingUntil.Unlock()
}

// clearRest wakes a candidate that just answered, whether it was tried after
// a provider rest or after a model-specific rest.
func clearRest(key string) {
	restingUntil.Lock()
	kept := ladderRest(restingUntil.note[key])
	delete(restingUntil.m, key)
	delete(restingUntil.note, key)
	restingUntil.Unlock()
	if kept {
		saveRests() // its rung starts over after a restart too
	}
}

// How long a candidate sits out, by why it failed.
const (
	creditRest  = 30 * time.Minute // out of credit: until someone tops it up
	quotaRest   = 15 * time.Minute // out of quota, with no word of when it resets
	longestWait = time.Hour        // the most a vendor's own "try again at" is trusted
	// chatgptCreditRest is how long a ChatGPT account out of its credits
	// sits out when neither the refusal nor its reading says when it can
	// answer again: its credits come with the plan, by the month, and its
	// own allowance is back as its five hours renew (PaulT on Discord: a
	// credits-only account was asked every half hour, 402 each time)
	chatgptCreditRest = 5 * time.Hour
	// longestQuota is the most an account out of quota, or failing with a
	// window full, sits out, when it says when it's back: a week's window,
	// and a day over
	longestQuota = 8 * 24 * time.Hour
	longestRetry = 10 * time.Minute // failing again and again
	// overloadRest: a vendor too busy for the request (vendorOverloaded)
	// rests the model on that account this long when it says nothing of
	// when, and no longer than overloadRestMost when it does: the same
	// every time, never a step on the backoff ladder
	overloadRest     = 30 * time.Second
	overloadRestMost = time.Minute
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
	// restrictedRest: an account its vendor restricted (WorkBuddy's 403
	// 11140, #1849) is out until the vendor lifts it, which takes an
	// appeal, not a wait: hours, not the minutes of a backoff, so a group
	// stops walking every restricted account on every request. Two hours
	// costs one refused request per account every two hours, and a
	// restriction lifted (or a refusal read wrong) is back the same
	// afternoon without anyone acting; the Routing page lifts it at once
	// (Unrest) for a user whose appeal went through
	restrictedRest = 2 * time.Hour
	// slowRests is how many slow starts in a row (restSlow) rest a model
	// on an account or key, and slowForget how long after the last one
	// the next starts a new run
	slowRests  = 2
	slowForget = 30 * time.Minute
)

// creditLadder is how long a key its vendor says is out of money sits
// out, by how many times in a row it was refused so (creditBackoff): a
// balance spent, or a spend limit reached, comes back when someone tops
// it up or raises the limit, or when a month's limit turns, not in half
// an hour (PaulT on Discord: an OpenAI key's 402 had it asked, and
// refused, every 30 minutes). The first rest is creditRest still, as a
// relay's 402 can be its own moment's; each refusal after a rest ended
// rests it longer, up to a week. "Try now" lifts it whenever the user
// has topped it up, and an answer clears the count.
var creditLadder = []time.Duration{creditRest, 2 * time.Hour, 8 * time.Hour, 24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour}

// creditSoon: an out-of-credit refusal that says it can pass soon, or
// that a smaller request would: OpenRouter's in-flight budget (a 402
// with Retry-After, "a wait-and-retry case", openrouter.ai/docs/
// api-reference/errors), its "requires more credits, or fewer
// max_tokens … can only afford N" (the request's size, not the balance
// spent), and a vendor's "try again in 30 seconds" or "1 分钟后重试".
// These rest creditRest, as before; they don't climb creditLadder.
var creditSoon = regexp.MustCompile(`(?i)openrouter_in_flight_budget|can only afford|fewer max_tokens|(?:try again|retry)(?: after| in)?\s*\d|\d+\s*(?:秒|分钟|小时)[后後]`)

// creditBackoff is the rest of a key (c, no subscription account) out of
// credit, rested by id: the vendor's own time when it names one (a
// Retry-After, a reset in its body or headers), creditRest when it says
// it can pass soon (creditSoon), else the next rung of creditLadder —
// the same rung while the last rest still runs (the lone key asked all
// the same), one up once it has ended. n is the rung, 0 off the ladder.
func creditBackoff(id string, header http.Header, body []byte, now time.Time) (d time.Duration, by string, n int, guessed bool) {
	if w := resetsAt(body, now); w > 0 {
		return min(w, longestMonthly), "resets", 0, false
	}
	if w := resetsNoted(header, now); w > 0 {
		return min(w, longestMonthly), "resets", 0, false
	}
	if retryAfter(header, now) > 0 || creditSoon.Match(wordsOf(body)) {
		return creditRest, "credit", 0, true
	}
	n = 1
	restingUntil.Lock()
	if prev, ok := restingUntil.note[id]; ok && prev.Why == failCredit && prev.Failures > 0 {
		// its rest gone rather than ended (lifted) starts a new run
		if until, had := restingUntil.m[id]; had && now.Before(until) {
			n = prev.Failures
		} else if had {
			n = prev.Failures + 1
		}
	}
	restingUntil.Unlock()
	return creditLadder[min(n, len(creditLadder))-1], "credit", n, true
}

var (
	// creditWords: the account or key has no money left.
	creditWords = regexp.MustCompile(`(?i)insufficient.?(balance|credit|fund)|balance|credit|billing|payment|arrear|overdue|suspended|余额|欠费|充值|账户.*(不足|停)`)
	// brokeWords: a 429 that says the balance itself is spent, as Zhipu
	// answers a GLM Coding Plan's key at the pay-as-you-go endpoint (1113
	// "余额不足或无可用资源包，请充值") — out of credit, not a rate limit;
	// WorkBuddy's 429 14018 "Credits exhausted. Please visit the link
	// below to purchase add-on packs…" too (Creat.wu on Discord), and
	// OpenAI's 429 credit_balance_exhausted for an organization whose
	// prepaid API credits are spent: its underscores kept "credits
	// exhausted" from matching, so it read as a rate limit and the same
	// key was asked again rather than the next account
	brokeWords = regexp.MustCompile(`(?i)insufficient.?(balance|credit|fund)|\bcredits? (are )?exhausted\b|credit.?balance.?exhausted|余额不足|欠费|请充值`)
	// quotaWords: it has used up what its plan allows for now. The budget's
	// words needn't sit beside each other: LiteLLM's spent key answers 429
	// "Budget has been exceeded! Key=… (sk-…), Current cost: …, Max budget:
	// …" (#5111, wuxiaoqiang12) — its "has been" broke the pair, and read
	// as a plain rate limit the key was asked again every half an hour, the
	// user's quota-rest never read
	usedUpWords = regexp.MustCompile(`(?i)quota|usage.?limit|out of budget|budget.{0,32}(exceeded|exhausted)|limit.?reached|hit your .*limit|limit.{0,24}resets|exceeded.*(plan|limit)|额度|用量|套餐|上限`)
	// rateWords: a 429 that is a short rate limit — requests or tokens per
	// minute — which usedUpWords took for a used-up plan ("Rate limit
	// exceeded", "reached"): as magpie words an upstream error, with
	// "rate_limit_error" for its type, every such 429 was, and rested a
	// quarter of an hour rather than a minute (#153).
	// "Frequency limit" is the English of 频率 (zoecats on Discord).
	rateWords = regexp.MustCompile(`(?i)rate.?limit|frequency.?limit|too many requests|per.?(second|sec|minute|min)\b|\b[rt]pm\b|频率|太频繁|过于频繁|速率`)
	// plannedWords: a 429 that says the plan's own allowance is used, rate
	// words or not — a day's free requests, say, or a sub2api key's own
	// 5-hour, day or 7-day limit ("api key 7天限额已用完", sent as
	// rate_limit_exceeded).
	plannedWords = regexp.MustCompile(`(?i)quota|usage.?limit|hit your .*limit|limit.{0,24}resets|per.?(day|week|month)|daily|weekly|monthly|额度|用量|套餐|限额已用完`)
	// minuteWords: a 429 that names a window of a minute or less, or the
	// rate itself — tokens or requests per minute, TPM, RPM — which some
	// vendors call a quota all the same: Gemini's "You exceeded your
	// current quota … quotaId GenerateContentPaidTierInputTokensPerModel
	// PerMinute … Please retry in 33s", Vertex's "Quota exceeded for
	// …requests_per_minute_per_project…", Azure's "exceeded token rate
	// limit". Read as a used-up plan, each rested the quarter of an hour
	// of a quota that says nothing of when, with balance left
	// (monater.114514 on Discord). A longer window named beside it
	// (longWords) is still the plan's.
	minuteWords = regexp.MustCompile(`(?i)per.?(second|minute)|per.?(sec|min)\b|\b[rt]p[ms]\b|(token|call|request) rate limit|每分钟|每秒`)
	// longWords: a window longer than a minute, or a plan's own limit,
	// named in a 429 — what minuteWords' rate limit is not.
	longWords = regexp.MustCompile(`(?i)usage.?limit|hit your .*limit|limit.{0,24}resets|per.?(hour|day|week|month)|\bhours?\b|hourly|daily|weekly|monthly|\b[rt]p[dh]\b|额度已用|用量|套餐|限额已用完|小时|每天|每日|每周|每月`)
	// urlWords: a link in an error, whose words are its path, not what the
	// vendor said: Azure's rate limit points to aka.ms/oai/quotaincrease
	urlWords = regexp.MustCompile(`https?://\S+`)
	// resetsWords: Claude Code's "usage limit reached|<when it resets>".
	resetsWords = regexp.MustCompile(`(?i)limit reached\|(\d{10})\b`)
	// resetsWhen: a 429 that says in words when the account is back, as
	// WorkBuddy's do in both its builds (#1669): "将在 2026-10-11 20:16:02
	// UTC+8重置", "your usage will reset at 2026-10-11 12:37:53 UTC+8, ...".
	// Volcengine Ark's monthly one writes its time as Go prints one, the
	// offset as four digits and the zone's name after it (#3251 CNife,
	// #3276 wyx1818): "It will reset at 2026-10-25 23:59:59 +0800 CST.";
	// the offset is read, the name (CST is more than one zone) isn't.
	// Only the reset phrasing right beside a whole date and time with its
	// offset is read, and the offset must be followed by what follows it
	// there (重置, or a comma or full stop), so a refusal cut short after
	// "UTC", "UTC+" or Ark's "+0800" isn't read as UTC: another vendor's
	// date in passing ("deprecated on …"), or one with no offset, is not a
	// reset time. The offset may sit in brackets, as WorkBuddy's China
	// build writes it since November (taoxiaoxin on Discord: "将在
	// 2026-11-06 17:13:04 (UTC+8) 重置"): unread, the refusal rested the
	// whole account a quarter of an hour as out of quota.
	resetsWhen = regexp.MustCompile(`(?i)(?:\bresets? at|将在|将于)\s*(\d{4})-(\d{1,2})-(\d{1,2})[ T](\d{1,2}):(\d{2}):(\d{2})\s*[(（]?\s*(?:(?:UTC|GMT)([+-])(\d{1,2})(?::?(\d{2}))?|([+-])(\d{2})(\d{2}) (?-i:[A-Z]{2,5}))\s*[)）]?(?:\s*(?:重置|[,.;，。；]))`)
	// monthlyWords: a refusal that says the allowance it used up is a
	// month's, as Ark's does ("You have exceeded the monthly usage quota")
	// and Zhipu's 1310 ("Weekly/Monthly Limit Exhausted"): the reset time
	// it writes can honestly be weeks off (longestSaid).
	monthlyWords = regexp.MustCompile(`(?i)\bmonthly\b|月度|每月|本月`)
	// otherModels: a refusal that says the account's other models still
	// answer ("切换其他模型", "switch to the other models"): its limit is
	// the model's, and the model alone rests
	otherModels = regexp.MustCompile(`(?i)switch(?:ing)? to (?:the )?other models?|切换(?:到)?其他模型`)
	// modelCooling is a relay that pools credentials per model saying
	// those of the requested model cooled down: CLIProxyAPI's 429
	// model_cooldown, "All credentials for model model-a are cooling down
	// via provider upstream" (#3039, Welkon's relay), or OmniRoute's "All
	// credentials for the requested model are cooling down"
	modelCooling = regexp.MustCompile(`(?i)credentials for (?:the requested )?model .{0,200}?cooling down`)
	// clineFreeLimit: Cline's refusal of a free model whose day's allowance
	// is used up, 429 "Daily free limit reached on model deepseek/deepseek-
	// v4-flash. Try again in 23h 59m", as its own clients read it (cline/
	// cline sdk/packages/llms/src/providers/errors.ts,
	// isClineFreeModelLimitMessage, which tells the user to pick another
	// model): the limit is that model's, and the model alone rests
	// (#1398, wuasakura)
	clineFreeLimit = regexp.MustCompile(`(?i)free limit reached on model`)
	// tryAgainIn: how long Cline's free-model refusal says until it is
	// back ("Try again in 23h 59m"), in the units resetsInWords reads
	tryAgainIn = regexp.MustCompile(`(?i)\btry again in (?:(\d+)\s*d(?:ays?)?\b\s*)?(?:(\d+)\s*h(?:ours?|rs?)?\b\s*)?(?:(\d+)\s*m(?:in(?:utes?|s)?)?\b)?`)
	// quotaCodes: a vendor's own code for a used-up allowance, which alone
	// makes a server error (5xx) one over the quota: Claude Code's resets
	// line, the ChatGPT backend's usage_limit_reached, Google's
	// RESOURCE_EXHAUSTED. A 5xx's words don't: a relay's 500 "当前模型 …
	// 负载已经达到上限，请稍后重试" is its load's limit, not the plan's (#1418).
	quotaCodes = regexp.MustCompile(`(?i)limit reached\|\d{10}\b|usage_limit_reached|RESOURCE_EXHAUSTED`)
	// cutWords: a plugin's own error for the turn its upstream's stream
	// died mid-call: the model wrote its tool call as text, the text
	// stopped before the call was whole, and the plugin ends the turn as a
	// failure "the gateway can retry or fail over" — its words, shared by
	// the plugins that read text-form calls (magpie-community/plugins#38,
	// packages/trae and packages/qoder's CUT_SHORT; #2873). The Qoder
	// plugin's NAMELESS is one too: qfmodel began a native tool call and
	// never named it, twice, before anything of the reply was given — the
	// model's glitch, not the account's, and asked again it mostly answers
	// (magpie-community/plugins#579)
	cutWords = regexp.MustCompile(`(?i)tool call, written as text, was cut short|native tool call has no name; it is not sent on as a tool`)
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
	// failForeign: the conversation's reasoning, or another item's sealed
	// copy, was sealed by another account, and is sent again without it
	failForeign = "foreign"
	// failFloor: the request asked for a shorter reply than the provider
	// gives, and is sent again asking for the least it takes
	failFloor = "floor"
	// failSight: the model turned an image away (imageRefused), and is
	// asked again with the request's images described (refuseSight)
	failSight = "sight"
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
	// *_PROXY variables, the system's) didn't take the connection (#381),
	// or the network closed each new connection before the request went
	// out (errClosedUnsent) —
	// nothing reached the vendor, so the next one is asked, and nobody rests
	failProxy = "proxy"
	// failOffline: this computer had no network (offline.go) — nothing
	// reached the vendor, so the next one is asked, and nobody rests
	failOffline = "offline"
	// failOverflow: the conversation is too long for the member's model
	// (#700) — another member, with room for it, is asked, and nobody rests
	failOverflow = "overflow"
	// failRoom: the window held the prompt but not the reply asked for
	// with it — the same one is asked again for the reply the window has
	// left, and nobody rests
	failRoom = "room"
	// failSlow: the member hadn't begun its reply when its group's
	// FirstToken ran out, nothing of it sent — the next is asked. Once,
	// nobody rests, as a long prompt is slow anywhere; again and again on
	// one account's model, that model rests there (restSlow)
	failSlow = "slow"
	// failLoop: the reply was ended stuck in a loop (#1359) — the agent
	// is told, and nobody rests, as the model, not the account, looped
	failLoop = "loop"
	// failRestricted: the vendor restricted the account itself (WorkBuddy's
	// 403 11140, "账号存在异常行为，已被限制使用", #1849) — the next is
	// asked, and it rests restrictedRest, until the vendor lifts it
	failRestricted = "restricted"
	// failCut: a plugin ended the turn on purpose — its upstream's stream
	// died mid-call, and the tool call it was reading, written as text,
	// can't go on as the answer (plugins#38: the turn ends as an error
	// "the gateway can retry or fail over"). The cut isn't the account's
	// doing: the request is asked again, and nobody rests (#2873)
	failCut = "cut"
)

// loopErrType is the usage log's ErrType for a reply ended for looping
// (#1359), told apart from what vendors call their errors.
const loopErrType = usage.LoopErrType

// reloopedError is a try's error once its reasoning was found stuck in a
// loop before it said anything, and it was let go for the request to ask
// again (#2436).
const reloopedError = "the model's reasoning was stuck in a loop before it said anything; asked again"

// proxyDown is the error Go gives when the proxy itself can't be reached,
// over HTTP (proxyconnect) or SOCKS (socks connect).
var proxyDown = regexp.MustCompile(`proxyconnect |socks connect `)

// wordsOf is catalog.Words: an error body as its words are matched, with
// the strings of a JSON body that escapes what isn't ASCII decoded (#1669).
func wordsOf(body []byte) []byte { return catalog.Words(body) }

// carriedStatus is the status an error a reply carried under a success
// status stands for: an `error` event, or a `{"error":{…}}` body, sent
// with HTTP 200, is read as streamStatus reads an error in a stream, so
// it fails as the same error would with its real status. Read as the
// 200 it came with, OpenAI's "Rate limit reached for gpt-6.1-sol … on
// tokens per min (TPM) … Please try again in 6ms." held usedUpWords'
// "limit reached" and rested the account as out of quota (monater on
// Discord), where its 429 is a minute's rate limit. The status the error
// object names for itself comes first (#4855: a plugin's in-stream frame
// is the only place its runtime can say one); words streamStatus doesn't
// know (its 502) keep the 200, and read as before; any other status is
// the reply's own.
func carriedStatus(status int, body []byte) int {
	if status < 200 || status >= 400 {
		return status
	}
	if said := namedStatus(body); said != 0 {
		return said
	}
	if said := streamStatus(string(wordsOf(body))); said != http.StatusBadGateway {
		return said
	}
	return status
}

// failure says why a reply failed.
func failure(status int, body []byte) string {
	status = carriedStatus(status, body)
	if status == http.StatusBadGateway && (proxyDown.Match(body) || bytes.Contains(body, []byte(closedUnsentWords))) {
		// the proxy didn't take the connection, or the network closed
		// each new one before the request was sent (errClosedUnsent):
		// nothing reached the vendor, and the account is as good as it
		// was. Read as the account's failure, it rested ChatGPT accounts
		// whose window was full till the window renewed, days off,
		// shown as "502 · 失败" (dealyang_ on X)
		return failProxy
	}
	if _, ok := provider.Verification(body); ok && (status == 401 || status == 403) {
		return failVerify
	}
	// the words are matched decoded, too
	body = wordsOf(body)
	switch {
	case cutWords.Match(body):
		// a plugin ended the turn on purpose: its upstream's stream died
		// mid-call, and the call it was reading isn't the answer
		// (plugins#38). The cut is transient and nobody's fault — the next
		// answer mostly isn't one — so the request is asked again, and
		// nobody rests (#2873)
		return failCut
	case restrictedSaid(status, body):
		return failRestricted
	case overflowed(status, body):
		// Kimi's "exceeded model token limit" is the conversation's
		// length, not the plan's
		return failOther
	case status == 402 && usedUpWords.Match(body) && !creditWords.Match(body) && !bytes.Contains(body, []byte("insufficient_quota")):
		// a 402 that says a usage limit and nothing of money is out of
		// quota, not of credit: Factory's "You've reached your weekly
		// standard usage limit (resets in …). Switch to Droid Core …" is
		// Standard's pool, which counts some models only, and read as the
		// account out of credit it rested Droid Core with it (#1979)
		return failQuota
	case status == 402, creditWords.Match(body) && (status != 429 || brokeWords.Match(body)),
		bytes.Contains(body, []byte("insufficient_quota")) && !minuteRate(body):
		return failCredit
	case status == 429 && resetsWhen.Match(body):
		// out until the time it names, in whichever language it said so:
		// WorkBuddy's Chinese refusal read as out of quota (用量), its
		// English one as a rate limit, and each rested its own fallback
		// (#1669)
		return failQuota
	case status == 429 && minuteRate(body):
		// a limit per minute, whatever it calls itself: back in a
		// minute, not a plan used up (minuteWords)
		return failRate
	case status == 429 && !creditWords.Match(body) && backSoon(body):
		// back within a wait a provider's patience can cover, as it says
		// itself, whatever else it says: a LAN aggregator's "Usage has
		// exceeded the frequency limit. … Your usage limit for the
		// original model will reset in 1 minutes." read as a plan used up
		// ("usage limit") and went to the agent at once, never waited
		// for (zoecats on Discord). A limit that says it is back in
		// hours stays out of quota below
		return failRate
	case status == 429 && rateWords.Match(body) && !plannedWords.Match(urlWords.ReplaceAll(body, nil)):
		return failRate
	case status >= 500 && quotaCodes.Match(body), status < 500 && usedUpWords.Match(body), status == 429 && plannedWords.Match(body):
		return failQuota
	case status == 429:
		return failRate
	}
	return failOther
}

// minuteRate says a 429's words name a limit per minute or less
// (minuteWords), and nothing longer or spent beside it: no longer window
// (longWords), no balance spent (brokeWords). Words in a link don't
// count. A body carrying the quota code insufficient_quota reads here
// too when its words say a rate: SenseNova's token-plan gateway answers
// its throttle with the code beside the type rate_limit_error and the
// message "inference exceeds tpm/rpm limit" over a pool half up — read
// from its body it was out of credit, from the words magpie gave the
// agent a rate limit, and the same text was both the same day (#5006).
// The code is the gateway's throttle code, not its balance: OpenAI's own
// out-of-credit (its message names no rate) reads credit as before.
func minuteRate(body []byte) bool {
	body = urlWords.ReplaceAll(body, nil)
	return minuteWords.Match(body) && !longWords.Match(body) && !brokeWords.Match(body)
}

// dashScopeTokenRate: DashScope's (Model Studio's) 429 for the tokens
// per minute or second its account is allowed, which it words as a
// quota: Throttling.AllocationQuota "Allocated quota exceeded, please
// increase your quota limit.", or in OpenAI's shape insufficient_quota
// "You exceeded your current quota, please check your plan and billing
// details." — both "超出TPM/TPS限制" in its error-code list
// (help.aliyun.com/zh/model-studio/error-code). Its free allowance used
// up ("Free allocated quota exceeded."), its Coding Plan's hour, week or
// month ("hour allocated quota exceeded") and arrears (400 Arrearage) are
// worded otherwise. OpenAI's own insufficient_quota is out of credit, so
// this is DashScope's alone.
var dashScopeTokenRate = regexp.MustCompile(`Allocated quota exceeded, please increase your quota limit|"(?:code|type)"\s*:\s*"insufficient_quota"`)

// dashScopeRate says p is DashScope and its 429 is a rate per minute or
// second (dashScopeTokenRate), not its free allowance or a plan's window.
func dashScopeRate(p provider.Provider, status int, body []byte) bool {
	if status != http.StatusTooManyRequests || !dashScope(p) {
		return false
	}
	w := wordsOf(body)
	if bytes.Contains(bytes.ToLower(w), []byte("free allocated quota")) || longWords.Match(urlWords.ReplaceAll(w, nil)) {
		return false
	}
	return dashScopeTokenRate.Match(w)
}

// failureOf says why c's reply failed: a Claude account's sign-in that
// Anthropic refused is told as one (provider/claude_auth.go), passed over
// till it is signed in again; another vendor saying the same words fails
// as failure says, and rests as before.
func failureOf(c candidate, status int, body []byte) string {
	status = carriedStatus(status, body)
	if status >= 400 && c.p.Account != nil && c.p.Account.Agent == "claude" && provider.ClaudeSignInRequired(string(body)) {
		return failAuth
	}
	// a subscription's sign-in its vendor revoked (#1793): no wait mends it
	if provider.RevokedAccount(c.p.Account) && provider.SignInRevoked(status, body) {
		return failAuth
	}
	if restrictedRefused(c.p, status, body) {
		return failRestricted
	}
	if dashScopeRate(c.p, status, body) {
		return failRate
	}
	return failure(status, body)
}

// restrictedSaid says an error is one magpie already put in words as an
// account its vendor restricted: WorkBuddy's 11140 as the built-in
// translates it, or as the plugin does, both ending in
// provider.WBRestrictedHint, and ZCode's Start Plan 3012 as the ZCode
// plugin puts it (provider.ZCodeRestrictedHint, plugins#348). magpie's own
// words, which no vendor says, so they are read whoever answered.
func restrictedSaid(status int, body []byte) bool {
	return status >= 400 && (bytes.Contains(body, []byte(provider.WBRestrictedHint)) || bytes.Contains(body, []byte(provider.ZCodeRestrictedHint)))
}

// restrictedRefused says p's vendor restricted the account: magpie's words
// for it (restrictedSaid), or WorkBuddy's own answer as it came — its code
// 11140 or its displayMsg's words — from a WorkBuddy account, the
// built-in's or the plugin's. Another vendor's code 11140 is its own, and
// isn't read so.
func restrictedRefused(p provider.Provider, status int, body []byte) bool {
	if restrictedSaid(status, body) {
		return true
	}
	return isWorkBuddy(p) && provider.WBRestricted(status, wordsOf(body)) || googleSuspended(p, status, body)
}

// googleToS is Google's 403 PERMISSION_DENIED for a Code Assist account it
// suspended: "This service has been disabled in this account for violation
// of Terms of Service. Please submit an appeal to continue using this
// product." (bestony on Discord, an Antigravity account, 11-06).
var googleToS = regexp.MustCompile(`(?i)disabled in this account for violation of Terms of Service`)

// googleSuspended says Google suspended p, an account signed in to
// Antigravity or Gemini CLI, for its terms (googleToS): the account, not
// the model or the request, until an appeal goes through. Read as any
// failure, it rested a minute, doubling to ten, and was asked again, and
// refused, every ten minutes for good. Another vendor's same words are its
// own, and aren't read so.
func googleSuspended(p provider.Provider, status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	if a := accountAgent(p); a != "antigravity" && a != "gemini" {
		return false
	}
	return googleToS.Match(wordsOf(body))
}

// isWorkBuddy is whether p is a WorkBuddy account, either build's: the
// built-in's, or the WorkBuddy plugin's.
func isWorkBuddy(p provider.Provider) bool {
	if p.Account == nil {
		return false
	}
	id := p.PluginProvider()
	if id == "" {
		id = p.Account.Agent
	}
	return id == "workbuddy" || id == provider.WorkBuddyAIID
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
	// By is what set how long: "credit" (half an hour, a key longer each
	// time again: creditLadder), "retry-after" (the
	// vendor's own headers), "resets" (the time Claude Code gave), "window"
	// (the allowance window it filled renews), "quota" (no word of when),
	// "cooldown" (a minute), "backoff" (longer each time it fails again,
	// or is rate limited again as soon as it is back), "restricted" (the
	// vendor restricted the account: two hours, or until lifted by hand),
	// "off" (no rest: its provider is set not to rest after an unknown
	// failure, provider.RestCapOff).
	By       string `json:"by"`
	Failures int    `json:"failures,omitempty"` // in a row, for a backoff
	// Key is what it rests by, for the app to lift the rest (Unrest)
	Key string `json:"key,omitempty"`
	// Model: the one model resting, when the account or key still
	// answers its others (a pooled allowance, a model it doesn't serve);
	// "" for the account or key as a whole. The app shows the rest on
	// that model's rows only (#1960: Antigravity's Claude & GPT pool used
	// up showed gemini-3.8-flash, in a pool of its own, as out too).
	Model string `json:"model,omitempty"`
	// Link: where the vendor said to verify the account, for failVerify
	Link string `json:"link,omitempty"`
	// Probed: a key resting after an outage that names no time (a 5xx, its
	// vendor unreachable) is asked again in the background, and its rest
	// lifted as soon as it answers (rest_probe.go)
	Probed bool `json:"probed,omitempty"`
	// Error: what the vendor said, for the app to show beside "failed"
	// (GanHei37443 on X: three WorkBuddy accounts each "failed · back in
	// 16s", and nothing on the row said why)
	Error string `json:"error,omitempty"`
	// Rate: the vendor called it a rate (frequency) limit and named when
	// it lifts (rateUntilSaid). It rests as out of quota until then, and
	// the app says rate limited, not quota used up: WorkBuddy's "您的使用
	// 量已超出频率限制，将在 … 重置，您也可以切换其他模型继续使用" rests
	// one model of an account with credits left (taoxiaoxin on Discord)
	Rate bool `json:"rate,omitempty"`

	// the subscription account resting, when it is one; agent "" and its
	// provider.KeyAllowanceID for a key out of its own windows
	agent, user string
	said        string    // the error it gave, for a failVerify held
	hold        time.Time // until when a failVerify is answered without asking
	// a key out of its allowance or credit for as long as magpie guessed,
	// the vendor and the user naming no time, is held (quotaHeld) once it
	// was refused so twice in a row, not on the first refusal (first),
	// and asked again from askAt, a minute on: zoecats on Discord, a local
	// aggregator's "quota exhausted" for the moment it switched upstreams
	// held every request for 15 minutes or more
	first bool
	askAt time.Time
	// there: another computer's magpie refused one of its models
	// (remoteModelRest). It routes that model there and holds its own
	// rests, and what it serves under a name can change at any moment (a
	// group moved to another vendor), so its refusal out of quota or
	// credit is never given again here without asking it (quotaHeld):
	// asked, it answers from its own hold at once, or from what it serves
	// now
	there bool
}

// asksAt is when r, a key's rest out of its allowance or credit, has it
// asked again: at once after its first refusal, a minute on when magpie
// guessed how long it is out, else when it is back. Any other rest asks
// when it ends.
func (r Rest) asksAt(now time.Time) time.Time {
	switch {
	case r.first:
		return now
	case !r.askAt.IsZero() && r.askAt.Before(r.Until):
		return r.askAt
	}
	return r.Until
}

// renewed lifts the rests of a subscription account whose windows were
// just started again, as a reading of them (Allowances) finds one it was
// full in full no more: out of quota no longer. Only its rests out of
// quota, or failed with a window full ("window"), are lifted, and a
// ChatGPT account's out of its credits, which it needs no more with its
// window back; and of them only what no vendor gave the time of: a
// refusal's own word ("resets", "retry-after") stands till a window truly
// starts again (restarted), or a reset is spent — not till a reading
// merely finds the window less than full, which the vendor's usage can
// say while its own refusal stands, and lifted on that the account was
// tried and refused again every few minutes till its reset (#2718). One
// for a rate limit, another's credit or a verification stays. Told agent
// "", it is a key (provider.KeyAllowanceID) whose windows a reading found
// full no more — its limit raised, or its usage reset — and its rest out
// of them is lifted; a key's other rests aren't noted by it.
func renewed(agent, user string) {
	liftRests(agent, user, false)
}

// restarted lifts what a reading's word alone may not: the rests the
// vendor itself gave the time of. Told as renewed is, when a window
// stands started again in fact, or a reset was spent.
func restarted(agent, user string) {
	liftRests(agent, user, true)
}

// liftRests lifts an account's rests out of quota, or of a window read
// full: those no vendor timed by renewed's word alone, and those it did
// only by restarted's.
func liftRests(agent, user string, vendorTill bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	now := time.Now()
	for k, r := range restingUntil.note {
		if r.agent != agent || !strings.EqualFold(r.user, user) || !(r.Why == failQuota || r.By == "window" || r.Why == failCredit && agent == "codex") {
			continue
		}
		if vendorTill != (r.By == "resets" || r.By == "retry-after") {
			continue
		}
		delete(restingUntil.m, k)
		delete(restingUntil.note, k)
		liftedLocked(k, now)
	}
}

func init() {
	provider.OnRenewed(renewed)
	provider.OnStarted(restarted)
}

// staleAllowance is provider.StaleAllowance, swapped in tests.
var staleAllowance = provider.StaleAllowance

// codexMayBeUsedUp is provider.CodexMayBeUsedUp, swapped in tests.
var codexMayBeUsedUp = provider.CodexMayBeUsedUp

// awaitAllowance is provider.AwaitAllowance, swapped in tests.
var awaitAllowance = provider.AwaitAllowance

// allowanceWait is how long a quota rest made before the account's
// windows were read waits for a reading to narrow it (narrowWhenRead).
const allowanceWait = time.Minute

// narrowing counts the narrowWhenRead still waiting, for a test to wait on.
var narrowing sync.WaitGroup

// narrowWhenRead narrows an account's quota rest, made while its windows
// were unknown (a start, or a reading that failed), to the model alone
// once a reading says the model's pool is the one used up (#2036,
// Ziqi-Cui): Factory's Standard pool used up leaves Droid Core's models
// in. A reading that says nothing of pools, or none in time, leaves the
// account's rest as it is, and so does a rest lifted or made again since.
// It waits off the request, which has failed over already.
func narrowWhenRead(c candidate, r Rest) {
	defer narrowing.Done()
	ctx, done := context.WithTimeout(context.Background(), allowanceWait)
	defer done()
	if !awaitAllowance(ctx, r.agent, r.user) || !c.pooled(time.Now()) {
		return
	}
	id, rid := c.restKey(), c.restID()
	restingUntil.Lock()
	defer restingUntil.Unlock()
	if now, ok := restingUntil.note[id]; !ok || now.Why != failQuota || !restingUntil.m[id].Equal(r.Until) {
		return
	}
	delete(restingUntil.m, id)
	delete(restingUntil.note, id)
	r.Key, r.Model = rid, c.model
	if until, ok := restingUntil.m[rid]; !ok || until.Before(r.Until) {
		restingUntil.m[rid] = r.Until
		restingUntil.note[rid] = r
	}
}

// keyAllowance and staleKeyAllowance are provider.KeyAllowance and
// provider.StaleKeyAllowance, swapped in tests.
var (
	keyAllowance      = provider.KeyAllowance
	staleKeyAllowance = provider.StaleKeyAllowance
)

// RestOf is why what rests by key — a provider's id, or its id#key — is
// passed over now, while it is.
func RestOf(key string) (Rest, bool) { return restOf(key) }

// RestsUnder are the rests in force now of what rests by a key starting
// with prefix — a subscription's accounts by "<provider>@", each its own
// (provider@user) and each of its models' (provider@user/model) — with
// Key, and Model for a model's, filled in, soonest back first. The
// Providers page shows them on the account's row (maxleebeta on Discord:
// WorkBuddy's 429 "将在 … 重置" rested a model with credits left, and
// nothing there said so).
func RestsUnder(prefix string) []Rest {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	now := time.Now()
	var out []Rest
	for k, until := range restingUntil.m {
		if !strings.HasPrefix(k, prefix) || !now.Before(until) {
			continue
		}
		r := restingUntil.note[k]
		r.Key, r.Until = k, until
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Rest) int {
		return cmp.Or(a.Until.Compare(b.Until), strings.Compare(a.Key, b.Key))
	})
	return out
}

// Unrest lifts the rest of what rests by key — an account just verified
// with its vendor, say — so the next request asks it again: rate limited
// with Sink on, it is no longer sent to the back of the order either.
// The lift is told to the Routing page (TraceState.Lifted), wherever it
// was asked for. False when it wasn't resting.
func (s *Server) Unrest(key string) bool {
	restingUntil.Lock()
	_, ok := restingUntil.m[key]
	kept := ladderRest(restingUntil.note[key])
	delete(restingUntil.m, key)
	delete(restingUntil.note, key)
	liftedLocked(key, time.Now())
	restingUntil.Unlock()
	if kept {
		saveRests()
	}
	unsink(key)
	s.trace.lifted()
	s.lanes.signal() // a request waiting in a full lane may go to it now (waitMate)
	return ok
}

// UnrestAll lifts every rest of what rests by key: its own and each of its
// models' (restID, key/model). A key whose value was replaced (#1869) is
// asked again for all of them. It returns how many it lifted.
func (s *Server) UnrestAll(key string) int {
	restingUntil.Lock()
	n, now, kept := 0, time.Now(), false
	for k := range restingUntil.m {
		if k == key || strings.HasPrefix(k, key+"/") {
			kept = kept || ladderRest(restingUntil.note[k])
			delete(restingUntil.m, k)
			delete(restingUntil.note, k)
			liftedLocked(k, now)
			n++
		}
	}
	liftedLocked(key, now)
	restingUntil.Unlock()
	if kept {
		saveRests()
	}
	unsinkAll(key)
	s.trace.lifted()
	s.lanes.signal() // a request waiting in a full lane may go to it now (waitMate)
	return n
}

// verifyHeld is the error to give again, without asking, for a candidate
// its vendor refused a moment ago until the account is verified: rested
// whole, or only its model where that is how it rests (a Remote magpie's
// model, remoteModelRest).
func verifyHeld(c candidate) (string, bool) {
	r, ok := restOf(c.restKey())
	if !ok && c.restID() != c.restKey() {
		r, ok = restOf(c.restID())
	}
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
// refusal says: Claude Code's "usage limit reached|<unix>", ChatGPT's
// resets_at / resets_in_seconds, or a time written beside the word for
// coming back (resetsWritten). Zero when it doesn't say.
func resetsAt(body []byte, now time.Time) time.Duration {
	if m := resetsWords.FindSubmatch(body); m != nil {
		if n, _ := strconv.ParseInt(string(m[1]), 10, 64); time.Unix(n, 0).After(now) {
			return time.Unix(n, 0).Sub(now)
		}
	}
	if d := resetsIn(body, now); d > 0 {
		return d
	}
	if d := resetsWritten(body, now); d > 0 {
		return d
	}
	if d := resetsZhipu(body, now); d > 0 {
		return d
	}
	return resetsAfter(body)
}

// zhipuResets: Zhipu's 1308 and 1310 (Z.ai and bigmodel.cn, GLM Coding
// Plan), as its error code pages give them: "Usage limit reached for 5
// hour. Your limit will reset at 2026-09-22 01:18:11", "Weekly/Monthly
// Limit Exhausted. Your limit will reset at …", "已达到 5 小时的使用上限。
// 您的限额将在 … 重置". The time has no zone; it is Beijing's (a 1308
// received at 2026-09-21T17:17:32Z said 01:18:11, 39 seconds later read as
// UTC+8, 8 hours later as UTC). Read with resetsWhen's offset alone, it was
// not read at all, and the key rested a quarter of an hour, not until the
// time Z.ai named (Player on Discord: "reset at 2026-10-15 20:12", "not
// asked again before 17:12"). Only these words, then a whole date and a
// time to the minute at least, with no digit, colon or zone after it.
var zhipuResets = regexp.MustCompile(`(?:\bYour limit will reset at|您的限额将在)\s*(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2}))?(\s*\S{0,3})`)

// heldClock is when a key or model held out of its allowance is asked
// again, as an error put in words says it. The agent reading it, or a
// Remote magpie's user, may be on another clock than this magpie's, so the
// offset goes with it: "20:12 UTC+8" today, else "Oct 16 09:00 UTC+8".
// Without it a magpie on UTC+8 said "not asked again before 17:12" to a
// user who couldn't tell whose 17:12 it was (Player on Discord).
func heldClock(t, now time.Time) string { return heldClockIn(t, now, time.Local) }

// heldClockIn is heldClock on the clock of loc.
func heldClockIn(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	s := t.Format("Jan 2 15:04")
	if y, m, d := t.Date(); now.Year() == y && now.Month() == m && now.Day() == d {
		s = t.Format("15:04")
	}
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	z := fmt.Sprintf("UTC%s%d", sign, off/3600)
	if m := off % 3600 / 60; m > 0 {
		z += fmt.Sprintf(":%02d", m)
	}
	return s + " " + z
}

// zhipuZone is the zone Zhipu writes its reset times in.
var zhipuZone = time.FixedZone("UTC+8", 8*3600)

// resetsZhipu is how long until the time a Zhipu 1308 or 1310 names
// (zhipuResets), read in Beijing's time. Zero when there is none, it isn't
// a real date, it is written with a zone of its own (resetsWritten reads
// those), it has passed, or it is further off than longestSaid.
func resetsZhipu(body []byte, now time.Time) time.Duration {
	m := zhipuResets.FindSubmatch(wordsOf(body))
	if m == nil {
		return 0
	}
	// cut in its seconds ("20:12:1"), or a zone after it: not this form
	if after := bytes.TrimLeft(m[7], " "); len(after) > 0 && (after[0] >= '0' && after[0] <= '9' || after[0] == ':' || after[0] == '+' || after[0] == '-' || bytes.HasPrefix(bytes.ToUpper(after), []byte("UTC")) || bytes.HasPrefix(bytes.ToUpper(after), []byte("GMT")) || after[0] == 'Z') {
		return 0
	}
	n := make([]int, 6)
	for i, g := range m[1:7] {
		if len(g) > 0 {
			n[i], _ = strconv.Atoi(string(g))
		}
	}
	y, mo, d, h, mi, sec := n[0], n[1], n[2], n[3], n[4], n[5]
	t := time.Date(y, time.Month(mo), d, h, mi, sec, 0, zhipuZone)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d || t.Hour() != h || t.Minute() != mi || t.Second() != sec {
		return 0
	}
	if w := t.Sub(now); w > 0 && w <= longestSaid(body) {
		return w
	}
	return 0
}

// resetsInWords: a refusal that says how long until it is back in days,
// hours and minutes, as Factory's 402 does: "You've reached your weekly
// standard usage limit (resets in 0h 19min)" (#1979). At least one of
// them, each a number and its unit, and nothing else between.
var resetsInWords = regexp.MustCompile(`(?i)\bresets? in (?:(\d+)\s*d(?:ays?)?\b\s*)?(?:(\d+)\s*h(?:ours?|rs?)?\b\s*)?(?:(\d+)\s*m(?:in(?:utes?|s)?)?\b)?`)

// quotaResetAfter: Antigravity's (Google Code Assist's) 429 for a model's
// allowance used up, which writes how long until it is back as Go prints
// a duration: "You have exhausted your capacity on this model. Your quota
// will reset after 3h19m30s." Unread, the account rested the quarter of
// an hour of a quota that says nothing, and the account first in order
// was asked again, and refused, every 15 minutes for the hours or days
// its quota was out (Dani Ariyanto on Discord, 10-24).
var quotaResetAfter = regexp.MustCompile(`(?i)\bquota will reset after ((?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s)?)`)

// resetsAfter is how long the refusal says until it is back
// (resetsInWords, quotaResetAfter, soonWords), held to longestWritten. Zero when it
// doesn't say.
func resetsAfter(body []byte) time.Duration {
	body = wordsOf(body)
	if d := spanWritten(resetsInWords.FindSubmatch(body)); d > 0 {
		return d
	}
	if m := quotaResetAfter.FindSubmatch(body); m != nil && len(m[1]) > 0 {
		if d, err := time.ParseDuration(string(m[1])); err == nil && d > 0 && d <= longestWritten {
			return d
		}
	}
	return soonWritten(body)
}

// soonWords: a refusal that says in seconds, or in Chinese minutes, how
// long until it is back: "try again in 30 seconds", "retry after 20s",
// "resets in 45 secs", "请 1 分钟后重试", "30秒后再试". The English minutes,
// hours and days are resetsInWords' and tryAgainIn's.
var soonWords = regexp.MustCompile(`(?i)\b(?:resets? (?:in|after)|try again in|retry (?:in|after))\s*(\d+(?:\.\d+)?)\s*(?:s|secs?|seconds?)\b|(\d+)\s*(秒|分钟)钟?\s*(?:之?后)`)

// soonest is the longest a refusal may say it is back in for a 429 to be
// a rate limit waited out, not a plan used up: as long as a provider's
// patience can be set to wait (provider.PatienceLongest).
const soonest = time.Duration(provider.PatienceLongest) * time.Second

// soonWritten is the seconds, or Chinese minutes, a refusal says until it
// is back (soonWords). Zero when it doesn't say.
func soonWritten(body []byte) time.Duration {
	m := soonWords.FindSubmatch(body)
	switch {
	case m == nil:
		return 0
	case len(m[1]) > 0:
		n, _ := strconv.ParseFloat(string(m[1]), 64)
		return time.Duration(n * float64(time.Second))
	}
	n, _ := strconv.Atoi(string(m[2]))
	if string(m[3]) == "分钟" {
		return time.Duration(n) * time.Minute
	}
	return time.Duration(n) * time.Second
}

// backIn is how long a refusal says in words until it is back: resetsAfter,
// or a "try again in" (tryAgainIn). Zero when it doesn't say.
func backIn(body []byte) time.Duration {
	body = wordsOf(body)
	if d := resetsAfter(body); d > 0 {
		return d
	}
	return spanWritten(tryAgainIn.FindSubmatch(body))
}

// backSoon says a refusal says it is back within soonest.
func backSoon(body []byte) bool {
	d := backIn(body)
	return d > 0 && d <= soonest
}

// clineModelLimited says c, a provider whose requests reach Cline's API,
// was refused for one free model's own daily limit (clineFreeLimit).
func clineModelLimited(c candidate, body []byte) bool {
	return c.p.ClinePinnable() && clineFreeLimit.Match(wordsOf(body))
}

// clineBackIn is how long Cline's free-model refusal says until the model
// is back (tryAgainIn), held to longestWritten. Zero for another refusal,
// or one that doesn't say.
func clineBackIn(c candidate, body []byte) time.Duration {
	if !clineModelLimited(c, body) {
		return 0
	}
	return spanWritten(tryAgainIn.FindSubmatch(wordsOf(body)))
}

// spanWritten is the days, hours and minutes a match of resetsInWords or
// tryAgainIn names, held to longestWritten. Zero when it names none.
func spanWritten(m [][]byte) time.Duration {
	if m == nil || len(m[1])+len(m[2])+len(m[3]) == 0 {
		return 0
	}
	var d time.Duration
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute} {
		if n, err := strconv.Atoi(string(m[i+1])); err == nil {
			d += time.Duration(n) * unit
		}
	}
	if d > longestWritten {
		return 0
	}
	return d
}

// longestWritten is the furthest off a reset time written in words is
// believed: WorkBuddy's frequency limits come back within the day, and a
// time further off than a week is more likely a misreading than a reset.
const longestWritten = 7 * 24 * time.Hour

// longestMonthly is the furthest off a written reset of a month's
// allowance is believed, and the longest an account or key out of one
// sits out: a month, and days over. Ark's monthly quota used up on the
// 22nd resets on the 31st, 9 days off; held to the week, it was read as
// no reset and asked again every quotaRest till the month turned (#3276).
const longestMonthly = 35 * 24 * time.Hour

// longestSaid is the furthest off a reset time a refusal writes is
// believed: longestMonthly when it says its allowance is a month's
// (monthlyWords), else longestWritten.
func longestSaid(body []byte) time.Duration {
	if monthlyWords.Match(wordsOf(body)) {
		return longestMonthly
	}
	return longestWritten
}

// resetsWritten is how long until the time a refusal names in words
// (resetsWhen), read in the offset it is written with. Zero when there is
// none, it isn't a real date, it has passed, or it is further off than
// longestSaid.
func resetsWritten(body []byte, now time.Time) time.Duration {
	m := resetsWhen.FindSubmatch(wordsOf(body))
	if m == nil {
		return 0
	}
	// the offset after UTC or GMT, else as Go prints it (Ark's +0800 CST)
	sign, oh, om := m[7], m[8], m[9]
	if len(sign) == 0 {
		sign, oh, om = m[10], m[11], m[12]
	}
	n := make([]int, 8)
	for i, g := range [][]byte{m[1], m[2], m[3], m[4], m[5], m[6], oh, om} {
		if len(g) > 0 {
			n[i], _ = strconv.Atoi(string(g))
		}
	}
	y, mo, d, h, mi, sec, offH, offM := n[0], n[1], n[2], n[3], n[4], n[5], n[6], n[7]
	if offH > 14 || offM > 59 {
		return 0
	}
	off := offH*3600 + offM*60
	if string(sign) == "-" {
		off = -off
	}
	t := time.Date(y, time.Month(mo), d, h, mi, sec, 0, time.FixedZone("", off))
	// a date time.Date had to carry over (2026-13-45, 24:61) isn't one
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d || t.Hour() != h || t.Minute() != mi || t.Second() != sec {
		return 0
	}
	if w := t.Sub(now); w > 0 && w <= longestSaid(body) {
		return w
	}
	return 0
}

// restError is the vendor's words a rest is shown with, cut short: the
// request's details keep them whole
func restError(status int, body []byte) string {
	return clipRunes(strings.TrimSpace(provider.APIError(body, http.StatusText(status))), restErrorRunes)
}

const restErrorRunes = 300

// restAfter sets a failed candidate aside for as long as its failure says.
func (s *Server) restAfter(c candidate, status int, header http.Header, body []byte) Rest {
	return s.restAfterMarked(c, status, header, body, openRouterSharedPool(body))
}

// restAfterMarked keeps an upstream routing fact through gateway error
// translation without putting it in the response sent to the client.
func (s *Server) restAfterMarked(c candidate, status int, header http.Header, body []byte, sharedPool bool) Rest {
	// an error carried under a 200 rests as its own status would, and
	// says that status where the rest is shown — first among equals the
	// one its stream's error frame named outright (#4855)
	status = failedNamed(header, carriedStatus(status, body))
	now := time.Now()
	d := fallbackCooldown
	why := failureOf(c, status, body)
	if why == failCut {
		// a plugin ended the turn on purpose, its upstream's stream having
		// died mid-call (plugins#38): the account is as good as it was, the
		// request is asked again, and no rest and no failure counted (#2873)
		return Rest{Why: why, Status: status, By: "off", Until: now}
	}
	if header.Get(zdrHeader) != "" || zdrRefused(status, body) {
		// Command Code's Zero Data Retention setting refused the request:
		// the model and the account are as good as they were, and answer
		// as soon as the switch is off
		return Rest{Why: why, Status: status, By: "off", Until: now}
	}
	if offlineFailure(c, status, body) {
		// this computer had no network: the provider is as good as it
		// was, and is asked again once the network is back (offline.go)
		return Rest{Why: failOffline, Status: status, By: "off", Until: now}
	}
	if regionRefused(status, wordsOf(body)) {
		// the vendor refused where the request came from (regionWords):
		// the network's doing, and the edge's the connection reached, not
		// the account's. Every account asked on that connection was
		// refused the same, and all of them rested, while a new
		// connection answered seconds later (#5182). None is set aside
		// and the failure counts nowhere; the next try goes out on a new
		// connection (regionFresh)
		return Rest{Why: why, Status: status, By: "off", Until: now}
	}
	if status == http.StatusBadRequest && signatureRefused.Match(wordsOf(body)) {
		// Google turned a thought signature away as one it didn't give
		// (signatureRefused): the history's doing, not the account's. A
		// group that moved from Vertex AI to AI Studio was refused on every
		// account alike (mimi001234); none is set aside
		return Rest{Why: why, Status: status, By: "off", Until: now}
	}
	if status == http.StatusBadGateway && bytes.Contains(body, []byte(openCodeBare)) {
		// OpenCode's bare 400 (openCodeBareRefusal, #3740) is about the
		// request, not the key: the key's other requests go through, so
		// none is set aside and the failure counts nowhere
		return Rest{Why: why, Status: status, By: "off", Until: now}
	}
	if status == http.StatusBadRequest || status == http.StatusForbidden {
		// a 403 too: Factory's usage policy check refuses at 403
		// (content_policy_rejected), which failureOf alone reads as the
		// account's
		if _, ok := policyRefusal(body); ok {
			// the safety filter refused the request's content (#3274:
			// Codex's 400 cyber_policy), read from the vendor's error
			// object, never from words a body may echo of the request:
			// any account is refused it alike, so none is set aside and
			// the failure counts nowhere. The serve loop doesn't rest a
			// refusal it reads (failRefused); this holds for every other
			// route that rests one
			return Rest{Why: failRefused, Status: status, By: "off", Until: now}
		}
	}
	// read before the allowance is asked again below: out of a pool that
	// counts some models only, the account's others are still in theirs
	// (Cursor's Auto with Other Models used up)
	pooled := why == failQuota && c.pooled(now)
	// and none to tell from: the account rests, narrowed once read (#2036)
	unread := why == failQuota && c.p.Account != nil && len(allowances(c.p.Account.UsageAgent())[c.p.Account.User]) == 0
	// a model the key's plan doesn't serve rests alone, counted by itself
	// (#1235): the key's other models are still served. So does a ZCode
	// trial entry zcode.z.ai's firewall blocked (zcodeTrialBlocked): the
	// account's GLM Coding Plan goes to another host
	unserved := why == failOther && (modelRefused(status, body) || codeAssistModelMissing(c.p, status, body) || zcodeTrialBlocked(c, status, body))
	// the vendor too busy for the request: the model's moment, not the
	// account's fault (vendorOverloaded)
	overloaded := why == failOther && !unserved && c.model != "" && vendorOverloaded(status, body)
	r := Rest{Why: why, Status: status, By: "cooldown", Error: restError(status, body)}
	// out of allowance or credit for as long as magpie guessed: the vendor
	// named no time, nor did the user (asksAt)
	guessed := false
	// a failure counted past the account's, for its model alone (below)
	modelBackoff := false
	if why == failProxy {
		// the account is as good as it was; the proxy is the user's to start
		return r
	}
	if c.p.LimitRestOff && (why == failQuota || why == failCredit || why == failRate || status == http.StatusTooManyRequests) {
		// the user set it never to rest for a limit (provider.LimitRestOff;
		// zoecats on Discord: a local aggregator says "quota exhausted"
		// for the moment it switches upstreams, and the next request goes
		// through): not set aside, sunk or counted, and asked as ever. A
		// key refused, a model not served or a plain failure rests it as
		// before
		r.By, r.Until = "off", now
		return r
	}
	if why == failCredit || why == failQuota {
		// the vendor's words, for quotaHeld to give again
		r.said = provider.APIError(body, http.StatusText(status))
	}
	switch why {
	case failCredit:
		d, r.By, guessed = creditRest, "credit", true
		if a := c.p.Account; a != nil && a.UsageAgent() == "codex" {
			// a ChatGPT account out of the credits its plan gives: they
			// come again with the plan, and the account answers again
			// once its own window renews, whichever comes first. Until the
			// time the refusal names, else the window its reading has
			// full (read whatever the credits it said it held: they are
			// what ran out), else a while; a reading that finds the window
			// renewed lifts it (renewed)
			al, _ := c.allowance()
			if w := resetsAt(body, now); w > 0 {
				d, r.By = w, "resets"
			} else if w := resetsNoted(header, now); w > 0 {
				d, r.By = w, "resets"
			} else if t := al.Full(c.model, provider.SpentShareOf(c.p.Routing), now); !t.IsZero() {
				d, r.By = t.Sub(now), "window"
			} else if w := retryAfter(header, now); w > chatgptCreditRest {
				d, r.By = w, "retry-after"
			} else {
				d = chatgptCreditRest
			}
			d, guessed = min(d, longestMonthly), false
		} else if c.p.Account == nil && !remoteModelRest(c, why, header) {
			// a key out of money: longer each time it is refused so
			// again (creditLadder). Rested by the id the rest is kept
			// under below; another computer's magpie rests its model
			// for it, as before
			id := c.restKey()
			if c.p.PaidBesideFree(c.model) || modelCooledDown(why, body) {
				id = c.restID()
			}
			d, r.By, r.Failures, guessed = creditBackoff(id, header, body, now)
		}
	case failQuota:
		if c.onCredits() {
			// a Codex account that holds credits refused as out of its
			// allowance: at the moment a window is used up ChatGPT may
			// turn a turn away before it spends the credits, and the next
			// goes through on them (Zhenzhen on Discord: a new session
			// answered at once). Benched until the window renews, days
			// off, it would leave its credits unspent; it is asked again
			// after a moment instead, and the reading asked for again
			// below tells whether it still holds them
			d, r.By = fallbackCooldown, "credits"
			break
		}
		// out of quota is out until the quota comes back: when the refusal
		// says (Claude Code's time, ChatGPT's resets_at — noted by
		// keepRetry once the error is put in words), else when the window
		// it filled renews, else as long as the vendor's headers ask — and
		// only a header is held to the longest wait, as a rate limit's
		// would be (#147: a ChatGPT account out until 21:34 read as back
		// "in 59 minutes", the resets_at turned Retry-After and cut to an
		// hour, then was tried, refused and benched again)
		d, r.By = quotaRest, "quota"
		// as long as the user set for this provider (#1772: Ollama
		// Cloud's weekly limit says nothing of when it renews), or until
		// the time of day they said its allowance comes back (#2008: a
		// free tier's daily quota)
		own := c.p.OwnQuotaRest(now)
		if own > 0 {
			d = own
		}
		if w := resetsAt(body, now); w > 0 {
			d, r.By = w, "resets"
		} else if w := clineBackIn(c, body); w > 0 {
			d, r.By = w, "resets"
		} else if w := resetsNoted(header, now); w > 0 {
			d, r.By = w, "resets"
		} else if t := c.full(now); !t.IsZero() {
			d, r.By = t.Sub(now), "window"
		} else if w := retryAfter(header, now); w > 0 && (w < longestWait || own <= w) {
			// a "try again in" cut to the longest wait asked for longer
			// still: the user's own longer rest stands then (#2008: a
			// daily quota's Retry-After of an hour or more had the seat
			// asked again every hour, whatever rest the user set)
			d, r.By = w, "retry-after"
		}
		// a month's allowance whose refusal named its reset is out till
		// then, past the week's cap (#3276); nothing else is held longer
		guessed = r.By == "quota" && own <= 0
		longest := longestQuota
		if r.By == "resets" && monthlyWords.Match(wordsOf(body)) {
			longest = longestMonthly
		}
		d = min(d, longest)
	case failRate:
		d, r.By, r.Failures = s.rateRest(c, header, body, sharedPool, now)
		// rate limited with quota left: to the back, where the order sinks
		if c.full(now).IsZero() && !(c.isOpenRouterFree() && sharedPool) {
			// a remote magpie's rest is the model's alone (caa53673, the
			// key's own limit apart): the sink keeps the same scope, or
			// one model's 429 would send the remote's other models, never
			// limited, behind the others too
			if remoteModelRest(c, failRate, header) || modelCooledDown(failRate, body) {
				sink(c.restID(), now)
			} else {
				sink(c.restKey(), now)
			}
		}
	case failRestricted:
		// out until the vendor lifts it: an appeal, not a wait (#1849)
		d, r.By = restrictedRest, "restricted"
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
		// the provider set never to rest after a failure that says nothing
		// of why (provider.RestCapOff; Kismet Alan on Discord: a relay's
		// or an account pool's unknown errors come now and then, and the
		// next request often goes through): it isn't set aside, nor its
		// failure counted. A key refused, a model not served, or a window
		// full still rests it, as they say why
		if c.p.NoFailRest() && why == failOther && !unserved && status != http.StatusUnauthorized && status != http.StatusForbidden && (c.p.Account == nil || c.full(now).IsZero()) {
			r.By, r.Until = "off", now
			return r
		}
		if overloaded {
			// the vendor's servers too busy for anyone (celestial_eagle_99230
			// on Discord: ChatGPT's 529 server_is_overloaded on gpt-6-sol
			// rested eight Plus accounts each up to the 10-minute cap,
			// "failed 5 times in a row", and left nothing to answer). It
			// says nothing of the account: its model sits out a moment on
			// it, as long as the vendor's Retry-After asks up to a minute,
			// and no failure is counted, so the next overload rests it as
			// briefly and its own failures climb as they did
			d, r.By = overloadRest, "cooldown"
			if w := retryAfter(header, now); w > 0 {
				d, r.By = min(w, overloadRestMost), "retry-after"
			}
			break
		}
		counted := c.restKey()
		if unserved {
			counted = c.restID()
		}
		// failing again while it still rests for a failure of this kind —
		// asked all the same, as the last one left, or by the request's
		// own patience going over every account again — keeps the count
		// where it is, as a rate limit does (rateRest): it climbs once a
		// rest has ended and it fails again. Before, one request whose
		// model failed alike on every account (a 5xx, asked a dozen times
		// over by patience) counted each account a dozen times and rested
		// them all the 10-minute cap at once (ly on Discord: seven Codex
		// accounts on gpt-6-astra, all resting minutes)
		again := backingOff(counted, now) || !unserved && c.model != "" && backingOff(c.restID(), now)
		routed.Lock()
		if !again {
			routed.failures[counted]++
		}
		n := max(routed.failures[counted], 1)
		// the model's own count too, which only its own answer clears: on
		// an account serving several models, a sibling's answer clears
		// the account's count, and a model that keeps failing between
		// them rested a minute forever (#2241, weizsw). Counted past the
		// account's, a sibling answered since it last did, so the failure
		// is the model's: it climbs its own ladder and rests alone. A
		// candidate naming no model has no sibling to tell apart
		if !unserved && c.model != "" {
			if !again {
				routed.failures[c.restID()]++
			}
			if own := routed.failures[c.restID()]; own > n {
				n, modelBackoff = own, true
			}
		}
		routed.Unlock()
		d, r.By, r.Failures = min(fallbackCooldown<<min(n-1, 10), c.p.RestCapOr(longestRetry)), "backoff", n
		// a subscription that failed with a window full is out of it,
		// whatever it said; a key isn't: a relay out of accounts for
		// everyone (sub2api's 503) says nothing of the key's own windows.
		// Held to the longest an account out of quota sits out: the window
		// is the vendor's reading, and one read a year off (a /usage date
		// with no year) would bench the account until someone noticed.
		// Not a refusal (401, 403) that names no quota: one that did is
		// failQuota already, and a plain 403 is about the key or the
		// request, so a cached reading of 98% benched a working account
		// until its window reset (plugins#243, zygs1083-dotcom)
		if t := c.full(now); !t.IsZero() && c.p.Account != nil && !unserved && status != http.StatusUnauthorized && status != http.StatusForbidden {
			d, r.By, modelBackoff = min(t.Sub(now), longestQuota), "window", false
		}
	}
	// an account is kept by the agent its usage is asked of: a plugin's
	// by the plugin's provider ("plugin:grok"), not by "plugin"
	if a := c.p.Account; a != nil && why != failOther && why != failVerify && why != failRestricted {
		staleAllowance(a.UsageAgent(), a.User) // ask again what it has left
		if why == failQuota && a.UsageAgent() == "codex" {
			// the account Codex is signed in to may be out: the Codex app
			// greys its Send till it knows magpie is its provider
			codexMayBeUsedUp(a.User)
		}
	} else if a == nil && why != failOther && why != failVerify && why != failRestricted {
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
	// a refusal that names its reset and points to the account's other
	// models is the model's limit: the others still answer (WorkBuddy's
	// "您也可以切换其他模型继续使用", #1669)
	if pooled || unserved || modelBackoff || overloaded || why == failQuota && resetsWhen.Match(wordsOf(body)) && otherModels.Match(wordsOf(body)) {
		id = c.restID()
	}
	// Cline's daily limit of one free model is that model's: its others,
	// free or the plan's, still answer (#1398, wuasakura)
	if why == failQuota && clineModelLimited(c, body) {
		id = c.restID()
	}
	// a ZCode Start Plan trial entry ended or spent is that entry's: the
	// account's GLM Coding Plan models still answer (#3661)
	if why == failQuota && zcodeTrialRefused(c, status, body) {
		id = c.restID()
	}
	// a relay's 429 that names the model whose credentials cooled down is
	// that model's: its siblings on the same key still answer upstream,
	// and the model's own sentence rests with the model, not on every
	// sibling's refusal (#3039)
	if modelCooledDown(why, body) {
		id = c.restID()
	}
	// out of credit, or of a quota, on a model that costs credits where
	// the subscription serves other models free is that model's: the free
	// ones draw on no credits, and the account still serves them (Creat.wu
	// on Discord: WorkBuddy AI's 14018 "Credits exhausted" on glm-5.3-flash
	// rested the account, and its free models went nowhere). A free model
	// the vendor refuses all the same rests on its own refusal.
	if (why == failCredit || why == failQuota) && c.p.PaidBesideFree(c.model) {
		id = c.restID()
	}
	if remoteModelRest(c, why, header) {
		id, r.there = c.restID(), true
	}
	r.Key = id
	if id == c.restID() {
		r.Model = c.model
	}
	r.Rate = rateUntilSaid(why, r.By, body)
	r.Probed = probeable(c, r, header, now) && !probeMissed(id, now)
	restingUntil.Lock()
	if why == failQuota || why == failCredit {
		// out for as long as magpie guessed: refused so the time before
		// too, it is held from now on, and asked again a minute on; a
		// refusal on its own is asked once more. A time the vendor named,
		// or the user set, holds it from the first
		if guessed {
			prev, had := restingUntil.note[id]
			r.first = !had || prev.Why != failQuota && prev.Why != failCredit
			r.askAt = now.Add(fallbackCooldown)
		}
	}
	kept := ladderRest(r) || ladderRest(restingUntil.note[id])
	// a failure answering while a longer rest still stands doesn't shorten
	// it (#3289, wjq990112): a 429 resting for the minute it starts at used
	// to replace the half-hour rest a 402 out of credit had taken a moment
	// before, and the account was asked again as soon as it ended, refused,
	// and rested again, round after round. Whichever rest ends later
	// stands, as a slow start's does here, and a rest narrowed to a model
	// keeps the longer of the two. A time magpie only guessed (askAt set:
	// a quota or credit refusal that named none) gives way to the same
	// refusal's time the vendor names later, though it is sooner: the
	// vendor's word is what the guess stood in for
	prev := restingUntil.note[id]
	named := !prev.askAt.IsZero() && prev.Why == r.Why && (r.By == "resets" || r.By == "retry-after")
	if until, ok := restingUntil.m[id]; !ok || !until.After(r.Until) || named {
		restingUntil.m[id] = r.Until
		restingUntil.note[id] = r
	}
	restingUntil.Unlock()
	if id == c.restKey() && (why == failQuota || why == failCredit) {
		// a pool routed Until used up goes on from it, though it renews
		// before the pool's next request (dzxx0496); one model of it
		// refused leaves the account in use for the others
		routes.Drained(c.rest)
	}
	if kept {
		saveRests() // a magpie started again rests it out, and climbs on
	}
	if r.Probed {
		s.probeLater(c.p, r, now)
	}
	if unread && id == c.restKey() && id != c.restID() {
		narrowing.Add(1)
		go narrowWhenRead(c, r)
	}
	return r
}

// rateUntilSaid says a refusal read as out of quota until the time it
// names (by "resets") calls itself a rate or frequency limit (rateWords,
// words in a link apart): the app words its rest as rate limited, with
// that time. How long it rests, and what it rests, are a quota's.
func rateUntilSaid(why, by string, body []byte) bool {
	return why == failQuota && by == "resets" && rateWords.Match(urlWords.ReplaceAll(wordsOf(body), nil))
}

// vendorOverloaded says a failure is the vendor's servers too busy for
// the request — a 529 (Anthropic's overloaded_error, the ChatGPT backend's
// server_is_overloaded), or a 5xx whose words say overloaded (Gemini's
// 503 "The model is overloaded") — which any account of it would have
// met alike (saidOverloaded).
func vendorOverloaded(status int, body []byte) bool {
	return saidOverloaded(status, string(wordsOf(body)))
}

// modelCooledDown is a relay's refusal of one model whose pooled
// credentials cooled down (modelCooling): out of quota, credit or rate,
// the limit is that model's on the relay, and the key's other models are
// still asked (#3039). Its rest, its ladder and its sink are the model's,
// as a remote magpie's are (remoteModelRest).
func modelCooledDown(why string, body []byte) bool {
	return (why == failQuota || why == failCredit || why == failRate) && modelCooling.Match(wordsOf(body))
}

// remoteModelRest says a refusal for want of quota, credit or rate rests
// only the model it was about, when it came from another computer's magpie
// (a Remote magpie provider, LAN share): that magpie serves each of its
// models from its own providers and rests them itself, so one of them out
// of quota there says nothing of the others (bigsteven on Discord: the
// remote's 429 for one model rested every model on it for the 3 hours it
// named). A restricted account, or one its vendor wants verified first, is
// that computer's account too, on the model it serves there (#1849: the
// restricted one says so in magpie's own words, which no vendor uses, so
// the refusal is read whoever answered). Its key refused, or the computer
// unreachable, still rests it whole, and so does the key's own limit there
// (keyLimited's 429, with remoteKeyLimitHeader), which refuses every model
// alike (#2077). A magpie added by hand, as an OpenAI-compatible provider
// of its own name, is one all the same: its reply names the provider that
// answered there (providerHeader, which keepRetry notes as
// answeredThereHeader), as no vendor's does (lagrangepoint1813
// on Discord: "Remote Magpie: Claude Code: You've hit your session
// limit" answered every model of it for the five hours it named).
func remoteModelRest(c candidate, why string, header http.Header) bool {
	if !c.p.IsRemoteMagpie() && header.Get(answeredThereHeader) == "" || header.Get(remoteKeyLimitHeader) != "" || header.Get(keyLimitThereHeader) != "" {
		return false
	}
	return why == failQuota || why == failCredit || why == failRate || why == failRestricted || why == failVerify
}

// restSlow counts a slow start — let go for no first token in its group's
// FirstToken — against the candidate's model on its account or key
// (restID), and rests that model there from the slowRests'th in a row: a
// minute, twice as long each time after, up to the provider's rest cap.
// One alone rests nothing, a long prompt being slow to start anywhere; a
// run ends when the model answers there (servedCandidate) or slowForget
// passes with none. Its other models and the account's other mates are
// asked as before (小八 on Discord: WorkBuddy's kimi-k3-1 and glm-5.3,
// out of a per-model token limit the vendor never named, stalled 15 times
// in 17 minutes, each request waiting 30s for it before the fallback
// answered). A slow start while it rests for one keeps the count where it
// is, so one request's tries don't stretch it. A provider set never to
// rest after a failure that says nothing of why (RestCapOff) isn't
// rested, nor its slow starts counted.
func restSlow(c candidate, now time.Time) (Rest, bool) {
	if c.p.NoFailRest() {
		return Rest{}, false
	}
	id := c.restID()
	prev, resting := restOf(id)
	routed.Lock()
	run := routed.slow[id]
	if now.Sub(run.at) >= slowForget {
		run.n = 0
	}
	if !resting || prev.Why != failSlow || run.n == 0 {
		run.n++
	}
	run.at = now
	routed.slow[id] = run
	routed.Unlock()
	if run.n < slowRests {
		return Rest{}, false
	}
	d, by := min(fallbackCooldown<<min(run.n-slowRests, 10), c.p.RestCapOr(longestRetry)), "cooldown"
	if run.n > slowRests {
		by = "backoff"
	}
	r := Rest{Why: failSlow, Status: http.StatusGatewayTimeout, Until: now.Add(d), By: by, Failures: run.n, Key: id, Model: c.model}
	if a := c.p.Account; a != nil {
		r.agent, r.user = a.UsageAgent(), a.User
	}
	restingUntil.Lock()
	if until, ok := restingUntil.m[id]; ok && until.After(r.Until) {
		// resting longer already, for what another failure said
		kept := restingUntil.note[id]
		kept.Until = until
		restingUntil.Unlock()
		return kept, true
	}
	ladder := ladderRest(restingUntil.note[id])
	restingUntil.m[id] = r.Until
	restingUntil.note[id] = r
	restingUntil.Unlock()
	if ladder {
		saveRests() // its ended ladder rest is a slow one's now
	}
	return r, true
}

// backingOff says key still rests after a failure that said nothing of
// why (failOther), whose count a failure now would climb.
func backingOff(key string, now time.Time) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	until, ok := restingUntil.m[key]
	return ok && now.Before(until) && restingUntil.note[key].Why == failOther
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
	if c.isOpenRouterFree() && sharedPool || remoteModelRest(c, failRate, header) || modelCooledDown(failRate, body) {
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
	d, by := min(fallbackCooldown<<min(n-1, 10), c.p.RestCapOr(longestRateRest)), "cooldown"
	if n > 1 {
		by = "backoff"
	}
	// a Retry-After is held to the longest wait, as any vendor's "try
	// again in" is; a time the refusal names for when the account is back
	// (resetsAt, or as keepRetry noted it) is a statement of when it is,
	// believed as far as out of quota believes it: the same words rested
	// 8 days read as out of quota and an hour read as a rate limit (#1669)
	said, saidBy := retryAfter(header, now), "retry-after"
	if w := resetsNoted(header, now); w > said {
		said, saidBy = min(w, longestQuota), "resets"
	}
	if w := resetsAt(body, now); w > said {
		said, saidBy = min(w, longestQuota), "resets"
	}
	if said > 0 && said >= d {
		d, by = said, saidBy
	}
	return d, by, n
}

// full is when a subscription — or a key that reads its own windows —
// whose allowance, as last known, has a window used up for the
// candidate's model renews; zero otherwise. Used up is all but
// (usedShare), except In order: there an account at 98% is still tried in
// its turn, and one that fails then isn't benched until its week renews
// unless the vendor said it was out (#530). A Codex account that goes on
// on its credits is never: a window used up doesn't stop it (onCredits).
func (c candidate) full(now time.Time) time.Time {
	a, _ := c.allowance()
	if spendsCredits(c.p, a) {
		return time.Time{}
	}
	return a.Full(c.model, provider.SpentShareOf(c.p.Routing), now)
}

// onCredits: c is a Codex account that goes on past its allowance on its
// credits, as its last reading says, and the user lets it spend them
// (provider.CodexCredits): its allowance used up, the vendor spends the
// credits, so it isn't benched until the window renews (豆子 on Discord).
func (c candidate) onCredits() bool {
	a, _ := c.allowance()
	return spendsCredits(c.p, a)
}

// creditsAgain says whether c, refused with status and body, is asked
// again at once: a Codex account on its credits (onCredits) that the
// vendor said is out of its allowance. At the moment a window is used
// up ChatGPT may refuse the turn before it goes on to the credits; the
// next request is answered on them.
func creditsAgain(c candidate, status int, body []byte) bool {
	return status == http.StatusTooManyRequests && failure(status, body) == failQuota && c.onCredits()
}

// spendsCredits is onCredits for the account of p, whose allowance is a.
func spendsCredits(p provider.Provider, a provider.Allowance) bool {
	return p.Account != nil && p.Account.UsageAgent() == "codex" && a.OnCredits() && provider.CodexCredits(p.Account.User)
}

// creditsLast: c is a Codex account on its credits (onCredits) that the
// user set to spend them only after everything else
// (provider.CodexCreditsLast), and by its last reading a window for the
// model is used up, where an account set not to spend them is held: it
// goes on on its credits now, so it is tried after every candidate that
// isn't (restLast), in its place among those that are. A call ChatGPT
// counts in no window (freeOnChatGPT) spends no credits, so it stays.
// Asked for gokoshiki on Discord: a subscription, then an API key, then
// the subscription's credits.
func (c candidate) creditsLast(now time.Time) bool {
	acct := c.p.Account
	if acct == nil || !provider.CodexCreditsLast(acct.User) || freeOnChatGPT(c.model, acct.Plan) {
		return false
	}
	a, _ := c.allowance()
	return spendsCredits(c.p, a) && a.CapHeld(c.model, provider.WindowCaps{Credits: true}, now) != nil
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
	if src.Get(remoteKeyLimitHeader) != "" {
		// another magpie's gateway key is over its own limit: noted for
		// restAfter, which rests that magpie whole (remoteModelRest), as
		// the held try's header is all it reads (#2791, ggbdpq: the
		// marker was left out, and the key's limit rested one model). A
		// note, not the marker, so a magpie this one answers doesn't read
		// another's key limit as this one's
		dst.Set(keyLimitThereHeader, "1")
	}
	if who := src.Get(providerHeader); who != "" {
		// another magpie refused it, naming its provider that did: this
		// magpie's own name for the provider goes in the same header
		// (noteMember), so it is noted beside it, for restAfter
		dst.Set(answeredThereHeader, who)
	}
	if note, ok := policyRefusal(body); ok {
		// the error the agent gets keeps the vendor's words but not its
		// code (bio_policy): that it was the safety filter goes on beside
		// it, for settle to try the next account (#248)
		dst.Set(refusedHeader, note)
	}
	if zdrCoded.Match(body) {
		// the error the agent gets may lose Command Code's code: that it
		// was its Zero Data Retention setting goes on beside it
		dst.Set(zdrHeader, "1")
	}
}

// keepRetryOut is keepRetry onto a reply that goes to the client as it
// is, with no holdWriter to drop magpie's own notes.
func keepRetryOut(dst, src http.Header, body []byte) {
	keepRetry(dst, src, body)
	dst.Del(answeredThereHeader)
	dst.Del(keyLimitThereHeader)
}

// keyLimitThereHeader carries, from keepRetry to restAfter, that another
// magpie refused with its gateway key's own limit (remoteKeyLimitHeader).
const keyLimitThereHeader = "X-Magpie-Key-Limit-There"

// answeredThereHeader carries, from keepRetry to restAfter, the provider
// another magpie named on its refusal (providerHeader): the refusal is
// that magpie's model's (remoteModelRest).
const answeredThereHeader = "X-Magpie-Answered-There"

// refusedHeader carries, from keepRetry to settle, that an error status
// was the safety filter refusing the request, and what it said.
const refusedHeader = "X-Magpie-Refused"

// resetsHeader carries, from keepRetry to restAfter, when a subscription
// out of quota said it's back. A held error that is passed on after all
// leaves it out; one written straight to the agent carries it, harmlessly.
const resetsHeader = "X-Magpie-Resets-At"

// namedStatusHeader carries, from a relayed stream's error frame to the
// routing loop and restAfter, the status the frame named for itself
// (#4855): a plugin that already sent its 200 can only say what went
// wrong in the frame, and the status it names there — not the one its
// message's words guess — is what the failure rests as. The client never
// sees it.
const namedStatusHeader = "X-Magpie-Named-Status"

// failedNamed is status, or the status a relayed stream's error frame named
// for itself (#4855, namedStatusHeader), which the failure reads as.
func failedNamed(h http.Header, status int) int {
	if n, err := strconv.Atoi(h.Get(namedStatusHeader)); err == nil && n >= 400 && n < 600 {
		return n
	}
	return status
}

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
// can't take a request (routes.LowShare, routes.UsedShare).
const (
	lowShare  = routes.LowShare
	usedShare = routes.UsedShare
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

	// free is each seat's model costing nothing there, by rest key: filled
	// only when the routing weighs by it (routes.Strategy.ByFree)
	free map[string]bool

	// expires is when each free seat's quota is first lost (routes.Expires),
	// by rest key: filled only when the routing weighs by plan
	// (routes.Strategy.ByPlan, Free quota by expiry)
	expires map[string]time.Time

	// speed is each seat's speed lately, by restID (a group's heads can
	// be two models on one key): filled only when the routing weighs by
	// it (routes.Strategy.BySpeed)
	speed map[string][2]float64

	// price is what each seat's model costs at the request's time, by
	// restID (a group's heads can be two models on one key): filled only
	// when the routing weighs by it (routes.Strategy.ByPrice)
	price map[string]seatPrice
}

// seatPrice is a seat's price at the request's time, as Cheapest weighed
// it and the trace tells it: a million tokens in and a million out, and
// whether that is its vendor's peak-hour price.
// priceClock is the hour Cheapest prices at; a test holds it.
var priceClock = time.Now

type seatPrice struct {
	in, out, cache float64
	peak           bool
}

type allowanceKey struct {
	rest, model string
}

func (c candidate) allowanceKey() allowanceKey {
	return allowanceKey{c.rest, c.model}
}

type left struct {
	used float64
	// weekLow: used is at lowShare or more for its budget windows alone,
	// no rate cap of it near full (Allowance.CapUsed, routes.Seat.WeekLow)
	weekLow bool
	renews  []time.Time
	// fullest: when the window used is the share of renews
	// (Allowance.Fullest), zero when not known
	fullest time.Time
	soon    []time.Time // renews as Smart ranks them (Allowance.Renewal)
	lapse   time.Time   // when the soonest of what is left is lost (Allowance.Lapse)
	pace    float64     // weekly pace: share of its week left per hour until it renews
	due     time.Time   // when the window that pace went by renews; zero when not known
	// restarts: when an auto-used reset starts its windows again
	// (Allowance.Restarts), zero when none will; dueRestart: due is that,
	// sooner than the window renews by itself; dueExpiry: due is when
	// credits in it expire, sooner than it renews (Allowance.Pace)
	restarts              time.Time
	dueRestart, dueExpiry bool
	// the count used is the share of, when the vendor counts it so
	// (Allowance.Count): amount of of, in unit
	amount, of float64
	unit       string
	// credits: a Codex account that goes on on its credits once its
	// allowance is used up (spendsCredits); first: one set to spend them
	// before another account (provider.CodexCreditsFirst)
	credits, first bool
	// bal is a prepaid wallet's balance in balUnit (Allowance.Left,
	// #3950); balIn says whose wallet it is, provider and unit, and is
	// empty when no balance was told: routes.Seat.LeftIn
	bal            float64
	balUnit, balIn string
}

// learns: c is a subscription whose allowance isn't known yet, of an agent
// that tells it as it answers (Claude Code's rate_limit_event; a ChatGPT
// account's x-codex-* headers, provider.NoteCodexLimits). A Codex account
// only while it hasn't answered lately (codexAnswered): its reply makes it
// known when it tells the week, and one that told nothing isn't sent
// every request first. Not known and kept after those known, an account
// whose usage can't be read was never sent one, under Use before expiry
// (a whole week ahead of it) and Smart alike (peterpark8527 on Discord).
func learns(c candidate, lefts map[allowanceKey]left) bool {
	if _, known := lefts[c.allowanceKey()]; known || c.p.Account == nil {
		return false
	}
	switch c.p.Account.Agent {
	case "claude":
		return true
	case "codex":
		// only behind one known: of accounts none known, the order stands,
		// and no week is started before it is needed
		return len(lefts) > 0 && !codexAnsweredLately(c.p.Account.UsageAgent(), c.p.Account.User, time.Now())
	}
	return false
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

// routingOf is how a pool routed so is routed now. A routing no strategy
// serves (routes.Find: plugins/rotation off) is its listed order, In
// order's, and so is what the core does for one besides ordering it: a
// turn's mark, spreading a group's sessions, sending the rate limited back
// and a group's heads.
func routingOf(routing string) string {
	if routing == "" || routing == provider.Manual || routes.Served(routing) {
		return routing
	}
	return provider.Ordered
}

// keepsWeek: routed so, a pool keeps an account running low on its week
// alone with those that have room (routes.Seat.WeekLow): Smart and Use
// before expiry, which spend what a reset would lose.
func keepsWeek(routing string) bool {
	r := routingOf(routing)
	return r == "" || r == provider.Pace
}

// goesRound: routed so, a pool goes round by itself (in turn, by weight),
// so its first is marked as the turn's and the rate limited aren't sent
// back.
func goesRound(routing string) bool {
	r := routingOf(routing)
	return r == provider.Rotate || r == provider.Weighted
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
		whose := c.p.ID // whose wallet a balance is in
		if c.p.Account == nil {
			// a key that reads its own windows is weighed by them as an
			// account is; any other key isn't known
			a, ok = keyAllowance(c.p)
		} else {
			ag := c.p.Account.UsageAgent()
			whose = ag
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
			l := left{used: u, renews: r, fullest: a.Fullest(c.model, now), soon: a.Renewal(c.model, now), lapse: a.Lapse(c.model, now), pace: pc, due: due, amount: amt, of: of, unit: unit,
				restarts: rs, dueRestart: !rs.IsZero() && due.Equal(rs), credits: spendsCredits(c.p, a)} // one not known counts as unused
			l.dueExpiry = !l.dueRestart && !l.lapse.IsZero() && due.Equal(l.lapse)
			l.weekLow = u >= lowShare && u < usedShare && a.CapUsed(c.model, now) < lowShare
			l.first = l.credits && provider.CodexCreditsFirst(c.p.Account.User)
			if b, bu, has := a.Left(c.model); has {
				// a wallet's figure is weighed only against the same
				// provider's in the same unit (routes.Seat.LeftIn)
				l.bal, l.balUnit, l.balIn = b, bu, whose+"\x00"+bu
			}
			wg.lefts[c.allowanceKey()] = l
		}
	}
	if len(cs) < 2 {
		return cs, wg
	}
	// the routing's strategy orders them (internal/routes): Smart, or one
	// a plugin registered; one not registered leaves them in their order
	st, ok := routes.Find(p.Routing)
	if !ok {
		return cs, wg
	}
	seats := make([]routes.Seat, len(cs))
	routed.Lock()
	for i, c := range cs {
		l, known := wg.lefts[c.allowanceKey()]
		pace := l.pace
		if !known && c.p.Account != nil {
			pace = provider.FreshPace
		}
		seats[i] = routes.Seat{Key: c.rest, Account: c.p.Account != nil, Known: known, Learns: learns(c, wg.lefts),
			Used: l.used, WeekLow: l.weekLow, Soon: l.soon, Lapse: l.lapse, Pace: pace, Weight: keyWeight(c), Resting: resting(c), Tokens: routed.used[c.restKey()].now(now), Credits: l.credits, First: l.first,
			Left: l.bal, LeftIn: l.balIn, Spent: spentRest(c)}
	}
	routed.Unlock()
	if st.ByUse {
		wg.tokens = map[string]float64{}
		for i, c := range cs {
			wg.tokens[c.rest] = seats[i].Tokens
		}
	}
	if st.ByFree {
		// only a routing that weighs by it pays for working it out: it
		// reads each candidate's provider list (provider.FreeModel)
		wg.free = map[string]bool{}
		for i, c := range cs {
			free := c.p.FreeModel(c.model)
			seats[i].Free, wg.free[c.rest] = free, free
		}
	}
	if st.ByPlan {
		// an account's plan as its vendor or plugin named it
		// (Account.Plan): a free one is spent before a paid one, and the
		// trace tells it free with when its quota is first lost
		if wg.free == nil {
			wg.free = map[string]bool{}
		}
		wg.expires = map[string]time.Time{}
		for i, c := range cs {
			seats[i].FreePlan = c.p.Account != nil && provider.FreePlan(c.p.Account.Plan)
			if seats[i].FreePlan {
				wg.free[c.rest] = true
			}
			if wg.free[c.rest] {
				if t := routes.Expires(seats[i]); !t.IsZero() {
					wg.expires[c.rest] = t
				}
			}
		}
	}
	if st.BySpeed {
		// a map lookup each (speed.go): what was measured as answers
		// ended, never the usage history
		wg.speed = map[string][2]float64{}
		for i, c := range cs {
			seats[i].TTFT, seats[i].TPS = speedOf(c, now)
			wg.speed[c.restID()] = [2]float64{seats[i].TTFT, seats[i].TPS}
		}
	}
	if st.ByPrice {
		// priced as Usage would count a call made now: the hour decides
		// a peak-billed vendor's price and a rate window's (provider.PriceAt)
		set, at := settings.Shared(), priceClock()
		wg.price = map[string]seatPrice{}
		for i, c := range cs {
			pr, ok := c.p.PriceAt(set, c.model, at)
			if !ok {
				continue
			}
			peak := pr.Peak.At(at)
			pr = pr.In(at)
			seats[i].Priced, seats[i].Cost, seats[i].CacheCost = true, pr.Input+pr.Output, pr.CacheRead
			wg.price[c.restID()] = seatPrice{in: pr.Input, out: pr.Output, cache: pr.CacheRead, peak: peak}
		}
	}
	order := routes.Apply(st, p.ID, seats)
	out := make([]candidate, 0, len(cs))
	for _, i := range order {
		out = append(out, cs[i])
	}
	cs = out
	if st.Exact {
		return cs, wg
	}
	if p.Account == nil {
		sort.SliceStable(cs, func(i, j int) bool { return keyFit(cs[i].p, cs[i].model, from) < keyFit(cs[j].p, cs[j].model, from) })
	}
	return cs, wg
}
