package tune

import (
	"fmt"
	"testing"
	"time"
)

// session makes one conversation: it starts at start tokens, grows by grow
// a call, and compacts down to after when it reaches at, gap apart.
func session(id string, t0 time.Time, n, start, grow, at, after int, gap time.Duration, wrote1 bool) []Call {
	var out []Call
	p := start
	for i := 0; i < n; i++ {
		c := Call{Time: t0.Add(time.Duration(i) * gap), Stream: id, Model: "claude-opus-5-5", Prompt: p, Out: 800, Wrote: grow}
		if wrote1 {
			c.Wrote1 = c.Wrote
		}
		out = append(out, c)
		p += grow
		if at > 0 && p >= at {
			p = after
		}
	}
	return out
}

func many(k int, f func(id string, t0 time.Time) []Call) []Call {
	var out []Call
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < k; i++ {
		out = append(out, f(fmt.Sprint("s", i), t0.Add(time.Duration(i)*24*time.Hour))...)
	}
	return out
}

var claudeOpts = CompactOpts{Min: 100000, Max: 1000000, Step: 10000, Reserve: 33000}

// Long conversations on the whole 1M window carry a huge prompt for every
// call: a smaller window reads far less.
func TestCompactLongSessionsGoSmaller(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 600, 30000, 5000, 967000, 60000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	if r.Best >= 1000000 || (r.Cost-r.Low)/r.Cost < 0.2 {
		t.Fatalf("best %d saves %.0f of %.0f: want a smaller window saving a fifth or more", r.Best, r.Cost-r.Low, r.Cost)
	}
	if r.Facts.Compacts == 0 || r.Facts.Growth != 5000 {
		t.Fatalf("facts %+v", r.Facts)
	}
}

// Conversations that never come near the window: no window changes what
// they cost, so it stays as the user has it.
func TestCompactShortSessionsKeepCurrent(t *testing.T) {
	cs := many(5, func(id string, t0 time.Time) []Call {
		return session(id, t0, 30, 20000, 2000, 0, 0, time.Minute, false)
	})
	o := claudeOpts
	o.Window = 200000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	if r.Best != r.Current || r.Current != 200000 || r.Low != r.Cost {
		t.Fatalf("got %d → %d (%.0f → %.0f), want 200000 kept", r.Current, r.Best, r.Cost, r.Low)
	}
}

// Too few calls to go on: no advice at all.
func TestCompactNeedsCalls(t *testing.T) {
	cs := session("s", time.Now(), 20, 20000, 2000, 0, 0, time.Minute, false)
	if r := Compact(cs, claudeOpts); r != nil {
		t.Fatalf("advice from 20 calls: %+v", r)
	}
}

// A user who comes back after 20 minutes each time: a 5-minute cache is
// written again on every call, an hour's is read.
func TestCacheTTLPausesWantAnHour(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 40, 30000, 3000, 0, 0, 20*time.Minute, false)
	})
	r := CacheTTL(cs, "", nil)
	if r == nil || r.Current != "5m" || r.Best != "1h" || r.Low >= r.Cost {
		t.Fatalf("got %+v, want 5m → 1h", r)
	}
	if r.Facts.Pause < 0.99 {
		t.Fatalf("pause share %.2f", r.Facts.Pause)
	}
}

// A user whose calls all come within seconds pays the hour's dearer writes
// for nothing.
func TestCacheTTLQuickWantsFiveMinutes(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 40, 30000, 3000, 0, 0, 20*time.Second, true)
	})
	r := CacheTTL(cs, "", nil)
	if r == nil || r.Current != "1h" || r.Best != "5m" {
		t.Fatalf("got %+v, want 1h (read from the writes) → 5m", r)
	}
}

// A setting over the model's window runs at the window: Claude Code takes
// the smaller of the two, so that is what the advice starts from.
func TestCompactSettingCappedByWindow(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 400, 30000, 1000, 167000, 57000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 200000, 500000
	r := Compact(cs, o)
	if r == nil || r.Current != 200000 || r.Window != 200000 {
		t.Fatalf("got %+v, want current 200000", r)
	}
	if last := r.Curve[len(r.Curve)-1].Value; last > 200000 {
		t.Fatalf("curve reaches %d, past the model's window", last)
	}
}

// Each window tried says how many compactions it would have made (#1860):
// fewer as the window grows, none once it is over every conversation, and
// at the window the agent ran with, about as many as its files show.
func TestCompactCurveCountsCompactions(t *testing.T) {
	cs := many(3, func(id string, t0 time.Time) []Call {
		return session(id, t0, 600, 30000, 5000, 967000, 60000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	first, last := r.Curve[0], r.Curve[len(r.Curve)-1]
	if first.Compacts <= last.Compacts || last.Compacts == 0 {
		t.Fatalf("%d compactions at %d, %d at %d: want more at the smaller window, some at the largest", first.Compacts, first.Value, last.Compacts, last.Value)
	}
	for i := 1; i < len(r.Curve); i++ {
		if r.Curve[i].Compacts > r.Curve[i-1].Compacts {
			t.Fatalf("%d compactions at %d, more than %d at %d", r.Curve[i].Compacts, r.Curve[i].Value, r.Curve[i-1].Compacts, r.Curve[i-1].Value)
		}
	}
	if d := last.Compacts - r.Facts.Compacts; d < -3 || d > 3 {
		t.Fatalf("%d compactions replayed at %d, %d seen in the files", last.Compacts, last.Value, r.Facts.Compacts)
	}
}

// timed marks the first ms compactions of cs (the call after each drop) as
// having kept the agent waiting ms milliseconds, as its files would say.
func timed(cs []Call, n int, ms int64) []Call {
	out := append([]Call(nil), cs...)
	for i := 1; i < len(out) && n > 0; i++ {
		if out[i].Stream == out[i-1].Stream && compacted(out[i-1].Prompt, out[i].Prompt) {
			out[i].Compact = ms
			n--
		}
	}
	return out
}

// With the agent's compactions timed, the advice never waits longer than
// the window now does: of the windows that compact no more often, the
// cheapest, and of those within 3% of it the fewest compactions. The
// cheaper window untimed advice takes, which compacts more often, is said
// with its wait instead. Each window says the seconds its compactions
// would have waited.
func TestCompactTimedWaitsLeast(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 400, 30000, 4000, 967000, 60000, 30*time.Second, false)
	})
	for _, cur := range []int{1000000, 600000, 150000} {
		o := claudeOpts
		o.Window, o.Current = 1000000, cur
		plain := Compact(cs, o)
		r := Compact(timed(cs, 4, 61400), o)
		if plain == nil || r == nil {
			t.Fatal("no advice")
		}
		if r.Facts.Took != 61 || r.Facts.Timed != 4 {
			t.Fatalf("took %ds of %d timed, want 61s of 4", r.Facts.Took, r.Facts.Timed)
		}
		nowN := r.Wait / 61 // the window now may sit under the curve
		low := r.Curve[0]
		for _, p := range r.Curve {
			if p.Cost < low.Cost {
				low = p
			}
		}
		var best Point
		for _, p := range r.Curve {
			if p.Wait != p.Compacts*61 {
				t.Fatalf("%d waits %ds for %d compactions", p.Value, p.Wait, p.Compacts)
			}
			if p.Value == r.Best {
				best = p
			}
		}
		if best.Compacts > nowN {
			t.Fatalf("at %d: advised %d makes %d compactions, more than %d now", cur, r.Best, best.Compacts, nowN)
		}
		for _, p := range r.Curve {
			if p.Compacts <= nowN && p.Cost*(1+sameCost) < best.Cost {
				t.Fatalf("at %d: %d waits no longer than now and costs %.0f, over 3%% under %.0f at %d", cur, p.Value, p.Cost, best.Cost, r.Best)
			}
		}
		if r.Cheapest != low.Value || r.WaitCheapest != low.Wait {
			t.Fatalf("at %d: cheapest said %d (%ds), want %d (%ds)", cur, r.Cheapest, r.WaitCheapest, low.Value, low.Wait)
		}
		if r.WaitBest != best.Wait {
			t.Fatalf("at %d: wait %ds now, %ds advised", cur, r.Wait, r.WaitBest)
		}
		t.Logf("at %d (%d compactions): untimed %d (%d), timed %d (%d), cheapest %d (%d)", cur, nowN, plain.Best, compactsAt(plain, plain.Best), r.Best, best.Compacts, low.Value, low.Compacts)
	}
	// from 1M, untimed advice goes to 260000 and compacts far more often;
	// timed, it stays where the wait doesn't grow
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	plain, r := Compact(cs, o), Compact(timed(cs, 4, 61400), o)
	if compactsAt(plain, plain.Best) <= compactsAt(r, 1000000) {
		t.Fatalf("fixture: untimed %d makes %d compactions, no more than %d now", plain.Best, compactsAt(plain, plain.Best), compactsAt(r, 1000000))
	}
	if r.Best == plain.Best {
		t.Fatalf("timed advice %d is untimed's, which waits longer than now", r.Best)
	}
}

func compactsAt(r *Compaction, v int) int {
	for _, p := range r.Curve {
		if p.Value == v {
			return p.Compacts
		}
	}
	return -1
}

// Too few compactions timed, or none, and the advice is what it was without
// their time: the same window, no wait said.
func TestCompactUntimedUnchanged(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 400, 30000, 4000, 967000, 60000, 30*time.Second, false)
	})
	o := claudeOpts
	o.Window, o.Current = 1000000, 1000000
	plain := Compact(cs, o)
	few := Compact(timed(cs, 2, 61400), o)
	if plain == nil || few == nil {
		t.Fatal("no advice")
	}
	if plain.Best != 260000 || few.Best != plain.Best {
		t.Fatalf("best %d untimed, %d with 2 timed; want 260000 both", plain.Best, few.Best)
	}
	if few.Facts.Took != 0 || few.Facts.Timed != 0 || few.Wait != 0 || few.WaitBest != 0 || few.Cheapest != 0 || few.WaitCheapest != 0 {
		t.Fatalf("wait said from 2 timed: %+v", few)
	}
	for _, p := range few.Curve {
		if p.Wait != 0 {
			t.Fatalf("%d waits %ds", p.Value, p.Wait)
		}
	}
}

// A model whose window isn't known (Window 0) caps nothing short of Max:
// the windows tried don't stop at the largest prompt seen (zhemu on
// Discord: a 1M model offered no more than 412K), and with no setting and
// no compaction seen it runs now at Max.
func TestCompactUnknownWindowCapsNothing(t *testing.T) {
	cs := many(2, func(id string, t0 time.Time) []Call {
		return session(id, t0, 80, 20000, 5000, 0, 0, 20*time.Second, false)
	})
	o := CompactOpts{Min: 50000, Max: 1000000, Step: 10000}
	r := Compact(cs, o)
	if r == nil {
		t.Fatal("no advice")
	}
	if last := r.Curve[len(r.Curve)-1].Value; last != 1000000 || r.Current != 1000000 || r.Window != 0 {
		t.Fatalf("curve to %d, now %d, window %d; want 1000000, 1000000, 0", last, r.Current, r.Window)
	}
}

// Calls are weighed at what Weigh says their model costs: on a provider
// that bills an hour's cache write as a 5-minute one (Copilot, #3435) the
// hour costs no more, so calls seconds apart are left on it, where at the
// maker's weights (nil) they are moved to 5 minutes.
func TestCacheTTLWeighedAtTheProvidersPrice(t *testing.T) {
	cs := many(4, func(id string, t0 time.Time) []Call {
		return session(id, t0, 40, 30000, 3000, 0, 0, 20*time.Second, true)
	})
	if r := CacheTTL(cs, "", nil); r == nil || r.Best != "5m" {
		t.Fatalf("at the maker's weights got %+v, want 1h → 5m", r)
	}
	copilot := func(string) Weights { return Weights{Read: 0.05, Write: 1.25, Write1h: 1.25, Out: 5} }
	r := CacheTTL(cs, "", copilot)
	if r == nil || r.Current != "1h" || r.Best != "1h" || r.Hour > r.Short {
		t.Fatalf("at an hour priced as 5 minutes got %+v, want it left on 1h", r)
	}
}
