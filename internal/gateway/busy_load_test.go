package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func busyLoadAt(t *testing.T, at int) {
	t.Helper()
	s := settings.Load()
	was := s.BusyLoad
	s.BusyLoad = at
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s := settings.Load()
		s.BusyLoad = was
		settings.Save(s)
	})
}

// A model its plugin says is busier than settings.BusyLoad goes behind
// the ready candidates, ahead of those resting after a failure
// (axelburks on Discord: an in-house vendor queues requests to its busy
// models). One with no load reported, or one older than
// plugin.LoadFresh, keeps its place; so does every one with the setting
// off, as it is by default.
func TestBusyModelGoesBehind(t *testing.T) {
	plugin.ForgetLoads()
	t.Cleanup(plugin.ForgetLoads)
	now := time.Now()
	plugin.NoteLoadsForTest("inhouse", map[string]float64{"big": 92, "mid": 60}, now)
	plugin.NoteLoadsForTest("inhouse", map[string]float64{"old": 99}, now.Add(-plugin.LoadFresh-time.Minute))
	p := provider.PluginForTest("inhouse-plugin", "inhouse")
	c := func(model string) candidate { return candidate{p: p, model: model, rest: "inhouse-plugin/" + model} }
	resting := candidate{p: provider.Provider{ID: "down"}, model: "m", rest: "down-busy-test"}
	restingUntil.Lock()
	restingUntil.m[resting.restKey()] = time.Now().Add(time.Minute)
	restingUntil.Unlock()
	t.Cleanup(func() { restingUntil.Lock(); delete(restingUntil.m, resting.restKey()); restingUntil.Unlock() })
	order := func() ([]string, []float64) {
		in := []candidate{c("big"), c("mid"), resting, c("old"), c("none")}
		out, pl := restLast(in, planned{order: make([]Weighed, len(in))})
		var got []string
		var busy []float64
		for i, x := range out {
			got = append(got, x.rest)
			busy = append(busy, pl.order[i].Busy)
		}
		return got, busy
	}
	same := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	off := []string{"inhouse-plugin/big", "inhouse-plugin/mid", "inhouse-plugin/old", "inhouse-plugin/none", "down-busy-test"}
	if got, _ := order(); !same(got, off) {
		t.Fatalf("off: %v, want %v", got, off)
	}
	busyLoadAt(t, 80)
	want := []string{"inhouse-plugin/mid", "inhouse-plugin/old", "inhouse-plugin/none", "inhouse-plugin/big", "down-busy-test"}
	got, busy := order()
	if !same(got, want) {
		t.Fatalf("over 80: %v, want %v", got, want)
	}
	if busy[3] != 92 || busy[0] != 0 || busy[1] != 0 {
		t.Fatalf("the trace's Busy = %v", busy)
	}
	busyLoadAt(t, 50)
	want = []string{"inhouse-plugin/old", "inhouse-plugin/none", "inhouse-plugin/big", "inhouse-plugin/mid", "down-busy-test"}
	if got, _ := order(); !same(got, want) {
		t.Fatalf("over 50: %v, want %v", got, want)
	}

	// a vendor that queues reports past 100 (axelburks: 200% happens),
	// and a threshold past 100 goes by it
	plugin.NoteLoadsForTest("inhouse", map[string]float64{"big": 180, "mid": 120}, now)
	busyLoadAt(t, 150)
	want = []string{"inhouse-plugin/mid", "inhouse-plugin/old", "inhouse-plugin/none", "inhouse-plugin/big", "down-busy-test"}
	got, busy = order()
	if !same(got, want) || busy[3] != 180 {
		t.Fatalf("over 150: %v %v, want %v", got, busy, want)
	}
	s := settings.Load()
	s.BusyLoad = settings.BusyLoadMost
	if err := settings.Save(s); err == nil {
		t.Fatalf("a threshold of %d was saved", settings.BusyLoadMost)
	}

	// a lone candidate is tried whatever its load
	if out, pl := restLast([]candidate{c("big")}, planned{order: make([]Weighed, 1)}); len(out) != 1 || pl.order[0].Busy != 0 {
		t.Fatalf("lone: %+v", pl.order)
	}
}

// A conversation a model answered that has since gone busy goes to the
// ready one that leads at its next turn, whatever it cached; within a
// turn it stays, on the reasoning it sealed. A turn going round, or a new
// conversation spread, never lands on the busy one past a ready one.
func TestBusyModelNotKeptAtNewTurn(t *testing.T) {
	p := provider.PluginForTest("inhouse-plugin", "inhouse")
	cs := []candidate{{p: p, model: "mid", rest: "inhouse-plugin/mid"}, {p: p, model: "big", rest: "inhouse-plugin/big"}}
	pl := planned{order: []Weighed{{Who: "mid"}, {Who: "big", Busy: 92}}}
	h := http.Header{}
	h.Set("x-claude-code-session-id", "busy-load-kept")
	turn := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	within := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	_, _, _, key := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, turn, turn, cs, pl)
	answered(key, cs[1], 1, Usage{CacheRead: 50_000})
	t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })

	got, _, aff, _ := affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, within, within, cs, pl)
	if got[0].rest != "inhouse-plugin/big" || !aff.Kept {
		t.Fatalf("within a turn: first %s, kept %v, why %s", got[0].rest, aff.Kept, aff.Why)
	}
	got, _, aff, _ = affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, turn, turn, cs, pl)
	if got[0].rest != "inhouse-plugin/mid" || aff.Kept || aff.Why != "busy" {
		t.Fatalf("a new turn: first %s, kept %v, why %s", got[0].rest, aff.Kept, aff.Why)
	}

	// every one busy: kept where it was
	both := planned{order: []Weighed{{Who: "mid", Busy: 85}, {Who: "big", Busy: 92}}}
	got, _, aff, _ = affine("t", provider.AffinitySession, false, false, false, false, h, provider.Chat, turn, turn, cs, both)
	if got[0].rest != "inhouse-plugin/big" || !aff.Kept {
		t.Fatalf("all busy: first %s, kept %v, why %s", got[0].rest, aff.Kept, aff.Why)
	}

	if got, _ := after(append([]candidate(nil), cs...), planned{order: append([]Weighed(nil), pl.order...)}, 0); got[0].rest != "inhouse-plugin/mid" {
		t.Fatalf("turn went to %s", got[0].rest)
	}
	if readyForNew(pl.order, 1) {
		t.Fatal("a new conversation may go to the busy one")
	}
}

// A plugin reports a model's load by its own id. One the user lists by
// another (Listed as) is judged busy by that load all the same (axelburks
// on Discord: a renamed model was never busy, the others were).
func TestBusyModelListedAsAnotherID(t *testing.T) {
	plugin.ForgetLoads()
	t.Cleanup(plugin.ForgetLoads)
	s := settings.Load()
	was := s.ModelIDs
	s.ModelIDs = map[string]string{"inhouse-plugin/fast": "big", "inhouse-plugin/grok": "grok-4.7@256k"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s := settings.Load(); s.ModelIDs = was; settings.Save(s) })
	now := time.Now()
	plugin.NoteLoadsForTest("inhouse", map[string]float64{"big": 92, "grok-4.7": 95}, now)
	p := provider.PluginForTest("inhouse-plugin", "inhouse")
	if own := p.OwnModelID("fast"); own != "big" {
		t.Fatalf("OwnModelID(fast) = %q", own)
	}
	for _, model := range []string{"fast", "grok", "big"} {
		load, busy := busyOf(candidate{p: p, model: model, rest: "inhouse-plugin/" + model}, 80, now)
		if !busy || load < 92 {
			t.Errorf("%s: load %v busy %v, want busy over 80", model, load, busy)
		}
	}
	if load, busy := busyOf(candidate{p: p, model: "other", rest: "inhouse-plugin/other"}, 80, now); busy || load != 0 {
		t.Errorf("other: load %v busy %v, want none", load, busy)
	}
}
