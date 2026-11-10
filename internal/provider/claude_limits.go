package provider

import (
	"slices"
	"strings"
	"time"
)

// Claude Code tells what an account has left as it answers (its
// rate_limit_event, from Anthropic's anthropic-ratelimit-unified-* headers),
// so an account whose usage endpoint keeps turning magpie away (429) is
// known all the same once it has answered.

// ClaudeLimit is one allowance window as Claude Code tells it: its kind
// (five_hour, seven_day, seven_day_opus, seven_day_sonnet,
// seven_day_overage_included), the share used,
// 0–1, and when it renews, in Unix seconds (0 when not said).
type ClaudeLimit struct {
	Kind     string
	Used     float64
	ResetsAt int64
}

// claudeKinds are the windows Claude Code names, as the usage endpoint's
// are kept, in that order.
var claudeKinds = []struct {
	kind, name, model string
	span              time.Duration
}{
	{"five_hour", "5 hours", "", 5 * time.Hour},
	{"seven_day", "7 days", "", 7 * 24 * time.Hour},
	{"seven_day_opus", "7 days · Opus", "opus", 7 * 24 * time.Hour},
	{"seven_day_sonnet", "7 days · Sonnet", "sonnet", 7 * 24 * time.Hour},
	// the week's allowance of the models Claude Code lists as taking
	// usage credits past it ("Fable", "Fable 5", "Fable 5.1"), which it
	// calls the Fable limit: the usage endpoint's weekly_scoped Fable
	{"seven_day_overage_included", "7 days · Fable", "fable", 7 * 24 * time.Hour},
}

// claudeKindOrder is w's place among claudeKinds, the windows Claude Code
// names, as a reading orders them; one it doesn't name goes last.
func claudeKindOrder(w QuotaWindow) int {
	for i, k := range claudeKinds {
		if k.name == w.Name {
			return i
		}
	}
	return len(claudeKinds)
}

// claudeWithUnstarted is a whole reading ws with the 5-hour window every
// Claude account has (as claudeWindowsExpected), when it leaves it out, as not
// started: Anthropic leaves a window out (null) while it hasn't started, so
// /usage has no "Current session" line for an account not used in the last
// five hours, and its card showed no 5-hour window at all (motui9535).
// The week needs none: a reading that tells any window tells the week, a
// session used being the week used too. Ordered as Claude Code names
// them, the 5-hour window first.
func claudeWithUnstarted(ws []QuotaWindow) []QuotaWindow {
	out := withExpected(ws, []QuotaWindow{{Name: "5 hours", Span: 5 * time.Hour}})
	slices.SortStableFunc(out, func(a, b QuotaWindow) int { return claudeKindOrder(a) - claudeKindOrder(b) })
	return out
}

// claudeHeard is how long what Claude Code said as it answered stands in
// for the usage endpoint when that fails.
const claudeHeard = time.Hour

// NoteClaudeLimits keeps what Claude Code said of user's allowance as it
// answered, in place of the windows of the same names last read, and has
// routing weigh the account by it. An answer tells only some windows (a
// rate_limit_event often names five_hour alone): the others stand as last
// read, after a restart as kept on disk (quotas.json), so the week isn't
// lost to an answer that didn't name it (ifccod7391564 on X).
func NoteClaudeLimits(user string, ls []ClaudeLimit) {
	var heard []QuotaWindow
	for _, l := range ls {
		i := slices.IndexFunc(claudeKinds, func(k struct {
			kind, name, model string
			span              time.Duration
		}) bool {
			return k.kind == l.Kind
		})
		if i < 0 {
			continue
		}
		k := claudeKinds[i]
		w := QuotaWindow{Name: k.name, Used: l.Used * 100, Span: k.span, Model: k.model}
		if l.ResetsAt > 0 {
			t := time.Unix(l.ResetsAt, 0)
			w.ResetsAt = &t
		}
		heard = append(heard, w)
	}
	if len(heard) == 0 {
		return
	}
	key := strings.ToLower(user)
	onDisk := keptClaudeWindows(key) // read before claudeUsage is held: lastQuotas has its own lock
	c := &claudeUsage
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	e := c.m[key]
	kept := e.ws
	if len(kept) == 0 {
		kept = onDisk
	}
	ws := []QuotaWindow{}
	for _, w := range kept {
		if !slices.ContainsFunc(heard, func(h QuotaWindow) bool { return h.Name == w.Name }) {
			ws = append(ws, w)
		}
	}
	ws = append(ws, heard...)
	slices.SortStableFunc(ws, func(a, b QuotaWindow) int { return claudeKindOrder(a) - claudeKindOrder(b) })
	e.ws, e.at, e.heard = ws, time.Now(), time.Now()
	e.heardSeq = quotaReadSequence.Add(1) // after every reading started before it, as the cards order them
	e.err = nil                           // a new observation supersedes the failed reading
	c.m[key] = e
	c.Unlock()
	StaleAllowance("claude", user)
}

// KeptClaudeLimits is what is kept of user's allowance as of now, in the
// windows Claude Code names (ClaudeLimit's kinds), each that says when it
// renews: what Claude Code last said as it answered, or its /usage read.
// Nil when nothing is kept.
func KeptClaudeLimits(user string) []ClaudeLimit {
	claudeUsage.Lock()
	e, ok := claudeUsage.m[strings.ToLower(user)]
	claudeUsage.Unlock()
	if !ok {
		return nil
	}
	var out []ClaudeLimit
	for _, w := range e.windows(time.Now()) {
		if w.ResetsAt == nil {
			continue // a window that renews when nobody knows says nothing of its own
		}
		for _, k := range claudeKinds {
			if k.name == w.Name {
				out = append(out, ClaudeLimit{Kind: k.kind, Used: w.Used / 100, ResetsAt: w.ResetsAt.Unix()})
				break
			}
		}
	}
	return out
}

// keptClaudeWindows are the windows of Claude account key (lower case)
// kept on disk (quotas.json) that haven't renewed since: what a cold start
// knows of it before /usage or an answer tells it again.
func keptClaudeWindows(key string) []QuotaWindow {
	q, ok := lastReading("claude/" + key)
	if !ok {
		return nil
	}
	var out []QuotaWindow
	for _, w := range q.Windows {
		if w.ResetsAt != nil { // renewed since (elapsed) or never said: unknown now
			out = append(out, w)
		}
	}
	return out
}
