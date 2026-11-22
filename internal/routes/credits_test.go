package routes

import (
	"slices"
	"testing"
	"time"
)

// A plugin's account gone on past its quota on credits it holds (a Kiro
// month spent with a trial left: Seat.Credits) has some left, as a
// ChatGPT account let spend its credits has: it isn't all but used up.
// Smart, Least used, Pace and Free quota by expiry keep it with those that
// have room, before those running low, and Until used up doesn't pass over
// it. Before, each counted it spent, so a group of Kiro's and Claude's left
// Kiro out until every Claude account was used up, and Until used up
// skipped it outright.
func TestCreditsHasSomeLeft(t *testing.T) {
	now := time.Now()
	seats := func() []Seat {
		return []Seat{
			{Key: "spent", Known: true, Used: 100},
			{Key: "credits", Known: true, Used: 100, Credits: true, Trial: true, Soon: []time.Time{now.Add(9 * 24 * time.Hour)}},
			{Key: "low", Known: true, Used: 95},
			{Key: "room", Known: true, Used: 40, Soon: []time.Time{now.Add(48 * time.Hour)}},
		}
	}
	want := []string{"room", "credits", "low", "spent"}
	for _, c := range []struct {
		name string
		st   Strategy
	}{{"Smart", Smart}, {"least used", LeastUsed}, {"free quota by expiry", FreeExpiring}} {
		if got := keys(seats(), Apply(c.st, "p", seats())); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", c.name, got, want)
		}
	}
}

// Until used up doesn't pass over an account on its credits for one with
// nothing left: the credits answer.
func TestDrainDoesNotPassOverCredits(t *testing.T) {
	drains.Lock()
	drains.m = map[string]drainAt{}
	drains.Unlock()
	s := []Seat{
		{Key: "spent", Known: true, Used: 100},
		{Key: "credits", Known: true, Used: 100, Credits: true, Trial: true},
	}
	if got := keys(s, Apply(Drain, "p", s)); got[0] != "credits" {
		t.Fatalf("Drain: %v, want credits first", got)
	}
}
