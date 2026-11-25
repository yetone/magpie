package middleware

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/internal/usage"
)

// serve serves a Points observer as the gateway would, until the test ends.
var warmPrices sync.Once

func serve(t *testing.T) *observer {
	t.Helper()
	// The first row a process prices loads the price catalog, ≈2s idle and
	// past until's 10s at load 80–100: a test about the queue doesn't wait
	// for it (TestOnUsageNeverBlocks went red so in a full run).
	warmPrices.Do(func() { usage.NewCoster()(usage.Record{Provider: "p"}) })
	o := Points().(*observer)
	stop := o.Serve()
	t.Cleanup(stop)
	return o
}

func seats(n int) []routes.Seat {
	out := make([]routes.Seat, n)
	for i := range out {
		out[i] = routes.Seat{Key: string(rune('a' + i)), Weight: 1, Soon: []time.Time{time.UnixMilli(1700000000000)}}
	}
	return out
}

func order(t *testing.T, routing string, n int) []int {
	t.Helper()
	s, ok := routes.Find(routing)
	if !ok {
		t.Fatalf("%s isn't served", routing)
	}
	return routes.Apply(s, "p1", seats(n))
}

// until waits for ok, a state, never a fixed time.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); {
		if time.Now().After(end) {
			t.Fatal("never:", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A module's strategies are routings while it is served, by
// "<plugin>/<name>", a function or an object's order alike; they are given
// the pool, the seats and the options. Off in plugins.json, reloaded or no
// longer served, they are gone, and a pool routed by one is tried in its
// listed order.
func TestStrategiesServedWhileOn(t *testing.T) {
	e := file(t, `export const strategies = {
		reverse: (pool, seats) => seats.map((_, i) => i).reverse(),
		keyed: { order(pool, seats, options) {
			if (pool !== "p1" || seats[1].key !== "b" || seats[0].weight !== 1 || seats[0].soon[0] !== 1700000000000 || options.first !== 2) throw new Error("wrong input " + JSON.stringify([pool, seats[0], options]));
			return [2, 0, 1];
		} },
	};`)
	e.Options = map[string]any{"first": 2}
	use(t, e)
	if routes.Served("x/reverse") {
		t.Fatal("served before the gateway serves it")
	}
	o := serve(t)
	if got := order(t, "x/reverse", 3); !slices.Equal(got, []int{2, 1, 0}) {
		t.Fatal("reverse:", got)
	}
	if got := order(t, "x/keyed", 3); !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatal("keyed:", got, States()[e.Spec])
	}
	st := States()[e.Spec]
	if !slices.Equal(st.Strategies, []string{"x/keyed", "x/reverse"}) || st.Orders != 2 || st.Failures != 0 || len(st.Hooks) != 0 {
		t.Fatalf("state %+v", st)
	}
	if r := Routings(); len(r) != 2 || r[0].ID != "x/keyed" || r[0].Plugin != e.Spec {
		t.Fatalf("routings %+v", r)
	}
	if Begin(info()) != nil {
		t.Fatal("a module with no hooks began a request")
	}

	// off in plugins.json
	e.Off = true
	use(t, e)
	o.refresh()
	if routes.Served("x/reverse") || len(Routings()) != 0 {
		t.Fatal("served while off")
	}
	e.Off = false
	use(t, e)
	o.refresh()
	if !routes.Served("x/reverse") {
		t.Fatal("not served on again")
	}

	// a reload serves the new set only
	os.WriteFile(e.Spec, []byte(`export const strategies = { other: (p, s) => s.map((_, i) => i) };`), 0o644)
	Reload()
	o.refresh()
	if routes.Served("x/reverse") || !routes.Served("x/other") {
		t.Fatal("a reload kept the old strategies")
	}
}

// A module turned off here (the Plugins page) is no longer served at once,
// without waiting for the look for other processes' writes, which is
// seconds apart (#3185).
func TestStrategiesFollowOwnWrite(t *testing.T) {
	e := file(t, `export const strategies = { a: (p, s) => s.map((_, i) => i) };`)
	use(t, e)
	serve(t)
	if !routes.Served("x/a") {
		t.Fatal("not served")
	}
	if err := plugin.SetOff(plugin.Name(e.Spec), true); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(2 * time.Second); routes.Served("x/a"); {
		if time.Now().After(end) {
			t.Fatal("still served 2s after it was turned off here")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Routings lists a plugin's routing the moment it is on, without waiting
// for the observer to hear the write: a picker drawn just after the
// plugin was installed offered it only once the page was read again
// (guhailin3 on X). plugins.json is written here as another process would,
// which the observer only looks for every few seconds.
func TestRoutingsServeTheSetInForce(t *testing.T) {
	e := file(t, `export const strategies = { a: (p, s) => s.map((_, i) => i) };`)
	use(t, e)
	serve(t)
	if r := Routings(); len(r) != 1 || r[0].ID != "x/a" {
		t.Fatalf("routings %+v", r)
	}
	p := filepath.Join(t.TempDir(), "pace-drain.middleware.js")
	if err := os.WriteFile(p, []byte(`export const strategies = { pd: (p, s) => s.map((_, i) => i) };`), 0o644); err != nil {
		t.Fatal(err)
	}
	use(t, e, plugin.Entry{Spec: p})
	r := Routings()
	if len(r) != 2 || !slices.ContainsFunc(r, func(x Routing) bool { return x.ID == "pace-drain/pd" }) {
		t.Fatalf("a plugin just turned on isn't listed: %+v", r)
	}
	if !routes.Served("pace-drain/pd") {
		t.Fatal("listed but not served")
	}
}

// Not served (plugins/middleware off), nothing is registered.
func TestStrategiesStopWithServe(t *testing.T) {
	use(t, file(t, `export const strategies = { a: (p, s) => s.map((_, i) => i) };`))
	o := Points().(*observer)
	stop := o.Serve()
	if !routes.Served("x/a") {
		t.Fatal("not served")
	}
	stop()
	if routes.Served("x/a") || len(Routings()) != 0 {
		t.Fatal("served after stop")
	}
}

// A strategy that throws, runs past its time, waits on a busy runtime or
// returns something other than an order of the seats leaves them in their
// listed order, and each is a failure on the Plugins page.
func TestStrategyFailsOpen(t *testing.T) {
	e := file(t, `export const strategies = {
		throws: () => { throw new Error("nope"); },
		slow: (p, s) => { for (;;) {} },
		twice: () => [0, 0, 1],
		short: () => [1, 0],
		fraction: () => [0.5, 1, 2],
		text: () => "0,1,2",
		nulls: () => [null, 1, 2],
		pending: () => new Promise(() => {}),
	};`)
	use(t, e)
	serve(t)
	for _, name := range []string{"throws", "slow", "twice", "short", "fraction", "text", "nulls", "pending"} {
		start := time.Now()
		if got := order(t, "x/"+name, 3); !slices.Equal(got, []int{0, 1, 2}) {
			t.Fatal(name, got)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%s held the request %v", name, d)
		}
	}
	st := States()[e.Spec]
	if st.Failures != 8 || !strings.Contains(st.Last, "strategies.pending") {
		t.Fatalf("state %+v", st)
	}

	// a busy runtime: onUsage or another request holds it
	m := current().list[0]
	m.pt.take(0)
	start := time.Now()
	if got := order(t, "x/twice", 3); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatal("busy:", got)
	}
	m.pt.give()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v on a busy runtime", d)
	}
	if st := States()[e.Spec]; st.Failures != 9 || !strings.Contains(st.Last, "busy") {
		t.Fatalf("busy state %+v", st)
	}
}

// onUsage is given each ledger row, without its error's text, its
// session, its request id or anything else not on the allow-list, and
// shares its runtime with the module's strategies.
func TestOnUsageRows(t *testing.T) {
	e := file(t, `
		let seen = [];
		export function onUsage(row, options) { seen.push([Object.keys(row).sort().join(","), row.provider, row.input, row.cost, options.o]); }
		export const strategies = { seen: (p, s) => { throw new Error(JSON.stringify(seen)); } };`)
	e.Options = map[string]any{"o": 1}
	use(t, e)
	o := serve(t)
	cost := 0.5
	o.Ledger(usage.Record{Time: time.Now(), Provider: "p1", Model: "m", Input: 10, Output: 5, Status: 200,
		Error: "secret error", RequestID: "rid", Session: "sess", Endpoint: "/v1/messages", OwnCost: &cost, CallerKeyName: "k"})
	until(t, "the row given", func() bool { return States()[e.Spec].Rows == 1 })
	order(t, "x/seen", 2)
	last := States()[e.Spec].Last
	i := strings.Index(last, "[[")
	if i < 0 {
		t.Fatal(last)
	}
	var seen [][]any
	if err := json.Unmarshal([]byte(last[i:]), &seen); err != nil || len(seen) != 1 {
		t.Fatal(last, err)
	}
	keys := strings.Split(seen[0][0].(string), ",")
	want := []string{"agent", "cacheRead", "cacheWrite", "callerKeyName", "cost", "input", "model", "ms", "output", "provider", "reasoning", "rejected", "status", "time", "ttftMs"}
	sort.Strings(want)
	if !slices.Equal(keys, want) || seen[0][1] != "p1" || seen[0][2] != float64(10) || seen[0][3] != 0.5 || seen[0][4] != float64(1) {
		t.Fatalf("row %v", seen)
	}
	if st := States()[e.Spec]; !st.Usage || st.Dropped != 0 {
		t.Fatalf("state %+v", st)
	}
}

// Every field of a Record that isn't on the allow-list stays out of the
// row, whatever it holds.
func TestUsageRowAllowList(t *testing.T) {
	r := usage.Record{Time: time.Now(), Provider: "p", Model: "m", Error: "body text DB_PASSWORD", RequestID: "rid-x",
		ResponseID: "resp-x", Session: "sess-x", NativeSession: "native-x", Endpoint: "/v1/x", Host: "host-x", Stop: "stop-x",
		Subagent: "sub-x", SessionAccount: "acct-x"}
	b, _ := json.Marshal(rowOf(r, nil))
	for _, s := range []string{"DB_PASSWORD", "rid-x", "resp-x", "sess-x", "native-x", "/v1/x", "host-x", "stop-x", "sub-x", "acct-x"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("%s in %s", s, b)
		}
	}
	if !strings.Contains(string(b), `"cost":null`) {
		t.Fatal("an unpriced row has a cost:", string(b))
	}
}

// A slow onUsage never holds the ledger's write up: what doesn't fit the
// queue is dropped and counted.
func TestOnUsageNeverBlocks(t *testing.T) {
	defer func(n int) { usageQueue = n }(usageQueue)
	usageQueue = 2
	e := file(t, `export function onUsage() { const end = Date.now() + 100; while (Date.now() < end) {} }`)
	use(t, e)
	o := serve(t)
	for range 20 {
		start := time.Now()
		o.Ledger(usage.Record{Time: time.Now(), Provider: "p"})
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("Ledger waited %v", d)
		}
	}
	st := States()[e.Spec]
	if st.Dropped < 15 {
		t.Fatalf("state %+v", st)
	}
	until(t, "the queued rows given", func() bool { return States()[e.Spec].Rows+States()[e.Spec].Dropped == 20 })
}

// Not served, onUsage is never called.
func TestOnUsageOnlyWhileServed(t *testing.T) {
	e := file(t, `export function onUsage() {}`)
	use(t, e)
	o := Points().(*observer)
	o.Ledger(usage.Record{Provider: "p"})
	stop := o.Serve()
	stop()
	o.Ledger(usage.Record{Provider: "p"})
	if st := States()[e.Spec]; st.Rows != 0 || st.Dropped != 0 {
		t.Fatalf("state %+v", st)
	}
}

// A gateway module reaches no network and no file: there is no fetch,
// XMLHttpRequest, WebSocket, require, process or Bun to call.
func TestPointsHaveNoNetwork(t *testing.T) {
	e := file(t, `
		const names = ["fetch", "XMLHttpRequest", "WebSocket", "require", "process", "Bun", "Deno", "navigator", "importScripts"];
		export const strategies = { probe: () => { const has = names.filter(n => typeof globalThis[n] !== "undefined"); if (has.length) throw new Error("has " + has.join(",")); return [0, 1]; } };`)
	use(t, e)
	serve(t)
	order(t, "x/probe", 2)
	if st := States()[e.Spec]; st.Failures != 0 {
		t.Fatal(st.Last)
	}
}

// A module with none of the exports, or a strategy named or made wrong,
// doesn't load, and says what it may export.
func TestPointsLoadErrors(t *testing.T) {
	none := file(t, `export const x = 1;`)
	badName := file(t, `export const strategies = { "a/b": () => [] };`)
	notFn := file(t, `export const strategies = { a: 1 };`)
	notObj := file(t, `export const strategies = 3;`)
	def := file(t, `export default { strategies: { d: { order: (p, s) => s.map((_, i) => i) } } };`)
	use(t, none, badName, notFn, notObj, def)
	st := States()
	if !strings.Contains(st[none.Spec].Error, "onRequest, onAccount, onProvider, onEvent, onResponse, strategies and onUsage") {
		t.Fatal(st[none.Spec].Error)
	}
	for _, e := range []plugin.Entry{badName, notFn, notObj} {
		if st[e.Spec].Error == "" {
			t.Fatalf("%s loaded", e.Spec)
		}
	}
	if st[def.Spec].Error != "" || !slices.Equal(st[def.Spec].Strategies, []string{"x/d"}) {
		t.Fatalf("default export: %+v", st[def.Spec])
	}
}

// A package's gateway module is named by magpie.middleware or
// magpie.gateway, and its routings by the package's short name.
func TestGatewayAlias(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "magpie-fastest")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"magpie-fastest","magpie":{"gateway":"gw.js"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "gw.js"), []byte(`export const strategies = { first: (p, s) => s.map((_, i) => i) };`), 0o644)
	if f, only := plugin.Middleware(dir); f != filepath.Join(dir, "gw.js") || !only {
		t.Fatalf("Middleware = %q, %v", f, only)
	}
	use(t, plugin.Entry{Spec: dir})
	if st := States()[dir]; st.Error != "" || !slices.Equal(st.Strategies, []string{"magpie-fastest/first"}) {
		t.Fatalf("%+v", st)
	}
	g := filepath.Join(t.TempDir(), "lat.gateway.js")
	os.WriteFile(g, []byte(`export function onUsage() {}`), 0o644)
	if f, _ := plugin.Middleware(g); f != g {
		t.Fatal("a .gateway.js isn't a gateway module")
	}
}

// The docs' example (testdata/spread.gateway.js) loads, orders by tokens
// with resting seats last, and takes rows.
func TestDocsExample(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "spread.gateway.js"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "spread.gateway.js")
	os.WriteFile(p, src, 0o644)
	use(t, plugin.Entry{Spec: p, Options: map[string]any{"slowMs": 10}})
	o := serve(t)
	s := seats(4)
	s[0].Tokens, s[1].Tokens, s[2].Tokens, s[3].Tokens = 30, 10, 20, 1
	s[3].Resting = true
	r, ok := routes.Find("spread/fewest-tokens")
	if !ok {
		t.Fatal("not served", States()[p])
	}
	if got := routes.Apply(r, "p1", s); !slices.Equal(got, []int{1, 2, 0, 3}) {
		t.Fatal(got, States()[p])
	}
	o.Ledger(usage.Record{Time: time.Now(), Provider: "p1", Model: "m", Millis: 50, Status: 200})
	until(t, "the row given", func() bool { return States()[p].Rows == 1 })
	if st := States()[p]; st.Failures != 0 || !st.Usage {
		t.Fatalf("state %+v", st)
	}
}

// A file's name is made into what a routing may hold, so the routing is
// one providers.json keeps (provider.PluginRouting).
func TestRoutingFromFileName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "my plügin (2).gateway.js")
	os.WriteFile(p, []byte(`export const strategies = { x: (p, s) => s.map((_, i) => i) };`), 0o644)
	use(t, plugin.Entry{Spec: p})
	serve(t)
	if r := Routings(); len(r) != 1 || r[0].ID != "my-pl-gin-2/x" || !provider.PluginRouting(r[0].ID) {
		t.Fatalf("%+v", r)
	}
}
