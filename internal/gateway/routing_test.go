package gateway

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Tests don't ask vendors how much of an allowance is used: the gateway
// would, behind a request, of whatever upstream a test serves.
func init() { allowances = func(string) map[string]provider.Allowance { return nil } }

func restsOf(cs []candidate) string {
	s := ""
	for _, c := range cs {
		s += c.rest + " "
	}
	return s
}

func TestRouting(t *testing.T) {
	cs := []candidate{{rest: "r#a"}, {rest: "r#b"}, {rest: "r#c"}}
	p := provider.Provider{ID: "r"}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "r#a r#b r#c " {
		t.Fatalf("smart: %s", got)
	}

	p.Routing = provider.Rotate
	var firsts string
	for range 4 {
		firsts += route(p, cs, "m", provider.Chat)[0].rest + " "
	}
	if firsts != "r#a r#b r#c r#a " {
		t.Fatalf("in turn: %s", firsts)
	}
	if restsOf(cs) != "r#a r#b r#c " {
		t.Fatal("rotating changed the list it was given")
	}

	p.Routing = provider.Ordered
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "r#a r#b r#c " {
		t.Fatalf("in order: %s", got)
	}

	p.ID, p.Routing = "u", provider.LeastUsed
	cs = []candidate{{rest: "u#a"}, {rest: "u#b"}, {rest: "u#c"}}
	served("u#a", 5000)
	served("u#c", 10)
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "u#b u#c u#a " {
		t.Fatalf("least used: %s", got)
	}
}

// Smart routing keeps the first while it has quota to spare, then goes to
// whichever has the most; a failure rests as long as it says.
func TestSmartRouting(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	one := func(used float64, resets time.Time) provider.Allowance {
		return provider.Allowance{{Used: used, Resets: resets}}
	}
	share := map[string]provider.Allowance{"a": one(10, time.Time{}), "b": one(50, time.Time{}), "c": one(20, time.Time{})}
	allowances = func(string) map[string]provider.Allowance { return share }
	acctFor := func(user, model string) candidate {
		return candidate{p: provider.Provider{Account: &provider.Account{Agent: "x", User: user}}, model: model, rest: "s#" + user}
	}
	acct := func(user string) candidate { return acctFor(user, "m") }
	p := provider.Provider{ID: "s", Account: &provider.Account{Agent: "x"}}
	cs := []candidate{acct("a"), acct("b"), acct("c")}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#a s#b s#c " {
		t.Fatalf("all fine: %s", got)
	}
	share["a"], share["b"] = one(99, time.Time{}), one(93, time.Time{})
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#c s#b s#a " {
		t.Fatalf("a used up, b low: %s", got)
	}

	// the allowance renewing soonest goes first: what it has left is lost
	// at its reset; one not known, or already renewed, after
	now := time.Now()
	share = map[string]provider.Allowance{
		"a": one(5, now.Add(6*24*time.Hour)),
		"b": one(70, now.Add(20*time.Hour)),
		"c": one(10, time.Time{}),
		"d": one(40, now.Add(3*24*time.Hour)),
		"e": one(95, now.Add(time.Hour)),
		"f": one(99, now.Add(-time.Hour)), // used up, but that has renewed since
	}
	cs = []candidate{acct("a"), acct("b"), acct("c"), acct("d"), acct("e"), acct("f")}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#b s#d s#a s#c s#f s#e " {
		t.Fatalf("by reset: %s", got)
	}
	// minutes apart is the same hour: the order given stays
	share["a"] = one(5, share["b"][0].Resets.Truncate(time.Hour).Add(time.Minute))
	share["b"] = one(70, share["b"][0].Resets.Truncate(time.Hour).Add(2*time.Minute))
	if got := restsOf(route(p, cs, "m", provider.Chat)[:2]); got != "s#a s#b " {
		t.Fatalf("same hour: %s", got)
	}

	// the week decides, not the five hours in it; the five hours only
	// between weeks renewing in the same hour
	// Keep the weekly resets in one hour: time.Now() near an hour
	// boundary made this tie-breaker test flaky in CI.
	routingNow := now.Truncate(time.Hour).Add(20 * time.Minute)
	week, day := 7*24*time.Hour, 24*time.Hour
	both := func(five, weekly time.Duration) provider.Allowance {
		return provider.Allowance{
			{Used: 30, Resets: routingNow.Add(five), Span: 5 * time.Hour},
			{Used: 30, Resets: routingNow.Add(weekly), Span: week},
		}
	}
	share = map[string]provider.Allowance{
		"a": both(10*time.Minute, 5*day),
		"b": both(4*time.Hour, 2*day),
		"c": both(3*time.Hour, 2*day+time.Minute),
	}
	cs = []candidate{acct("a"), acct("b"), acct("c")}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#c s#b s#a " {
		t.Fatalf("week, then five hours: %s", got)
	}

	// Opus's own weekly allowance used up leaves the account to Sonnet
	share = map[string]provider.Allowance{
		"a": {{Used: 20, Span: week}, {Used: 100, Resets: now.Add(day), Span: week, Model: "opus"}},
		"b": {{Used: 60, Span: week}},
	}
	for _, c := range []struct{ model, want string }{{"claude-opus-5-5", "s#b s#a "}, {"claude-sonnet-5", "s#a s#b "}} {
		cs = []candidate{acctFor("a", c.model), acctFor("b", c.model)}
		if got := restsOf(route(p, cs, c.model, provider.Chat)); got != c.want {
			t.Fatalf("%s: %s", c.model, got)
		}
	}

	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{402, `{}`, failCredit},
		{400, `{"error":{"message":"Your credit balance is too low"}}`, failCredit},
		{429, `{"error":{"code":"insufficient_quota"}}`, failCredit},
		{403, `账户余额不足`, failCredit},
		{429, `{"error":{"message":"usage limit reached for your plan"}}`, failQuota},
		{429, `{"error":{"message":"Too many requests"}}`, failRate},
		{429, "You've hit your limit · resets 5pm (Asia/Shanghai)", failQuota},
		{502, "Claude AI usage limit reached|1790000000", failQuota},
		{503, `overloaded`, failOther},
	} {
		if got := failure(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %s, want %s", c.status, c.body, got, c.want)
		}
	}

	s := &Server{}
	until := func(id string) time.Duration {
		restingUntil.Lock()
		defer restingUntil.Unlock()
		return time.Until(restingUntil.m[id]).Round(time.Minute)
	}
	key := func(rest string) candidate { return candidate{rest: rest} }
	s.restAfter(key("t#credit"), 402, http.Header{}, nil)
	h := http.Header{}
	h.Set("Retry-After", "300")
	s.restAfter(key("t#rate"), 429, h, []byte("slow down"))
	s.restAfter(key("t#fail"), 500, http.Header{}, nil)
	s.restAfter(key("t#fail"), 500, http.Header{}, nil)
	if until("t#credit") != creditRest || until("t#rate") != 5*time.Minute || until("t#fail") != 2*time.Minute {
		t.Fatalf("rests: %v %v %v", until("t#credit"), until("t#rate"), until("t#fail"))
	}
	served("t#fail", 1)
	s.restAfter(key("t#fail"), 500, http.Header{}, nil)
	if until("t#fail") != time.Minute {
		t.Fatalf("answering again starts over: %v", until("t#fail"))
	}
	// a subscription out of quota rests until the window it filled renews,
	// or until when Claude Code says it resets
	share = map[string]provider.Allowance{"q": {{Used: 30, Span: week}, {Used: 100, Resets: now.Add(3 * time.Hour), Span: 5 * time.Hour}}}
	s.restAfter(acct("q"), 429, http.Header{}, []byte("You've hit your limit · resets 5pm"))
	s.restAfter(acct("r"), 429, http.Header{}, []byte("Claude AI usage limit reached|"+strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)))
	s.restAfter(acct("o"), 429, http.Header{}, []byte("You've hit your limit"))
	if until("s#q") != 3*time.Hour || until("s#r") != 2*time.Hour || until("s#o") != quotaRest {
		t.Fatalf("subscription rests: %v %v %v", until("s#q"), until("s#r"), until("s#o"))
	}
	// and so does one that failed some other way with a window full
	s.restAfter(acct("q"), 502, http.Header{}, []byte("Claude Code ended without an answer"))
	if until("s#q") != 3*time.Hour {
		t.Fatalf("failed full: %v", until("s#q"))
	}

	h = http.Header{}
	h.Set("X-Ratelimit-Reset-Tokens", "6m0s")
	if d := retryAfter(h, time.Now()); d != 6*time.Minute {
		t.Fatalf("reset header: %v", d)
	}
}
