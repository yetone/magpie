package provider

import (
	"context"
	"testing"
	"time"
)

// A model's test can be pinned to one of a provider's keys (tmr11235 on
// X), and its result names the key that was asked: without the pick,
// routing sends m to the first key that serves it and the others are
// never tried.
func TestModelTestPinnedToAKey(t *testing.T) {
	probeHome(t)
	srv, asked := askingRelay(t, "sk-b", "m")
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1",
		Key: "sk-a", KeyName: "first", Keys: []KeyAccount{{Name: "second", Key: "sk-b"}}, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	picks := p.TestPicks()
	if len(picks) != 2 || picks[0].Name != "first" || picks[1].Name != "second" || picks[1].ID != keyID("sk-b") {
		t.Fatalf("the picks: %+v", picks)
	}
	// the key asked: sk-b answers m, sk-a is turned away, and each result says who was asked
	rs := p.TestModels(TestOn(context.Background(), keyID("sk-b")), []string{"m"})
	if len(rs) != 1 || !rs[0].OK || rs[0].Account != "second" {
		t.Fatalf("pinned to the second key: %+v (asked %v)", rs, asked())
	}
	rs = p.TestModels(TestOn(context.Background(), keyID("sk-a")), []string{"m"})
	if len(rs) != 1 || rs[0].OK || rs[0].Account != "first" {
		t.Fatalf("pinned to the first key: %+v (asked %v)", rs, asked())
	}
	// nothing pinned: routing's pick, still named
	rs = p.TestModels(context.Background(), []string{"m"})
	if len(rs) != 1 || rs[0].Account == "" {
		t.Fatalf("unpinned: %+v", rs)
	}
	// a key that isn't there is said, not swapped for another
	rs = p.TestModels(TestOn(context.Background(), "nope"), []string{"m"})
	if len(rs) != 1 || rs[0].OK || rs[0].Error == "" {
		t.Fatalf("a key that is not there: %+v", rs)
	}
}

// One key is nothing to pick from.
func TestTestPicksNeedSeveral(t *testing.T) {
	if got := (Provider{ID: "x", Key: "sk-a"}).TestPicks(); got != nil {
		t.Fatalf("one key: %+v", got)
	}
}

// A pool's test pinned to its second account asks it, though the first
// is the one routing takes, and names it (twoAccountPlugin's fake vendor
// answers b@fake alone).
func TestModelTestPinnedToAnAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, wrong := twoAccountPlugin(t, ctx, nil)
	if got := p.TestPicks(); len(got) != 2 || got[0].ID != "a@fake" || got[1].ID != "b@fake" {
		t.Fatalf("the picks: %+v", got)
	}
	rs := p.TestModels(TestOn(ctx, "b@fake"), []string{"fake-1"})
	if len(rs) != 1 || !rs[0].OK || rs[0].Account != "b@fake" {
		t.Fatalf("pinned to b@fake: %+v (others asked %v)", rs, wrong())
	}
	rs = p.TestModels(TestOn(ctx, "a@fake"), []string{"fake-1"})
	if len(rs) != 1 || rs[0].OK || rs[0].Account != "a@fake" {
		t.Fatalf("pinned to a@fake, which the vendor turns away: %+v", rs)
	}
	rs = p.TestModels(TestOn(ctx, "c@fake"), []string{"fake-1"})
	if len(rs) != 1 || rs[0].OK || rs[0].Error == "" {
		t.Fatalf("an account that isn't there: %+v", rs)
	}
}
