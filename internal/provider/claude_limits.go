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

// claudeHeard is how long what Claude Code said as it answered stands in
// for the usage endpoint when that fails.
const claudeHeard = time.Hour

// NoteClaudeLimits keeps what Claude Code said of user's allowance as it
// answered, in place of the windows of the same names last read, and has
// routing weigh the account by it.
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
	c := &claudeUsage
	c.Lock()
	if c.m == nil {
		c.m = map[string]claudeUsageEntry{}
	}
	e := c.m[key]
	ws := []QuotaWindow{}
	for _, w := range e.ws {
		if !slices.ContainsFunc(heard, func(h QuotaWindow) bool { return h.Name == w.Name }) {
			ws = append(ws, w)
		}
	}
	ws = append(ws, heard...)
	order := func(w QuotaWindow) int {
		for i, k := range claudeKinds {
			if k.name == w.Name {
				return i
			}
		}
		return len(claudeKinds)
	}
	slices.SortStableFunc(ws, func(a, b QuotaWindow) int { return order(a) - order(b) })
	e.ws, e.at, e.heard = ws, time.Now(), time.Now()
	e.err = nil // a new observation supersedes the failed reading
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
