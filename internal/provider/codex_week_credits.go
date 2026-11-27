package provider

// A Codex account's week used up while its credits go down: a reset it
// spends by itself when the week runs out is spent then, from the reading
// that shows them going (shangyuan0818 on X). The gateway spends one
// before a request it relays goes on the account's credits, and on a 429
// (resetBeforeCredits, autoReset), but a request that never passes it —
// the Codex app or CLI signed in without magpie, another CODEX_HOME, a
// cloud task, another computer — spends the credits all the same, and no
// reset was spent while the readings showed the week at 100% and the
// credits falling for over an hour. Credits cost money and a reset is
// free, so a fall between two readings in the same used-up week is the
// sign: AutoUseCodexReset reads the week again and spends one, one a
// week at most, within the days range, as it does for a request.

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// weekCreditsTimeout bounds the reading and the spend a fall starts.
var weekCreditsTimeout = 30 * time.Second

var weekCredits = struct {
	sync.Mutex
	m map[string]weekCreditsSeen // by account, lowercased
}{m: map[string]weekCreditsSeen{}}

type weekCreditsSeen struct {
	week     time.Time // when the used-up week ends
	left     float64   // the credits the last reading in it said
	spending bool      // a spend it started still runs
}

// noteWeekCredits keeps what a reading of the Codex account user said of
// its week and credits at now, and spends a reset (AutoUseCodexReset)
// when the credits left went down since the last reading in the same
// used-up week. left is nil when the reading didn't say them, or they are
// unlimited; nothing is spent then, nor without a reset held.
func noteWeekCredits(user string, windows []QuotaWindow, resets *ResetCredits, left *float64, now time.Time) {
	key := strings.ToLower(strings.TrimSpace(user))
	if key == "" {
		return
	}
	week := weekUsedUp(windows, now)
	onCredits := false
	for _, w := range windows {
		onCredits = onCredits || w.Credits
	}
	weekCredits.Lock()
	defer weekCredits.Unlock()
	was, seen := weekCredits.m[key]
	if week == nil || !onCredits || left == nil || resets == nil || resets.Count <= 0 || !AutoResetsWeek("codex", user) {
		if seen && !was.spending {
			delete(weekCredits.m, key)
		}
		return
	}
	next := weekCreditsSeen{week: *week, left: *left, spending: was.spending}
	fell := seen && sameWeek(was.week, *week) && *left < was.left
	if !fell || was.spending {
		weekCredits.m[key] = next
		return
	}
	next.spending = true
	weekCredits.m[key] = next
	goRead(func() {
		ctx, cancel := context.WithTimeout(context.Background(), weekCreditsTimeout)
		defer cancel()
		out, err := AutoUseCodexReset(ctx, user)
		switch {
		case err != nil:
			log.Printf("codex reset for %s not used, its credits going while its week is used up: %v", user, err)
		case out.Code == "reset":
			log.Printf("codex reset used for %s, its credits going while its week is used up: %s", user, out.Text())
		case out.Code != "":
			log.Printf("codex reset for %s not used, its credits going while its week is used up: %s", user, out.Text())
		}
		weekCredits.Lock()
		if s, ok := weekCredits.m[key]; ok {
			s.spending = false
			weekCredits.m[key] = s
		}
		weekCredits.Unlock()
	})
}

// sameWeek says whether two readings' ends of the used-up week are of one
// week: a few seconds apart as the backend rounds them, not days.
func sameWeek(a, b time.Time) bool {
	d := a.Sub(b)
	return d > -time.Hour && d < time.Hour
}
