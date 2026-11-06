// Package tune works out, from what one user's agents really did, the
// settings that would have cost that user the fewest tokens: when each
// agent compacts its conversation, and how long Claude Code keeps its
// prompt cache. Every setting is tried on the user's own calls of the
// period, replayed one by one: the same prompts growing by the same
// amounts at the same times, compacted or read from the cache as the
// setting would have had them. What it says is so for this user and this
// agent only; another's habits give other answers.
//
// Tokens are weighed by what the vendor charges for them against a plain
// input token, which is what a subscription's quota and an API bill both
// count: a cache read is a tenth of one, a cache write a quarter more (two
// for an hour's), an output token several.
package tune

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// Call is one model call of an agent's conversation, as tune replays it.
type Call struct {
	Time   time.Time
	Stream string // the conversation it is in: its file, a subagent's its own
	Sub    bool   // a subagent's, a workflow's or a helper's, not the main conversation
	Model  string // as the agent asked for it, without [1m]
	Long   bool   // asked for with the long context ([1m])
	Prompt int    // the prompt, all of it: input, cache read and cache write
	Out    int
	Wrote  int // of Prompt, written to the cache
	Wrote1 int // of Wrote, kept an hour
	// Compact is how long a compaction kept the agent waiting, in ms, on the
	// call its file tells it with; 0 when none or the agent doesn't say
	Compact int64
}

// FromCalls makes tune's calls of an agent's calls read from its files,
// leaving out those that failed or carried no prompt.
func FromCalls(cs []sessions.Call) []Call {
	out := make([]Call, 0, len(cs))
	for _, c := range cs {
		p := c.Input + c.CacheRead + c.CacheWrite
		if c.Error != "" || p <= 0 {
			continue
		}
		m, long := c.Requested, false
		if m == "" {
			m = c.Model
		}
		if strings.HasSuffix(strings.ToLower(m), "[1m]") {
			m, long = m[:len(m)-4], true
		}
		stream := c.File
		if stream == "" {
			stream = c.Agent + "/" + c.Session
		}
		out = append(out, Call{
			Time: c.Time, Stream: stream, Model: strings.ToLower(m), Long: long,
			Sub:    strings.Contains(filepath.ToSlash(c.File), "/subagents/"),
			Prompt: p, Out: c.Output, Wrote: c.CacheWrite, Wrote1: c.CacheWrite1h,
			Compact: c.Compact,
		})
	}
	return out
}

// Weights is what a vendor charges for each kind of token against one of
// plain input.
type Weights struct {
	Read, Write, Write1h, Out float64
}

// WeightsFor is the weights of the vendor of a model: Anthropic charges a
// quarter more to write its cache (twice for an hour's) and a tenth to
// read it; OpenAI writes it at the input's price and reads it at a tenth.
func WeightsFor(model string) Weights {
	if strings.HasPrefix(model, "claude") {
		return Weights{Read: 0.1, Write: 1.25, Write1h: 2, Out: 5}
	}
	return Weights{Read: 0.1, Write: 1, Write1h: 1, Out: 8}
}

// Weigh gives the weights a model's calls are replayed at: what magpie
// bills the provider that served them, where the caller knows it (#3435:
// Copilot bills an hour's cache write as a 5-minute one), else WeightsFor.
type Weigh func(model string) Weights

func (w Weigh) of(model string) Weights {
	if w == nil {
		return WeightsFor(model)
	}
	return w(model)
}

// Point is one setting tried and what the period would have cost with it,
// and how many compactions it would have made there: what a window over
// the leanest buys, said beside what it costs (#1860). Wait is the seconds
// those compactions would have kept the user waiting, when the agent's
// files time them (Facts.Took).
type Point struct {
	Value    int     `json:"value"`
	Cost     float64 `json:"cost"`
	Compacts int     `json:"compacts"`
	Wait     int     `json:"wait,omitempty"`
}

// Facts are what a replay found of the user's habits, said beside the
// advice so it reads as theirs.
type Facts struct {
	Calls    int     `json:"calls"`
	Streams  int     `json:"streams"`
	Compacts int     `json:"compacts,omitempty"` // seen in the period
	Peak     int     `json:"peak,omitempty"`     // the median prompt a compaction was made at
	After    int     `json:"after,omitempty"`    // the median prompt right after one
	Growth   int     `json:"growth,omitempty"`   // the median a prompt grows by from one call to the next
	Rework   int     `json:"rework,omitempty"`   // what a compaction costs to read back, in the calls after it
	Long     float64 `json:"long,omitempty"`     // of the calls, the share that reached past the window advised
	Quick    float64 `json:"quick,omitempty"`    // of the gaps between calls, the share under 5 minutes
	Pause    float64 `json:"pause,omitempty"`    // between 5 minutes and an hour
	Away     float64 `json:"away,omitempty"`     // over an hour
	Hour     float64 `json:"hour,omitempty"`     // of the cache writes, the share kept an hour
	Sessions int     `json:"sessions,omitempty"` // streams that reached a compaction
	// Took is the median seconds a compaction kept the agent waiting, of
	// Timed compactions its files timed; 0 when fewer than minTimed were
	Took  int `json:"took,omitempty"`
	Timed int `json:"timed,omitempty"`
}

// Compaction is the window advised for one model of an agent.
type Compaction struct {
	Model   string  `json:"model"`
	Current int     `json:"current"` // the window it runs with
	Window  int     `json:"window"`  // the model's, which caps a setting
	Best    int     `json:"best"`
	Cost    float64 `json:"cost"` // with Current, the period's weighted tokens
	Low     float64 `json:"low"`  // with Best
	Curve   []Point `json:"curve"`
	Facts   Facts   `json:"facts"`
	// with compactions timed, the seconds they would have waited: with
	// Current, and with Best
	Wait     int `json:"wait,omitempty"`
	WaitBest int `json:"wait_best,omitempty"`
	// with compactions timed, the cheapest window and its wait, which
	// the advice passes over when it would wait longer than now
	Cheapest     int `json:"cheapest,omitempty"`
	WaitCheapest int `json:"wait_cheapest,omitempty"`
}

// CompactOpts says how an agent compacts: the windows it takes, and how
// far below the window it sets it really compacts.
type CompactOpts struct {
	Min, Max int
	Step     int
	// Reserve is what the agent keeps free under the window: Claude Code
	// compacts at the window less its output's room (167K of a 200K one,
	// 967K of 1M); Codex at the limit itself
	Reserve int
	// Current is the window it runs with now; 0 when nothing sets one,
	// and tune takes it from where its compactions were made
	Current int
	// Window is the model's whole window, the most a setting can give; 0
	// when it isn't known, and then nothing short of Max caps a setting
	// (zhemu on Discord: a 1M model magpie had no window for was offered
	// no more than the largest prompt it had been sent, 412K)
	Window int
	// Weigh prices the calls; nil weighs them at WeightsFor
	Weigh Weigh
}

const (
	// a drop to under this share of the prompt before is a compaction
	dropShare = 0.6
	// and only from a prompt at least this long
	dropFrom = 20000
	// the calls after a compaction its rework is counted over
	reworkCalls = 10
	// the compactions that must be timed before their time is weighed
	minTimed = 3
	// windows within this share of the cheapest cost the same: the least
	// that is worth changing a setting for (Compact keeps the current one
	// unless it saves this much), and so, with compactions timed, the band
	// the one that compacts least is taken from
	sameCost = 0.03
)

// took is the median seconds a compaction took of those the calls time,
// and how many that is; 0 when fewer than minTimed are.
func took(cs []Call) (int, int) {
	var ms []int
	for _, c := range cs {
		if c.Compact > 0 {
			ms = append(ms, int(c.Compact))
		}
	}
	if len(ms) < minTimed {
		return 0, len(ms)
	}
	return (median(ms) + 500) / 1000, len(ms)
}

// streams groups calls by the conversation they are in, each oldest first.
func streams(cs []Call) [][]Call {
	by := map[string][]Call{}
	for _, c := range cs {
		by[c.Stream] = append(by[c.Stream], c)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]Call, 0, len(keys))
	for _, k := range keys {
		s := by[k]
		slices.SortStableFunc(s, func(a, b Call) int { return a.Time.Compare(b.Time) })
		out = append(out, s)
	}
	return out
}

func compacted(prev, cur int) bool { return prev >= dropFrom && float64(cur) < dropShare*float64(prev) }

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

// observe reads, from an agent's own calls, how its conversations grow
// and what its compactions came to.
func observe(ss [][]Call) Facts {
	var f Facts
	var peaks, afters, growths, reworks []int
	for _, s := range ss {
		f.Streams++
		f.Calls += len(s)
		var ds []int
		cuts := []int{}
		for i := 1; i < len(s); i++ {
			if compacted(s[i-1].Prompt, s[i].Prompt) {
				peaks = append(peaks, s[i-1].Prompt)
				afters = append(afters, s[i].Prompt)
				cuts = append(cuts, i)
				continue
			}
			if d := s[i].Prompt - s[i-1].Prompt; d >= 0 {
				ds = append(ds, d)
			}
		}
		if len(cuts) > 0 {
			f.Sessions++
		}
		g := median(ds)
		growths = append(growths, ds...)
		// what the calls after a compaction read back beyond the stream's
		// usual growth: the files and results the summary left out
		for _, at := range cuts {
			end := at + reworkCalls
			if end >= len(s) {
				continue
			}
			extra := s[end].Prompt - s[at].Prompt - reworkCalls*g
			ok := true
			for j := at + 1; j <= end; j++ {
				if compacted(s[j-1].Prompt, s[j].Prompt) {
					ok = false
				}
			}
			if ok {
				reworks = append(reworks, max(0, extra))
			}
		}
	}
	f.Compacts = len(peaks)
	f.Peak, f.After, f.Growth, f.Rework = median(peaks), median(afters), median(growths), median(reworks)
	return f
}

// base is a stream's first prompt: what every call of it starts from
// (the system prompt, the tools), which a compaction keeps.
func base(ss [][]Call) int {
	var firsts []int
	for _, s := range ss {
		firsts = append(firsts, s[0].Prompt)
	}
	return median(firsts)
}

// replayCompact is what the streams would have cost compacting at
// threshold: each call's prompt grows by what it grew by, a compaction
// is made when it would pass the threshold, and the agent's own
// compactions in the files are left out, the prompt after them going on
// from where it was. A compaction reads the whole prompt, writes a summary
// (after less base, as output), the next call writes the summary to the
// cache, and the calls after it read back rework.
func replayCompact(ss [][]Call, threshold, after, b, rework int, w Weights, write float64) (float64, int, int) {
	cost, n, over := 0.0, 0, 0
	summary := max(0, after-b)
	for _, s := range ss {
		ctx := s[0].Prompt
		cost += float64(ctx)*write + float64(s[0].Out)*w.Out
		for i := 1; i < len(s); i++ {
			d := s[i].Prompt - s[i-1].Prompt
			if d < 0 || compacted(s[i-1].Prompt, s[i].Prompt) {
				d = 0
			}
			if ctx+d > threshold && ctx > after {
				cost += float64(ctx)*w.Read + float64(summary)*w.Out + float64(summary+rework)*write
				ctx = after + rework
				n++
			}
			if ctx+d > threshold {
				over++
			}
			cost += float64(ctx)*w.Read + float64(d)*write + float64(s[i].Out)*w.Out
			ctx += d
		}
	}
	return cost, n, over
}

// Compact advises the window to compact at for one model's calls of an
// agent, or nil when there is too little to go on: a few calls, or none
// that ever came near a window.
func Compact(cs []Call, o CompactOpts) *Compaction {
	ss := streams(cs)
	f := observe(ss)
	if f.Calls < 50 || f.Streams == 0 {
		return nil
	}
	model := cs[0].Model
	w := o.Weigh.of(model)
	write := w.Write
	var wrote, wrote1 int
	for _, c := range cs {
		wrote += c.Wrote
		wrote1 += c.Wrote1
	}
	if wrote > 0 {
		f.Hour = float64(wrote1) / float64(wrote)
		if f.Hour > 0.5 {
			write = w.Write1h
		}
	}
	b := base(ss)
	after := f.After
	if after == 0 {
		// no compaction seen: the summary Claude Code and Codex write is
		// some 10K over what the conversation started from
		after = b + 10000
	}
	rework := f.Rework
	if f.Compacts < 5 {
		rework = max(rework, 10000)
	}
	cur := o.Current
	if cur == 0 {
		switch {
		case f.Compacts >= 3:
			cur = roundTo(f.Peak+o.Reserve, o.Step)
		case o.Window > 0:
			cur = o.Window
		default:
			cur = o.Max
		}
	}
	// the agent compacts at the smaller of the setting and the model's
	// window: a 500K setting on a 200K model runs at 200K. A window
	// nobody knows caps nothing short of Max
	top := o.Max
	if o.Window > 0 {
		cur = min(cur, o.Window)
		top = min(o.Max, o.Window)
	}
	// a window that leaves little room over the summary compacts over and
	// over: it keeps at least half the summary's size, or twenty calls'
	// growth, free over it
	floor := max(o.Min, roundUp(after+max(after/2, 20*f.Growth)+o.Reserve, o.Step))
	f.Took, f.Timed = took(cs)
	if f.Took == 0 {
		f.Timed = 0
	}
	r := &Compaction{Model: model, Current: cur, Window: o.Window, Facts: f}
	run := func(v int) Point {
		c, n, _ := replayCompact(ss, max(after+1, v-o.Reserve), after, b, rework, w, write)
		return Point{Value: v, Cost: c, Compacts: n, Wait: n * f.Took}
	}
	now := run(cur)
	r.Cost = now.Cost
	for v := floor; v <= top; v += o.Step {
		r.Curve = append(r.Curve, run(v))
	}
	if len(r.Curve) == 0 {
		return nil
	}
	low := slices.MinFunc(r.Curve, func(a, b Point) int { return cmpF(a.Cost, b.Cost) })
	// of the windows within 1% of the cheapest, the largest: fewer
	// compactions lose less of what the conversation knew
	best := low
	for _, p := range r.Curve {
		if p.Cost <= low.Cost*1.01 && p.Value > best.Value {
			best = p
		}
	}
	// with the agent's compactions timed, the advice never makes the user
	// wait longer than the window now does: tokens and seconds share no
	// unit the files measure, so it doesn't trade one for the other on a
	// guessed rate. Of the windows that compact no more often than now,
	// the cheapest; of those within sameCost of it, the one that waits
	// least (the fewest compactions, the larger of two that make as
	// many). A cheaper window that waits longer stays on the curve with
	// its wait, for the reader to pick.
	if f.Took > 0 {
		var calm []Point
		for _, p := range r.Curve {
			if p.Compacts <= now.Compacts {
				calm = append(calm, p)
			}
		}
		best = now
		if len(calm) > 0 {
			cl := slices.MinFunc(calm, func(a, b Point) int { return cmpF(a.Cost, b.Cost) })
			best = cl
			for _, p := range calm {
				if p.Cost <= cl.Cost*(1+sameCost) && (p.Compacts < best.Compacts || p.Compacts == best.Compacts && p.Value > best.Value) {
					best = p
				}
			}
		}
		r.Cheapest, r.WaitCheapest = low.Value, low.Wait
	}
	r.Best, r.Low = best.Value, best.Cost
	r.Wait, r.WaitBest = now.Wait, best.Wait
	// it only says to change what would save 3% or more
	if r.Cost <= 0 || (r.Cost-r.Low)/r.Cost < sameCost {
		r.Best, r.Low, r.WaitBest = cur, r.Cost, r.Wait
	}
	_, _, over := replayCompact(ss, max(after+1, r.Best-o.Reserve), after, b, rework, w, write)
	f.Long = float64(over) / float64(max(1, f.Calls))
	r.Facts = f
	return r
}

func roundTo(v, step int) int {
	if step <= 0 {
		return v
	}
	return (v + step/2) / step * step
}

func roundUp(v, step int) int {
	if step <= 0 {
		return v
	}
	return (v + step - 1) / step * step
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TTL is the cache lifetime advised for one part of Claude Code's calls:
// its main conversation, or what runs beside it (subagents, workflows).
type TTL struct {
	Current string  `json:"current"` // 5m or 1h
	Best    string  `json:"best"`
	Cost    float64 `json:"cost"`
	Low     float64 `json:"low"`
	Short   float64 `json:"short"` // with 5m
	Hour    float64 `json:"hour"`  // with 1h
	Facts   Facts   `json:"facts"`
}

// replayTTL is what the streams would have cost with the cache kept for
// ttl, each stream at its own model's weights (an hour's writes at Write1h):
// a call within it of the one before reads what that one sent and writes
// what it adds; one after it, or after a compaction, writes its whole
// prompt again.
func replayTTL(ss [][]Call, ttl time.Duration, hour bool, weigh Weigh) float64 {
	cost := 0.0
	for _, s := range ss {
		w := weigh.of(s[0].Model)
		write := w.Write
		if hour {
			write = w.Write1h
		}
		cost += float64(s[0].Prompt) * write
		for i := 1; i < len(s); i++ {
			p, c := s[i-1].Prompt, s[i].Prompt
			switch {
			case s[i].Time.Sub(s[i-1].Time) > ttl, compacted(p, c):
				cost += float64(c) * write
			case c >= p:
				cost += float64(p)*w.Read + float64(c-p)*write
			default:
				cost += float64(c) * w.Read
			}
		}
	}
	return cost
}

// CacheTTL advises Claude Code's cache lifetime for calls, all of the
// main conversation or all beside it; nil when there is too little to go
// on. current is the one set, "" when nothing sets one, and tune takes
// it from what the calls' cache writes were kept for. weigh prices the
// calls; nil weighs them at WeightsFor.
func CacheTTL(cs []Call, current string, weigh Weigh) *TTL {
	ss := streams(cs)
	f := observe(ss)
	if f.Calls < 50 {
		return nil
	}
	var wrote, wrote1, gaps, quick, pause int
	for _, s := range ss {
		for i, c := range s {
			wrote += c.Wrote
			wrote1 += c.Wrote1
			if i == 0 {
				continue
			}
			gaps++
			switch g := c.Time.Sub(s[i-1].Time); {
			case g <= 5*time.Minute:
				quick++
			case g <= time.Hour:
				pause++
			}
		}
	}
	if gaps > 0 {
		f.Quick = float64(quick) / float64(gaps)
		f.Pause = float64(pause) / float64(gaps)
		f.Away = 1 - f.Quick - f.Pause
	}
	if wrote > 0 {
		f.Hour = float64(wrote1) / float64(wrote)
	}
	if current == "" {
		current = "5m"
		if f.Hour > 0.5 {
			current = "1h"
		}
	}
	r := &TTL{Current: current, Facts: f}
	r.Short = replayTTL(ss, 5*time.Minute, false, weigh)
	r.Hour = replayTTL(ss, time.Hour, true, weigh)
	r.Cost, r.Best, r.Low = r.Short, "5m", r.Short
	if current == "1h" {
		r.Cost = r.Hour
	}
	if r.Hour < r.Short {
		r.Best, r.Low = "1h", r.Hour
	}
	if r.Cost <= 0 || (r.Cost-r.Low)/r.Cost < 0.03 {
		r.Best, r.Low = current, r.Cost
	}
	return r
}
