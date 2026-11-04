package gateway

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// didNotStream is in the error for an upstream that answered a stream
// request with a whole reply: no ask of it will read as a stream, however
// often it goes out, so it is no failure a moment may mend — the same-one
// retries (passing) and a group's patience (busy) both leave it alone.
const didNotStream = "did not stream"

// A group's patience (#1418): when every member has failed in a way that
// passes — overloaded, busy, rate limited for a moment — and nothing of a
// reply has gone to the agent, the request isn't ended on the last one's
// error. After a pause, longer each round and never shorter than a
// member's Retry-After, those members are asked again in turn, for as long
// as the group's Patience (provider.Group.Waits) lasts, counted from the
// first time every member had failed, when the agent would have had the
// error. The members take turns: the last one isn't first asked again
// alone, as the last one left of a plain provider is (passing), while
// another member is busy too. A streaming agent is kept alive with SSE
// comments through the pause, as through a held stream. A plain provider,
// which the agent picked as itself, is kept the same way once the retries
// passing gives the last one left are spent, its accounts its members,
// for its own Patience, unset singlePatience.

// singlePatience is how long a request to one provider, not a group, is
// kept once it has failed in a way that passes and the same-one retries
// (passing) are spent: as long as a group that sets no Patience waits
// (zoecats on Discord), unless the provider sets its own Patience (magpie
// provider set <id> patience=), which wins. A group's, set or not, is the
// group's.
var singlePatience = provider.PatienceDefault * time.Second

// replanRounds is how many times the members are asked again, at most:
// a backstop that never comes before the patience does. The pauses alone
// (2s, 4s, 8s, 16s, then 30s each) add up to PatienceLongest in 11 rounds,
// so a group set to wait 240s waits 240s; with 6 it gave up at 90s of
// pauses, whatever it was set to (real-jacket on #1418: patience=240,
// replan=6, ~103s in all).
const replanRounds = 12

// replanLongest is the longest pause between rounds: 30 times retryPause,
// 30s.
func replanLongest() time.Duration { return 30 * retryPause }

// busyTry is a member that failed in a way that passes, and when it may
// be asked again: now, or when its Retry-After says.
type busyTry struct {
	c     candidate
	ready time.Time
}

// busy says whether a failure is one a member may be over in a moment:
// overloaded, unavailable, timed out, or a rate limit that isn't the
// plan's or the money's, nor an account's token wall (walled). A proxy
// that refused the connection, a quota used up, response protection's
// 502, or an upstream that answered a stream request with a whole reply
// (didNotStream) isn't: each fails the same however often it is asked.
func busy(status int, body []byte) bool {
	switch {
	case busyStatus(status):
		return failure(status, body) == failOther && !protectionRefused(status, body) &&
			!bytes.Contains(body, []byte(didNotStream))
	case status == 429:
		return failure(status, body) == failRate && !creditWords.Match(wordsOf(body)) && !walled(body)
	}
	return false
}

// busyStatus says whether an error status is one a vendor or the relay in
// front of it gives for a moment's trouble: a timeout, an internal error,
// a gateway or the service unavailable, Anthropic's 529 overloaded, and
// Cloudflare's 520 to 524 (the origin gave an unknown error, is down,
// timed out, unreachable, or slow to answer), which a relay behind
// Cloudflare answers with now and then (vdaoren on Discord: an API-key
// provider's 4xx/5xx went straight to the agent). The same-one retries
// (passing) and the patience's rounds (busy) both read it.
func busyStatus(status int) bool {
	switch status {
	case 408, 500, 502, 503, 504, 529, 520, 521, 522, 523, 524:
		return true
	}
	return false
}

// noteBusy adds c to the members to ask again, once, with when its vendor
// said it may be asked.
func noteBusy(list []busyTry, c candidate, header http.Header) []busyTry {
	if slices.ContainsFunc(list, func(b busyTry) bool { return b.c.restKey() == c.restKey() && b.c.effort == c.effort }) {
		return list
	}
	now := time.Now()
	return append(list, busyTry{c: c, ready: now.Add(retryAfter(header, now))})
}

// waitsOf is how long a member may be kept for replan: its own Patience
// seconds when set (including PatienceOff → 0), else the asked group's
// Waits (fallback).
func waitsOf(patience int, fallback time.Duration) time.Duration {
	switch {
	case patience < 0:
		return 0
	case patience == 0:
		return fallback
	}
	return time.Duration(min(patience, provider.PatienceLongest)) * time.Second
}

// maxWaits is the longest waitsOf among cands, for holding the last
// failure while any member's budget may still cover a replan (#2410).
func maxWaits(cands []candidate, fallback time.Duration) time.Duration {
	var m time.Duration
	for _, c := range cands {
		if w := waitsOf(c.patience, fallback); w > m {
			m = w
		}
	}
	return m
}

// replan is, of the members that failed so in this round, those to ask
// again and the pause before: retryPause doubled each round up to
// replanLongest, and as long as the soonest Retry-After. Each member's
// own Patience budget (#2410) is counted from waitFrom; a member whose
// budget is spent, or Off, is left out. ok is false when the rounds are
// spent, or no member still has budget covering the pause.
func replan(list []busyTry, round int, waitFrom time.Time, fallback time.Duration) ([]candidate, time.Duration, bool) {
	if len(list) == 0 || round >= replanRounds {
		return nil, 0, false
	}
	now := time.Now()
	wait := min(retryPause<<(round+1), replanLongest())
	// a vendor's time is read in whole seconds: one back within a second
	// past a member's budget is asked as the budget ends, not given up on
	// (zoecats on Discord: "will reset in 1 minutes" against the minute a
	// lone provider waits by default, which the moment between the
	// failure and its note put a hair past)
	list = slices.Clone(list)
	for i, b := range list {
		if budget := waitsOf(b.c.patience, fallback); budget > 0 {
			end := waitFrom.Add(budget)
			if b.ready.After(end) && !b.ready.After(end.Add(time.Second)) {
				list[i].ready = end
			}
		}
	}
	soonest := list[0].ready
	for _, b := range list[1:] {
		soonest = minTime(soonest, b.ready)
	}
	wait = max(wait, soonest.Sub(now))
	at := now.Add(wait)
	var alive []busyTry
	for _, b := range list {
		budget := waitsOf(b.c.patience, fallback)
		if budget <= 0 {
			continue
		}
		if at.After(waitFrom.Add(budget)) {
			continue // this member's budget is spent
		}
		alive = append(alive, b)
	}
	if len(alive) == 0 {
		return nil, 0, false
	}
	var next, later []candidate
	for _, b := range alive {
		if b.ready.After(at) {
			later = append(later, b.c) // still asking for time: after those back
			continue
		}
		next = append(next, b.c)
	}
	return append(next, later...), wait, true
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// pauseAlive waits d, keeping a streaming agent alive as a held stream
// does once the request has gone keepHeldAfter without a word. false: the
// agent went away first.
func pauseAlive(ctx context.Context, w http.ResponseWriter, kept *keptAlive, streams bool, start time.Time, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	var hw *holdWriter
	if streams && kept != nil && kept.proto != provider.Gemini {
		hw = newHoldWriter(w, true)
		hw.alive, hw.streams = kept, true
	}
	tick := time.NewTicker(min(watchEvery, max(d, time.Millisecond)))
	defer tick.Stop()
	for {
		if hw != nil && time.Since(start) >= keepHeldAfter {
			hw.keepAlive()
		}
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}
