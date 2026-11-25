package routes

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// guhailin3 on X: Codex accounts used one by one, each until it is used
// up, and the next picked by the quota a reset would waste soonest — its
// week left per hour until it renews (Seat.Pace). Use before expiry
// alone goes back and forth as the paces cross; Until used up alone goes
// on in the listed order.
func TestPaceDrainPicksByPaceAndStays(t *testing.T) {
	Forget()
	t.Cleanup(func() { Forget() })
	const pool = "codex"
	order := func(seats ...Seat) []int { return Apply(PaceDrain, pool, seats) }
	seat := func(key string, used, pace float64) Seat {
		return Seat{Key: key, Account: true, Known: true, Used: used, Pace: pace}
	}
	a, b, c := seat("a", 40, 2), seat("b", 20, 5), seat("c", 70, 1)

	// picked by pace: b, then the others by pace for failover
	if got := order(a, b, c); !slices.Equal(got, []int{1, 0, 2}) {
		t.Fatalf("fresh: %v, want b (most left per hour) first", got)
	}
	// b spent on: its pace falls below a's, and the pool stays on it
	bLater := seat("b", 85, 1.5)
	if got := order(a, bLater, c); !slices.Equal(got, []int{1, 0, 2}) {
		t.Fatalf("b's pace below a's: %v, want b kept first", got)
	}
	if got := Apply(Pace, pool, []Seat{a, bLater, c}); got[0] != 0 {
		t.Fatalf("Use before expiry alone: %v, want it moved to a (the contrast)", got)
	}
	// asked again with nothing changed (a preview): the same
	if got := order(a, bLater, c); !slices.Equal(got, []int{1, 0, 2}) {
		t.Fatalf("asked again: %v", got)
	}
	// b at 98% is not used up yet (In order's and Until used up's line)
	if got := order(a, seat("b", provider.SpentShare, 0.1), c); got[0] != 1 {
		t.Fatalf("b at %v%%: %v, want b kept first", provider.SpentShare, got)
	}
	// b used up: picked again by pace, a; Until used up would go on to c,
	// the next listed
	bSpent := seat("b", 100, 0)
	if got := order(a, bSpent, c); !slices.Equal(got, []int{0, 2, 1}) {
		t.Fatalf("b used up: %v, want a first", got)
	}
	// b renews with the most left per hour: the pool stays on a
	if got := order(a, seat("b", 0, 9), c); got[0] != 0 {
		t.Fatalf("b renewed: %v, want a kept first", got)
	}
	// a refused for its quota (Drained) and renewed before the next
	// request: picked again, b now
	Drained("a")
	if got := order(seat("a", 0, 8), seat("b", 0, 9), c); !slices.Equal(got, []int{1, 0, 2}) {
		t.Fatalf("a drained: %v, want b first", got)
	}
}

// A seat that failed or was rate limited (resting) is left for the next by
// pace, and the pool stays there once the rest is over. One out of quota
// (Spent) is left as in Until used up.
func TestPaceDrainLeavesAFailure(t *testing.T) {
	Forget()
	t.Cleanup(func() { Forget() })
	const pool = "group/codex"
	a, b, c := Seat{Key: "a", Known: true, Pace: 3}, Seat{Key: "b", Known: true, Pace: 2}, Seat{Key: "c", Known: true, Pace: 1}
	if got := Apply(PaceDrain, pool, []Seat{a, b, c}); got[0] != 0 {
		t.Fatalf("fresh: %v", got)
	}
	failed := a
	failed.Resting = true
	if got := Apply(PaceDrain, pool, []Seat{failed, b, c}); got[0] != 1 {
		t.Fatalf("a failed: %v, want b first", got)
	}
	if got := Apply(PaceDrain, pool, []Seat{a, b, c}); got[0] != 1 {
		t.Fatalf("a back: %v, want b kept first", got)
	}
	out := b
	out.Resting, out.Spent = true, true
	if got := Apply(PaceDrain, pool, []Seat{a, out, c}); got[0] != 0 {
		t.Fatalf("b out of quota: %v, want a first", got)
	}
	// every one resting: the first by pace not used up, all used up: kept
	rest := func(s Seat) Seat { s.Resting = true; return s }
	used := func(s Seat) Seat { s.Used = 100; return s }
	if got := Apply(PaceDrain, pool, []Seat{used(a), rest(b), rest(c)}); got[0] != 1 {
		t.Fatalf("a used up, the rest resting: %v, want b", got)
	}
	if got := Apply(PaceDrain, pool, []Seat{used(a), used(b), used(c)}); got[0] != 1 {
		t.Fatalf("all used up: %v, want b kept", got)
	}
	// b gone from the pool: picked again
	if got := Apply(PaceDrain, pool, []Seat{a, c}); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("b gone: %v", got)
	}
}

// An account not known that tells what it has left as it answers goes
// first once, as in Use before expiry, but the pool stays on its pick.
func TestPaceDrainLearnsWithoutKeeping(t *testing.T) {
	Forget()
	t.Cleanup(func() { Forget() })
	const pool = "codex"
	a, b := Seat{Key: "a", Known: true, Pace: 2}, Seat{Key: "b", Known: true, Pace: 1}
	learner := Seat{Key: "n", Learns: true, Pace: provider.FreshPace}
	if got := Apply(PaceDrain, pool, []Seat{a, b, learner}); !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatalf("with one learning: %v, want it, then a", got)
	}
	// known now, with less left per hour than a: a stays
	known := Seat{Key: "n", Known: true, Pace: 0.5}
	if got := Apply(PaceDrain, pool, []Seat{a, b, known}); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("learnt: %v, want a kept", got)
	}
	// only ones learning: one of them is kept
	l2 := Seat{Key: "m", Learns: true, Pace: provider.FreshPace}
	Forget(pool)
	if got := Apply(PaceDrain, pool, []Seat{learner, l2}); got[0] != 0 {
		t.Fatalf("all learning: %v", got)
	}
	if got := Apply(PaceDrain, pool, []Seat{learner, l2}); got[0] != 0 {
		t.Fatalf("all learning, again: %v", got)
	}
}

func TestPaceDrainIsBuiltin(t *testing.T) {
	s, ok := Builtin[provider.PaceDrain]
	if !ok || !s.Exact || !s.ByUse {
		t.Fatalf("Builtin[%q] = %+v, %v; want an exact strategy that weighs use", provider.PaceDrain, s, ok)
	}
	if !provider.GroupRouting(provider.PaceDrain) || provider.SpentShareOf(provider.PaceDrain) != 100 {
		t.Fatal("pace-drain is a group's routing and counts an account used up at 100%")
	}
}
