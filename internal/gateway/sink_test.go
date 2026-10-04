package gateway

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func forgetSinks(t *testing.T) {
	t.Helper()
	clear := func() {
		sunk.Lock()
		sunk.m = map[string]sinking{}
		sunk.Unlock()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.note = map[string]Rest{}
		restingUntil.Unlock()
		routed.Lock()
		routed.failures = map[string]int{}
		routed.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// rested ends what rests by key's rest, as if its minute were up.
func rested(key string) {
	restingUntil.Lock()
	restingUntil.m[key] = time.Now().Add(-time.Second)
	restingUntil.Unlock()
}

func whoOf(cs []candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.p.Account.User)
	}
	return out
}

// 01huadalang (WorkBuddy, several accounts, in order): an account rate
// limited while it has quota left goes back to the front as soon as its
// rest ends, and is the one a vendor's risk control notices. With Sink on
// it goes to the back instead, behind every one not rate limited since,
// and comes round again only once those ahead of it were rate limited in
// their turn. A used-up account doesn't sink, a resting one still waits
// at the back, and with Sink off the order is as it was.
func TestRateLimitedSinksToTheBack(t *testing.T) {
	forgetSinks(t)
	old := allowances
	defer func() { allowances = old }()
	used := map[string]float64{"a@x.com": 10, "b@x.com": 10, "c@x.com": 10}
	allowances = func(string) map[string]provider.Allowance {
		out := map[string]provider.Allowance{}
		for u, v := range used {
			out[u] = provider.Allowance{{Used: v, Span: 30 * 24 * time.Hour, Resets: time.Now().Add(24 * time.Hour)}}
		}
		return out
	}
	s := &Server{}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Routing: provider.Ordered, Account: &provider.Account{Agent: "workbuddy", User: user}}, model: "m", rest: "workbuddy@" + user}
	}
	cs := []candidate{acct("a@x.com"), acct("b@x.com"), acct("c@x.com")}
	p := provider.Provider{ID: "workbuddy", Routing: provider.Ordered, Sink: true}
	order := func(p provider.Provider) []string {
		out, _ := weigh(p, slices.Clone(cs), "m", "")
		out, _ = restLast(out, planned{order: make([]Weighed, len(out))})
		return whoOf(out)
	}
	rate := []byte(`{"error":{"message":"Too many requests","type":"rate_limit_error"}}`)
	limit := func(i int) {
		if r := s.restAfter(cs[i], 429, http.Header{}, rate); r.Why != failRate {
			t.Fatalf("%s: %s", cs[i].p.Account.User, r.Why)
		}
		rested(cs[i].restKey())
	}
	want := func(p provider.Provider, w ...string) {
		t.Helper()
		if got := order(p); !slices.Equal(got, w) {
			t.Fatalf("order %v, want %v", got, w)
		}
	}
	off := p
	off.Sink = false

	want(p, "a@x.com", "b@x.com", "c@x.com")
	limit(0)
	want(off, "a@x.com", "b@x.com", "c@x.com") // off: back to the first
	want(p, "b@x.com", "c@x.com", "a@x.com")
	limit(1)
	want(p, "c@x.com", "a@x.com", "b@x.com")
	limit(2)
	want(p, "a@x.com", "b@x.com", "c@x.com") // round again: a sank first
	limit(0)
	want(p, "b@x.com", "c@x.com", "a@x.com")

	// in the trace, the one at the back says when it sank
	_, wg := weigh(p, slices.Clone(cs), "m", "")
	if w := weighed(cs[0], p, wg, false, ""); w.Sunk == nil {
		t.Fatal("trace: a sank, and doesn't say so")
	}
	if w := weighed(cs[0], off, wg, false, ""); w.Sunk != nil {
		t.Fatal("trace: sank where the order doesn't sink")
	}

	// in turn goes round already: nothing sinks there
	turn := p
	turn.Routing = provider.Rotate
	if !sinks(p.Sink, p.Routing) || sinks(turn.Sink, turn.Routing) {
		t.Fatal("sinks: in order sinks, in turn doesn't")
	}

	// out of quota, or rate limited with its allowance used up, it doesn't
	// sink: it rests until the quota comes back, and keeps its place
	forgetSinks(t)
	s.restAfter(cs[0], 429, http.Header{}, []byte(`{"error":{"message":"You have hit your usage limit"}}`))
	rested(cs[0].restKey())
	want(p, "a@x.com", "b@x.com", "c@x.com")
	used["a@x.com"] = 100
	s.restAfter(cs[0], 429, http.Header{}, rate)
	used["a@x.com"] = 10
	rested(cs[0].restKey())
	want(p, "a@x.com", "b@x.com", "c@x.com")

	// a sunk one resting is still behind every one ready
	limit(0)
	s.restAfter(cs[1], 429, http.Header{}, rate) // b rests on
	want(p, "c@x.com", "a@x.com", "b@x.com")
}

// A routing group with Sink: a member's key rate limited with quota left
// goes behind the other members, in order or not, and the trace's order
// follows it.
func TestGroupRateLimitedSinks(t *testing.T) {
	forgetSinks(t)
	s := &Server{}
	mem := func(id string) provider.Member {
		return provider.Member{ID: id + "/m", Path: []string{id + "/m"}, Provider: provider.Provider{ID: id, Chat: "http://127.0.0.1:1/v1", Key: "k-" + id}, Model: "m"}
	}
	ms := []provider.Member{mem("a"), mem("b"), mem("c")}
	ids := func(cs []candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.p.ID)
		}
		return out
	}
	rate := []byte(`{"error":{"message":"Too many requests"}}`)
	cs, _ := s.planGroup(provider.Group{ID: "g", Routing: provider.Ordered}, ms, provider.Chat)
	s.restAfter(cs[0], 429, http.Header{}, rate)
	rested(cs[0].restKey())
	for _, routing := range []string{provider.Ordered, ""} {
		g := provider.Group{ID: "g", Routing: routing, Sink: true}
		out, pl := s.planGroup(g, ms, provider.Chat)
		if got := ids(out); !slices.Equal(got, []string{"b", "c", "a"}) {
			t.Fatalf("%q: order %v", routing, got)
		}
		if pl.order[2].Provider != "a" || pl.order[2].Sunk == nil || pl.order[0].Sunk != nil {
			t.Fatalf("%q: trace %+v", routing, pl.order)
		}
		g.Sink = false
		if out, _ := s.planGroup(g, ms, provider.Chat); ids(out)[0] != "a" {
			t.Fatalf("%q off: order %v", routing, ids(out))
		}
	}
}
