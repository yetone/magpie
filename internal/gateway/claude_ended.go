package gateway

import (
	"time"
)

// A run that ends during a turn keeps its session on disk a while after
// (#6528): the run waited on the client's AskUserQuestion past runTimeout,
// or its client went away mid-reply and the turn couldn't be taken back
// (letGo). Its Claude Code is gone, and the results or the retry that come
// after found no run: each was told the whole conversation by a new Claude
// Code, writing all of it to the cache again (recih: 118K for an answer
// given two minutes late, 305K for two retried sub-agents and their delayed
// AgentSummary). The session as Claude Code wrote it, with where each call
// of the client's tools is in it (lines) and the conversation each reply
// answered, is what a branch is cut from (branch, retryBranch) as from a
// live run's: each takes a copy of its own, so the file is never written
// again, and a later request on the same history (the AgentSummary that
// comes after the retry) branches off it too.
//
// endedMost is how many ended runs are kept so; endedLongest how long each
// is, as long as Anthropic's cache keeps what it asked (claudeCacheTTL).
// The gateway stopping keeps none (abortAll), and its files are swept at
// the next start (sweepSessions).
const endedMost = 16

var endedLongest = idleLongest

// endedRun is when an ended run was kept, and the timer that lets it go.
type endedRun struct {
	at    time.Time
	timer *time.Timer
}

// keepable says the session of a run that just ended can be kept for a
// branch to be cut from: a session on disk with a line or a turn to cut
// at, Claude Code's own record of its conversation — not one on the shelf
// already, one whose turn was taken back (rewound, which the file may not
// say), or one the client marks its own cache in.
func (r *subscriptionRun) keepable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID != "" && len(r.sessions) > 0 && !r.shelved && !r.clientMarks && !r.rewound &&
		(len(r.lines) > 0 || r.backLine != "" || r.backMsg != "")
}

// keepEnded keeps the session of a run that just ended (keepable) for a
// branch to be cut from, and says whether it did, with the runs let go
// for it past endedMost, whose files the caller removes (b.mu held): not
// as the gateway stops, nor one whose account was handed over to Claude
// Code itself.
func (b *subscriptionBridge) keepEnded(run *subscriptionRun) (bool, []*subscriptionRun) {
	if b.stopping || run.handed {
		return false, nil
	}
	if b.ended == nil {
		b.ended = map[*subscriptionRun]endedRun{}
	}
	if e, ok := b.ended[run]; ok { // removed again: kept from now
		e.timer.Stop()
		delete(b.ended, run)
	}
	var drop []*subscriptionRun
	for len(b.ended) >= endedMost {
		var oldest *subscriptionRun
		for r, e := range b.ended {
			if oldest == nil || e.at.Before(b.ended[oldest].at) {
				oldest = r
			}
		}
		b.ended[oldest].timer.Stop()
		delete(b.ended, oldest)
		drop = append(drop, oldest)
	}
	b.ended[run] = endedRun{at: time.Now(), timer: time.AfterFunc(endedLongest, func() { b.dropEnded(run) })}
	return true, drop
}

// dropEnded lets an ended run's session go, its time over.
func (b *subscriptionBridge) dropEnded(run *subscriptionRun) {
	b.mu.Lock()
	e, ok := b.ended[run]
	delete(b.ended, run)
	b.mu.Unlock()
	if ok {
		e.timer.Stop()
		run.removeFiles()
	}
}

// dropAllEnded lets every ended run's session go, as the gateway stops.
func (b *subscriptionBridge) dropAllEnded() {
	b.mu.Lock()
	b.stopping = true
	ended := b.ended
	b.ended = nil
	b.mu.Unlock()
	for run, e := range ended {
		e.timer.Stop()
		run.removeFiles()
	}
}

// endedRuns is the ended runs kept, with when each ended (b.mu held).
func (b *subscriptionBridge) endedRuns() map[*subscriptionRun]time.Time {
	m := make(map[*subscriptionRun]time.Time, len(b.ended))
	for r, e := range b.ended {
		m[r] = e.at
	}
	return m
}

// removeFiles removes the run's session.
func (r *subscriptionRun) removeFiles() {
	r.mu.Lock()
	sid, sessions := r.sessionID, r.sessions
	r.mu.Unlock()
	removeSession(sessions, sid)
}
