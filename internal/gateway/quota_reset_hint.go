package gateway

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// quotaResetBack is the first known allowance reset of the pool. Unlike
// soonestBack it does not use an early probe or a guessed cooldown. Do
// not tell an agent to wait for quota when another candidate may answer
// sooner (or its failure was not about quota).
func quotaResetBack(now time.Time, pools ...[]candidate) time.Time {
	var back time.Time
	for _, cs := range pools {
		for _, c := range cs {
			r, ok := restingOf(c)
			if !ok || r.Why != failQuota && r.Why != failCredit || !quotaStatus(r.Status) || r.there || r.first || !r.askAt.IsZero() || !r.Until.After(now) {
				return time.Time{}
			}
			// A reset on a plain frequency limit is also recorded as quota
			// by routing. Inspect the vendor's message, not its error type
			// (Claude's real quota errors are rate_limit_error too).
			words := strings.TrimPrefix(r.said, c.p.Name+": ")
			if r.Why == failQuota && rateWords.MatchString(words) && !plannedWords.MatchString(words) {
				return time.Time{}
			}
			switch r.By {
			case "resets", "window", "retry-after":
			default:
				return time.Time{}
			}
			if back.IsZero() || r.Until.Before(back) {
				back = r.Until
			}
		}
	}
	return back
}

// cappedQuotaResetBack is the first known renewal when the plan left
// every eligible candidate out at a usage cap. An unread window or a
// money cap is not an allowance reset.
func cappedQuotaResetBack(ws []Weighed, now time.Time) time.Time {
	var back time.Time
	for _, w := range ws {
		if w.Barred || w.Held || w.StandIn {
			continue
		}
		if w.Capped == 0 || w.Unread != "" || w.SpendCap > 0 || w.CapBack == nil || !w.CapBack.After(now) {
			return time.Time{}
		}
		if back.IsZero() || w.CapBack.Before(back) {
			back = *w.CapBack
		}
	}
	return back
}

// Claude Code using a gateway token exposes the API error's text to its
// callers, but does not expose subscription reset headers as native
// rate_limit_info. Tell those callers the countdown in the error itself
// (#7405), on magpie's own line. Retry-After has a separate client wait
// policy; it can be capped without changing this actual pool deadline.
func quotaResetHint(msg string, back time.Time) string {
	wait := time.Until(back)
	if msg == "" || wait <= 0 || strings.Contains(msg, "(reset after ") {
		return msg
	}
	return msg + magpieSays + "(reset after " + ((wait + time.Second - 1).Truncate(time.Second)).String() + ")"
}

// withQuotaResetHint only changes the final Anthropic error's message.
func withQuotaResetHint(b []byte, back time.Time) []byte {
	msg := gjson.GetBytes(b, "error.message")
	if told := quotaResetHint(msg.String(), back); msg.Type == gjson.String && told != msg.String() {
		return setErrorField(b, "message", told)
	}
	return b
}
