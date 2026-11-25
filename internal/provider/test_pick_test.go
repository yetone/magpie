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

// tmr11235 on X: "这个开启就直接上线启用了，但我想先测试能不能用再开". A
// key that is off is offered for a test too, after those on, and a test
// pinned to it is answered by it while it stays off: routing still never
// asks it, and nothing is written of it.
func TestModelTestOnAKeyOff(t *testing.T) {
	probeHome(t)
	srv, asked := askingRelay(t, "sk-c", "m")
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1",
		Key: "sk-a", KeyName: "first", Models: []string{"m"}, Keys: []KeyAccount{
			// one made for chat, which a test there would go to before a
			// key made for any if the pinned one weren't asked alone
			{Name: "chatty", Key: "sk-b", Protocol: Chat},
			{Name: "spare", Key: "sk-c", Off: true}}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	// the one off is offered, last
	picks := p.TestPicks()
	if len(picks) != 3 || picks[0].Name != "first" || picks[0].Off || picks[1].Off || picks[2].Name != "spare" || !picks[2].Off || picks[2].Note != "Off" || picks[1].Note != "" || picks[2].ID != keyID("sk-c") {
		t.Fatalf("the picks: %+v", picks)
	}
	rs := p.TestModels(TestOn(context.Background(), keyID("sk-c")), []string{"m"})
	if len(rs) != 1 || !rs[0].OK || rs[0].Account != "spare" {
		t.Fatalf("pinned to the key that is off: %+v (asked %v)", rs, asked())
	}
	if got := asked(); len(got) != 1 || got[0] != "sk-c:m" {
		t.Fatalf("asked %v, want sk-c alone", got)
	}
	// it is still off, and routing's test goes to a key on
	q, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Keys) != 2 || !q.Keys[1].Off || len(q.KeysOn()) != 2 {
		t.Fatalf("the key off after its test: %+v", q.Keys)
	}
	rs = q.TestModels(context.Background(), []string{"m"})
	if len(rs) != 1 || rs[0].OK {
		t.Fatalf("unpinned, a key on is asked: %+v", rs)
	}
	if got := asked(); got[len(got)-1] == "sk-c:m" {
		t.Fatalf("unpinned asked %v", got)
	}
}

// The same for a signed-in account turned off: offered after those on,
// marked off; its test goes to it; it stays off and out of AlsoOn, which
// routing takes its accounts from.
func TestModelTestOnAnAccountOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, wrong := twoAccountPlugin(t, ctx, nil)
	if err := SetLoginOn(pluginAgent(*p.Account.plugin), "b@fake", false); err != nil {
		t.Fatal(err)
	}
	p2, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.AlsoOn()) != 0 {
		t.Fatalf("b@fake turned off is still on: %d", len(p2.AlsoOn()))
	}
	got := p2.TestPicks()
	if len(got) != 2 || got[0].ID != "a@fake" || got[0].Off || got[1].ID != "b@fake" || !got[1].Off {
		t.Fatalf("the picks: %+v", got)
	}
	rs := p2.TestModels(TestOn(ctx, "b@fake"), []string{"fake-1"})
	if len(rs) != 1 || !rs[0].OK || rs[0].Account != "b@fake" {
		t.Fatalf("pinned to b@fake, off: %+v (others asked %v)", rs, wrong())
	}
	p3, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p3.AlsoOn()) != 0 || len(p3.AccountsOn()) != 1 {
		t.Fatalf("b@fake after its test: %d also on, %d on", len(p3.AlsoOn()), len(p3.AccountsOn()))
	}
	for _, l := range Logins(pluginAgent(*p.Account.plugin)) {
		if l.User == "b@fake" && l.On {
			t.Fatal("b@fake was turned on by its test")
		}
	}
	// unpinned, routing's test goes to a@fake, which the vendor turns away
	rs = p3.TestModels(ctx, []string{"fake-1"})
	if len(rs) != 1 || rs[0].OK || rs[0].Account != "a@fake" {
		t.Fatalf("unpinned: %+v", rs)
	}
}
