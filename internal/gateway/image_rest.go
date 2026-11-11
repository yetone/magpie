package gateway

import (
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func init() { provider.ImageRest = imageRestOf }

// imageRestOf is when the ChatGPT account user draws again, as the refusal
// that rested its image model said (restDrawer; usage_limit_reached's
// resets_at), while that rest holds. It is the only thing magpie learns of
// the account's image allowance, which the backend shows no share of
// (provider.ImageLane, #6160), and it is only that: not when the allowance
// began, nor what it counted. The latest of several resting image models
// (a rest per model) is the one that says when drawing is back.
func imageRestOf(agent, user string, now time.Time) (time.Time, bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	var last time.Time
	for k, r := range restingUntil.note {
		if r.agent != agent || !strings.EqualFold(r.user, user) || !strings.HasPrefix(r.Model, codexDrawerFamily) {
			continue
		}
		if until := restingUntil.m[k]; until.After(now) && until.After(last) {
			last = until
		}
	}
	return last, !last.IsZero()
}
