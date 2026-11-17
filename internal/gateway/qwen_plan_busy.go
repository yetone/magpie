package gateway

import (
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// qwenPlanHost is the Qwen AI platform's Token Plan host, its preset's
// (qwen-token-plan) and a custom provider's at it.
const qwenPlanHost = "token-plan.maas.qianwenaiapi.com"

// qwenPlanBusy says p is the Qwen AI platform's Token Plan and its 429 is
// the plan's concurrency limit: under many requests at once the host
// answers "Allocated quota exceeded, please increase your quota limit"
// (dashScopeRate's words) with the plan's quota far from used, and the
// same request asked again a moment later is served (pelikanade on X).
// It rests as a rate limit already (dashScopeRate); here it is also
// asked again on the same account, as a rate limit is, before any byte
// reaches the agent (passingFor), and a group's member is asked again
// in place before the group moves on. Only this host: DashScope's
// pay-as-you-go TPM limit says the same words and lasts the minute, and
// another vendor's "Allocated quota exceeded" is its own.
func qwenPlanBusy(p provider.Provider, status int, body []byte) bool {
	if status != http.StatusTooManyRequests || p.Preset != "qwen-token-plan" && p.Host() != qwenPlanHost {
		return false
	}
	return dashScopeRate(p, status, body)
}

// jittered is a pause of about d, between half and one and a half of it,
// so the many requests one burst of concurrency turned away aren't all
// sent again in the same instant, to be turned away again together.
func jittered(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d/2 + rand.N(d)
}

// passingFor is passing for c's failure: the Qwen Token Plan's
// concurrency 429 (qwenPlanBusy) passes as a rate limit does, for up to
// rateRetries tries again after jittered pauses of about 1, 2 and 4s, or
// what Retry-After says; any other failure is passing's.
func passingFor(c candidate, status int, header http.Header, body []byte, again int) (time.Duration, bool) {
	if !qwenPlanBusy(c.p, status, body) || routedThere(header) {
		return passing(status, header, body, again)
	}
	wait := jittered(retryPause << again)
	if d := retryAfter(header, time.Now()); d > 0 {
		wait = d
	}
	return wait, wait <= longestPause && again < rateRetries
}

// busyFor is busy for c's failure: the Qwen Token Plan's concurrency
// 429 is a moment's, as a rate limit is, and a group's or a plain
// provider's patience asks it again.
func busyFor(c candidate, status int, body []byte) bool {
	return qwenPlanBusy(c.p, status, body) || busy(status, body)
}
