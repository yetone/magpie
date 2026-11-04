package routes

import (
	"slices"
	"testing"
	"time"
)

// wowbbs on Discord: a group of check-in accounts, each given free quota
// that lapses on its own day, and a paid one. Free quota by expiry spends
// the free quota lost soonest first, goes on to the next free one as one
// is used up, and the paid one only when no free one has quota left.
func TestFreeExpiringSpendsSoonestFreeFirst(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }
	cases := []struct {
		name  string
		seats []Seat
		want  []int
	}{
		{
			// the paid one listed first, renewing soonest of all (Smart
			// would put it first): it waits behind every free one, and the
			// free go by when theirs is lost — a lapse as a renewal
			name: "free by expiry, paid last",
			seats: []Seat{
				{Key: "paid", Known: true, Used: 10, Soon: []time.Time{at(1)}},
				{Key: "workbuddy", Known: true, FreePlan: true, Used: 40, Soon: []time.Time{at(72)}},
				{Key: "minimax", Known: true, FreePlan: true, Used: 5, Lapse: at(20)},
				{Key: "trae", Known: true, Free: true, Used: 70, Soon: []time.Time{at(300), at(48)}},
			},
			want: []int{2, 3, 1, 0},
		},
		{
			// the soonest free one used up: the next free one, not the paid
			name: "used up goes last",
			seats: []Seat{
				{Key: "paid", Known: true, Used: 10},
				{Key: "a", Known: true, FreePlan: true, Used: 100, Soon: []time.Time{at(2)}},
				{Key: "b", FreePlan: true, Spent: true, Soon: []time.Time{at(3)}},
				{Key: "c", Known: true, FreePlan: true, Used: 50, Soon: []time.Time{at(30)}},
			},
			want: []int{3, 0, 2, 1},
		},
		{
			// a free one that tells no expiry after those that do; those
			// expiring in the same hour keep their listed order
			name: "unknown expiry and ties",
			seats: []Seat{
				{Key: "x", FreePlan: true},
				{Key: "y", Known: true, FreePlan: true, Soon: []time.Time{at(5).Add(10 * time.Minute)}},
				{Key: "z", Known: true, FreePlan: true, Soon: []time.Time{at(5)}},
			},
			want: []int{1, 2, 0},
		},
		{
			// no free one: the paid as Smart orders them
			name: "none free is Smart",
			seats: []Seat{
				{Key: "late", Known: true, Used: 10, Soon: []time.Time{at(100)}},
				{Key: "soon", Known: true, Used: 10, Soon: []time.Time{at(10)}},
				{Key: "low", Known: true, Used: 95, Soon: []time.Time{at(1)}},
			},
			want: []int{1, 0, 2},
		},
	}
	for _, c := range cases {
		if got := Apply(FreeExpiring, "group/x", c.seats); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExpiresIsTheSoonest(t *testing.T) {
	a, b, c := time.Unix(300, 0), time.Unix(100, 0), time.Unix(200, 0)
	if got := Expires(Seat{Soon: []time.Time{a, b}, Lapse: c}); !got.Equal(b) {
		t.Errorf("renewals and lapse: %v, want %v", got, b)
	}
	if got := Expires(Seat{Lapse: c}); !got.Equal(c) {
		t.Errorf("lapse alone: %v", got)
	}
	if got := Expires(Seat{}); !got.IsZero() {
		t.Errorf("nothing told: %v", got)
	}
}
