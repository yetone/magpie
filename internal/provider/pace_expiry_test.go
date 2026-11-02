package provider

import (
	"math"
	"testing"
	"time"
)

// Pace takes credits expiring before a budget window renews for its
// deadline when they make the more per hour (lcyksp on Discord): the share
// that expires (RunsOutLeft of Of, at most all), else all that is left,
// per hour until they expire, never fewer than one.
func TestPaceCreditsExpiring(t *testing.T) {
	now := time.Now()
	month := now.Add(28 * 24 * time.Hour)
	l := Limit{Used: 20, Of: 500, Resets: month}
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

	if got, due := (Allowance{l}).Pace("m", now); !near(got, 80/(28*24.0)) || !due.Equal(month) {
		t.Fatalf("nothing expiring: %v by %v", got, due)
	}
	l.RunsOut, l.RunsOutLeft = now.Add(10*time.Hour), 50 // 10% in 10h
	if got, due := (Allowance{l}).Pace("m", now); !near(got, 1) || !due.Equal(l.RunsOut) {
		t.Fatalf("expiring: %v by %v", got, due)
	}
	l.RunsOutLeft = 0 // not told: the 80% left
	if got, _ := (Allowance{l}).Pace("m", now); !near(got, 8) {
		t.Fatalf("amount not told: %v", got)
	}
	l.RunsOutLeft = 5000 // more than the window: all of it
	if got, _ := (Allowance{l}).Pace("m", now); !near(got, 10) {
		t.Fatalf("capped: %v", got)
	}
	l.RunsOut, l.RunsOutLeft = now.Add(10*time.Minute), 50 // an hour at least
	if got, _ := (Allowance{l}).Pace("m", now); !near(got, 10) {
		t.Fatalf("within the hour: %v", got)
	}
	l.RunsOut, l.RunsOutLeft = now.Add(14*time.Hour), 0.5 // too little to matter
	if got, due := (Allowance{l}).Pace("m", now); !near(got, 80/(28*24.0)) || !due.Equal(month) {
		t.Fatalf("a little expiring: %v by %v", got, due)
	}
	l.RunsOut = month.Add(time.Hour) // after the renewal
	if got, due := (Allowance{l}).Pace("m", now); !near(got, 80/(28*24.0)) || !due.Equal(month) {
		t.Fatalf("after the renewal: %v by %v", got, due)
	}
}

// allowanceOf carries how much expires with when (QuotaWindow.RunsOut).
func TestAllowanceCarriesRunsOutLeft(t *testing.T) {
	at := time.Now().Add(14 * time.Hour)
	a := allowanceOf([]QuotaWindow{{Name: "Credits", Used: 0, Limit: 500, RunsOutAt: &at, RunsOut: 70.85}}, time.Now())
	if len(a) != 1 || !a[0].RunsOut.Equal(at) || a[0].RunsOutLeft != 70.85 {
		t.Fatalf("allowance %+v", a)
	}
}
