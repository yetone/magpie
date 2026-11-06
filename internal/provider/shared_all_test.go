package provider

import (
	"fmt"
	"testing"
)

// Find under a hold copies the provider it gives, and no other: it copied
// every provider, every account's keys and headers, to give one. What it
// gives is still the caller's to change.
func TestFindCopiesTheOneItGives(t *testing.T) {
	isolate(t)
	for i := range 40 {
		p := Provider{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("P%d", i), Key: "k", Chat: "http://127.0.0.1:1/v1",
			Models: []string{"m1", "m2", "m3"}, Keys: []KeyAccount{{Key: "k1"}, {Key: "k2"}}, Headers: map[string]string{"X-A": "1", "X-B": "2"}}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	defer Hold()()
	p, err := Find("p7")
	if err != nil || p.ID != "p7" {
		t.Fatalf("Find: %v %v", p, err)
	}
	p.Keys[0].Key, p.Models[0], p.Headers["X-A"] = "changed", "changed", "changed"
	again, _ := Find("p7")
	if again.Keys[0].Key != "k1" || again.Models[0] != "m1" || again.Headers["X-A"] != "1" {
		t.Fatalf("a change to what Find gave reached the next: %v %v %v", again.Keys, again.Models, again.Headers)
	}
	for _, q := range All() {
		if q.ID == "p7" && q.Keys[0].Key != "k1" {
			t.Fatalf("a change to what Find gave reached All: %v", q.Keys)
		}
	}
	n := leastAllocs(20, func() { Find("p7") })
	if n > 60 {
		t.Fatalf("Find of one of 40 providers made %v allocations, want at most 60", n)
	}
}

// FoundOnce walks each group once, however often an agent's list asks.
func TestFoundOnceAsksEachGroupOnce(t *testing.T) {
	asked := map[string]int{}
	find := FoundOnce(func(id string) (Group, []Member, bool) {
		asked[id]++
		return Group{ID: id}, []Member{{ID: id + "-m"}}, id != "group:none"
	})
	for range 5 {
		for _, id := range []string{"group:a", "group:b", "group:none"} {
			g, ms, ok := find(id)
			if g.ID != id || len(ms) != 1 || ms[0].ID != id+"-m" || ok != (id != "group:none") {
				t.Fatalf("%s: %v %v %v", id, g, ms, ok)
			}
		}
	}
	for id, n := range asked {
		if n != 1 {
			t.Fatalf("%s walked %d times, want once", id, n)
		}
	}
}
