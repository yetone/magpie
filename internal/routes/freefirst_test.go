package routes

import (
	"slices"
	"testing"
)

// FreeFirst spends what costs nothing before what spends a subscription's
// allowance: the free seats in their listed order, then the paid ones in
// theirs — nothing free left behind a paid one, and failover inside a tier
// staying in the order the user set.
func TestFreeFirstPutsFreeAheadOfPaid(t *testing.T) {
	cases := []struct {
		name  string
		seats []Seat
		want  []int
	}{
		{
			// b and d cost nothing: they go first, each where it stood
			name:  "free among paid",
			seats: []Seat{{Key: "a"}, {Key: "b", Free: true}, {Key: "c"}, {Key: "d", Free: true}},
			want:  []int{1, 3, 0, 2},
		},
		{
			// every seat free: their order alone
			name:  "all free",
			seats: []Seat{{Key: "a", Free: true}, {Key: "b", Free: true}},
			want:  []int{0, 1},
		},
		{
			// none free: In order, so a routing set on a pool without free
			// models changes nothing
			name:  "none free",
			seats: []Seat{{Key: "a"}, {Key: "b"}, {Key: "c"}},
			want:  []int{0, 1, 2},
		},
		{
			name:  "the last alone free",
			seats: []Seat{{Key: "a"}, {Key: "b"}, {Key: "c", Free: true}},
			want:  []int{2, 0, 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Apply(FreeFirst, "group/g", c.seats); !slices.Equal(got, c.want) {
				t.Fatalf("order = %v, want %v", got, c.want)
			}
		})
	}
}

// FreeFirst asks the gateway for Free (Strategy.ByFree), since nothing else
// fills it; a routing that doesn't is not to pay for reading each model
// list. Free quota by expiry ("expiring") counts a model that costs
// nothing as free quota too, so it is the other that asks.
func TestFreeFirstIsByFree(t *testing.T) {
	if !FreeFirst.ByFree || !FreeExpiring.ByFree {
		t.Fatal("FreeFirst or FreeExpiring doesn't ask the gateway for Free")
	}
	for name, s := range Builtin {
		if name == "free" || name == "expiring" {
			continue
		}
		if s.ByFree {
			t.Fatalf("%s weighs by Free too, and would read every model list", name)
		}
	}
}

// The routing a provider or group is saved with is the one routes.Find
// knows: "free" is in Builtin, so plugins/rotation serves it and a saved
// one isn't read as an unknown routing (tried in its listed order).
func TestFreeFirstIsABuiltin(t *testing.T) {
	s, ok := Builtin["free"]
	if !ok {
		t.Fatal("\"free\" is not among Builtin")
	}
	if s.Order == nil {
		t.Fatal("\"free\" has no order")
	}
}
