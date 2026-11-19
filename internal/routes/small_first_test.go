package routes

import (
	"slices"
	"testing"
	"time"
)

// Small context first (yg6888 on X) puts the seat whose model's context
// window is smallest first, so a request that fits it leaves the big ones
// for those only they hold (the gateway has left out every seat the
// request is longer than, roomy). A window not known goes after every
// known one; seats of one window go as Smart orders them (the sooner
// renewal first); one all but used up still goes after those with room,
// whatever its window.
func TestSmallFirstOrdersByWindow(t *testing.T) {
	soon, later := time.Now().Add(2*time.Hour), time.Now().Add(72*time.Hour)
	seats := []Seat{
		{Key: "1m", Known: true, Window: 1_000_000},
		{Key: "unknown", Known: true},
		{Key: "256k-later", Known: true, Window: 256_000, Soon: []time.Time{later}},
		{Key: "200k-spent", Known: true, Window: 100_000, Used: 100},
		{Key: "256k-soon", Known: true, Window: 256_000, Soon: []time.Time{soon}},
		{Key: "200k", Known: true, Window: 200_000},
	}
	var got []string
	for _, i := range Apply(SmallFirst, "g", seats) {
		got = append(got, seats[i].Key)
	}
	want := []string{"200k", "256k-soon", "256k-later", "1m", "unknown", "200k-spent"}
	if !slices.Equal(got, want) {
		t.Fatalf("Small context first ordered %v, want %v", got, want)
	}
	if !SmallFirst.ByWindow || Builtin["fit"].Order == nil {
		t.Fatal("Small context first doesn't weigh windows or isn't registered as fit")
	}
}
