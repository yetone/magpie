package provider

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A rate window (#2463) holds a time of day, and a window that crosses
// midnight belongs to the day it starts on.

// atLocal is a time on this computer's clock, so a window and the time
// asked of it are always in one zone whatever the test runs under.
func atLocal(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
}

func TestParseClock(t *testing.T) {
	good := map[string]int{
		"00:00": 0, "09:00": 540, "9:30": 570, "23:59": 1439, "24:00": 1440, " 8:05 ": 485,
	}
	for s, want := range good {
		if n, ok := ParseClock(s); !ok || n != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d, true", s, n, ok, want)
		}
	}
	for _, s := range []string{"", "9", "9:5", "09:60", "24:01", "25:00", "ab:cd", "-1:00", "1:2:3", "0900"} {
		if n, ok := ParseClock(s); ok {
			t.Errorf("ParseClock(%q) = %d taken", s, n)
		}
	}
}

func TestCheckRateWindows(t *testing.T) {
	ok := []RateWindow{
		{Start: "09:00", End: "12:00", Rate: 3},
		{Days: []int{1, 2, 3, 4, 5}, Start: "22:00", End: "06:00", Rate: 0.5},
		{Start: "00:00", End: "24:00", Rate: 1000},
	}
	if bad := CheckRateWindows(ok); bad != "" {
		t.Fatalf("good windows: %s", bad)
	}
	bad := []struct {
		name string
		ws   []RateWindow
	}{
		{"nine windows", make([]RateWindow, RateWindowsMax+1)},
		{"bad start", []RateWindow{{Start: "9", End: "12:00", Rate: 2}}},
		{"bad end", []RateWindow{{Start: "09:00", End: "24:01", Rate: 2}}},
		{"start 24:00", []RateWindow{{Start: "24:00", End: "06:00", Rate: 2}}},
		{"empty span", []RateWindow{{Start: "09:00", End: "09:00", Rate: 2}}},
		{"day 7", []RateWindow{{Days: []int{7}, Start: "09:00", End: "12:00", Rate: 2}}},
		{"rate 0", []RateWindow{{Start: "09:00", End: "12:00", Rate: 0}}},
		{"rate negative", []RateWindow{{Start: "09:00", End: "12:00", Rate: -1}}},
		{"rate over 1000", []RateWindow{{Start: "09:00", End: "12:00", Rate: 1000.5}}},
		{"rate four decimals", []RateWindow{{Start: "09:00", End: "12:00", Rate: 0.1234}}},
	}
	for _, x := range bad {
		if CheckRateWindows(x.ws) == "" {
			t.Errorf("%s taken", x.name)
		}
	}
}

func TestRateAt(t *testing.T) {
	// any Tuesday; the days are taken from the day itself, so the test
	// stands in every zone and on every calendar
	day := atLocal(2026, 10, 20, 10, 0)
	if day.Weekday() != time.Tuesday {
		t.Fatalf("2026-10-20 is a %v", day.Weekday())
	}
	tue := []int{int(time.Tuesday)}

	// in from the start, out at the end
	ws := []RateWindow{{Start: "09:00", End: "12:00", Rate: 3}}
	if r := RateAt(ws, atLocal(2026, 10, 20, 9, 0)); r != 3 {
		t.Errorf("at the start: %v", r)
	}
	if r := RateAt(ws, atLocal(2026, 10, 20, 11, 59)); r != 3 {
		t.Errorf("a minute before the end: %v", r)
	}
	if r := RateAt(ws, atLocal(2026, 10, 20, 12, 0)); r != 0 {
		t.Errorf("at the end: %v", r)
	}

	// across midnight, every day: in before, in after
	ws = []RateWindow{{Start: "22:00", End: "06:00", Rate: 0.5}}
	for _, x := range []struct{ h, mi int }{{21, 59}, {6, 0}, {12, 0}} {
		if r := RateAt(ws, atLocal(2026, 10, 20, x.h, x.mi)); r != 0 {
			t.Errorf("outside 22:00-06:00 at %02d:%02d: %v", x.h, x.mi, r)
		}
	}
	for _, x := range []struct{ h, mi int }{{22, 0}, {23, 59}, {0, 0}, {5, 59}} {
		if r := RateAt(ws, atLocal(2026, 10, 20, x.h, x.mi)); r != 0.5 {
			t.Errorf("inside 22:00-06:00 at %02d:%02d: %v", x.h, x.mi, r)
		}
	}

	// across midnight on Tuesdays only: the early hours read as Tuesday's,
	// so Tuesday 22:00 into Wednesday 06:00, not Wednesday's night
	ws = []RateWindow{{Days: tue, Start: "22:00", End: "06:00", Rate: 2}}
	if r := RateAt(ws, atLocal(2026, 10, 20, 23, 0)); r != 2 {
		t.Errorf("Tuesday 23:00: %v", r)
	}
	if r := RateAt(ws, atLocal(2026, 10, 21, 5, 0)); r != 2 {
		t.Errorf("Wednesday 05:00, still Tuesday's window: %v", r)
	}
	if r := RateAt(ws, atLocal(2026, 10, 21, 22, 30)); r != 0 {
		t.Errorf("Wednesday 22:30, a Wednesday night: %v", r)
	}

	// the first window that holds the time wins
	ws = []RateWindow{
		{Start: "09:00", End: "12:00", Rate: 3},
		{Start: "00:00", End: "24:00", Rate: 0.5},
	}
	if r := RateAt(ws, atLocal(2026, 10, 20, 10, 0)); r != 3 {
		t.Errorf("the first of two: %v", r)
	}
	if r := RateAt(ws, atLocal(2026, 10, 20, 15, 0)); r != 0.5 {
		t.Errorf("the second of two: %v", r)
	}

	// a time written in another zone is read on this computer's clock: a
	// call reported as "…Z" is the same instant as the local one
	ws = []RateWindow{{Start: "10:00", End: "14:00", Rate: 3}}
	in := atLocal(2026, 10, 20, 11, 0)
	for _, z := range []*time.Location{time.UTC, time.FixedZone("far", -11*3600), time.FixedZone("east", 14*3600)} {
		if r := RateAt(ws, in.In(z)); r != 3 {
			t.Errorf("local 11:00 written in %s (%v): %v", z, in.In(z), r)
		}
	}

	// a window that can't be read stands aside, and none at all is 0
	if r := RateAt([]RateWindow{{Start: "bad", End: "12:00", Rate: 3}}, atLocal(2026, 10, 20, 10, 0)); r != 0 {
		t.Errorf("an unreadable window: %v", r)
	}
	if r := RateAt(nil, atLocal(2026, 10, 20, 10, 0)); r != 0 {
		t.Errorf("no window: %v", r)
	}
}

// A provider's windows multiply the price a call is counted at, at the
// call's own time, on top of the price rate; BasePriceIn gives the price
// without them and the windows to read a time against.
func TestRateWindowsPrice(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Models: []string{"sol"}, PriceRate: 2,
		RateWindows: []RateWindow{{Start: "09:00", End: "12:00", Rate: 3}}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	peak := atLocal(2026, 10, 20, 10, 0)
	off := atLocal(2026, 10, 20, 15, 0)
	// the list price is 2 in, 10 out; PriceRate 2 makes 4 and 20, the
	// peak window's 3 makes 12 and, outside, 20 again
	pr, ok := EffectivePriceAt(settings.Load(), "relay", "sol", peak)
	if !ok || pr.Input != 12 || pr.Output != 60 {
		t.Fatalf("at the peak of a 2× relay: %+v %v", pr, ok)
	}
	pr, ok = EffectivePriceAt(settings.Load(), "relay", "sol", off)
	if !ok || pr.Input != 4 || pr.Output != 20 {
		t.Fatalf("off peak: %+v %v", pr, ok)
	}
	// EffectivePriceIn prices a call now, whatever that is: one of the two
	now, ok := EffectivePriceIn(settings.Load(), "relay", "sol")
	if !ok {
		t.Fatal("a price now is known")
	}
	if now.Input != 4 && now.Input != 12 {
		t.Fatalf("a price now is one of the window's: %+v", now)
	}
	// the base price and the windows apart, as the ledger's Pricer holds them
	base, ok, ws := BasePriceIn(settings.Load(), "relay", "sol")
	if !ok || base.Input != 4 || len(ws) != 1 || ws[0].Rate != 3 {
		t.Fatalf("the base price and the windows: %+v %v %+v", base, ok, ws)
	}
	// a rate of 1 changes nothing, and a model priced by the user is
	// scaled by the window all the same: the window is when the vendor
	// bills more, whatever the price is set from
	p.RateWindows = []RateWindow{{Start: "09:00", End: "12:00", Rate: 1}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePriceAt(settings.Load(), "relay", "sol", peak); pr.Input != 4 {
		t.Fatalf("a rate of 1: %+v", pr)
	}
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{"relay/sol": statePrice(3)}}); err != nil {
		t.Fatal(err)
	}
	p.RateWindows = []RateWindow{{Start: "09:00", End: "12:00", Rate: 3}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if pr, _ := EffectivePriceAt(settings.Load(), "relay", "sol", peak); pr.Input != 9 {
		t.Fatalf("a price the user set is scaled by the window too: %+v", pr)
	}
	if pr, _ := EffectivePriceAt(settings.Load(), "relay", "sol", off); pr.Input != 3 {
		t.Fatalf("a price the user set off peak: %+v", pr)
	}
}

// A signed-in account's windows survive its Save: the account branch of
// Save keeps only what it lists, and the windows are listed there — a
// plugin provider is an account, and the editor's Save would otherwise
// quietly drop what it shows, as it once did the account's PriceRate.
func TestAccountSaveKeepsTheRateWindows(t *testing.T) {
	signIn(t) // a codex account, so Save takes the account branch
	ws := []RateWindow{{Days: []int{1}, Start: "09:00", End: "18:00", Rate: 3}}
	if err := Save(Provider{ID: "codex", RateWindows: ws}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.RateWindows) != 1 || p.RateWindows[0].Rate != 3 {
		t.Fatalf("the account's windows are gone: %+v", p.RateWindows)
	}
}

// A template weighs a model by its price without the provider's rate
// windows (#2463): the hour a template is made in says nothing about how
// strong a model is, so a peak window never makes a relay's model read
// as a flagship.
func TestTemplateModelsLeaveTheRateWindowOut(t *testing.T) {
	priceHome(t)
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{"relay/obscure-lab-ultra": statePrice(5)}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Models:      []string{"obscure-lab-ultra"},
		RateWindows: []RateWindow{{Start: "00:00", End: "24:00", Rate: 3}}}); err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, r := range templateModels() {
		if r.e.Provider.ID == "relay" && r.e.Model == "obscure-lab-ultra" {
			seen = true
			if r.out != 5 {
				t.Errorf("the template weighs it at %v out, want the set 5, not the window's 3×", r.out)
			}
		}
	}
	if !seen {
		t.Fatal("the relay's model is not among the template's")
	}
}

// A zero time is a price asked with no call's time, a memo many calls
// share (the Requests tab's gateway blocks): no window holds it, however
// wide, so the memo is the base and each call's own time is read after.
func TestRateAtZeroTimeIsTheBase(t *testing.T) {
	all := []RateWindow{{Start: "00:00", End: "24:00", Rate: 3}}
	if r := RateAt(all, time.Time{}); r != 0 {
		t.Fatalf("zero time: %v, want 0", r)
	}
	if r := RateAt(all, time.Now()); r != 3 {
		t.Fatalf("now in an all-day window: %v, want 3", r)
	}
}

// The prices magpie writes into an agent's own model file (Pi's, omp's)
// leave a rate window out: nothing rewrites the file as a window opens or
// closes, so a price taken in one would stay on outside it. /v1/models,
// read at each request, prices now.
func TestAgentPricerLeavesTheRateWindowOut(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Models: []string{"sol"}, PriceRate: 2,
		RateWindows: []RateWindow{{Start: "00:00", End: "24:00", Rate: 3}}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	none := func(string) (Group, []Member, bool) { return Group{}, nil, false }
	e := Entry{ID: "relay/sol", Model: "sol", Provider: &p}
	if pr, ok := AgentPricer(settings.Load(), none)(e); !ok || pr.Input != 4 || pr.Output != 20 {
		t.Fatalf("an agent file's price under an all-day 3× window: %+v %v, want the 2× relay's 4 and 20", pr, ok)
	}
	if pr, ok := EntryPricer(settings.Load(), none)(e); !ok || pr.Input != 12 || pr.Output != 60 {
		t.Fatalf("/v1/models' price now: %+v %v, want 12 and 60", pr, ok)
	}
	// a group whose members differ is priced at its first member, base too
	q := Provider{ID: "other", Name: "Other", Chat: "https://other.example.com/v1", Key: "k", Models: []string{"sol"}}
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	group := func(string) (Group, []Member, bool) {
		return Group{}, []Member{{Provider: p, Model: "sol"}, {Provider: q, Model: "sol"}}, true
	}
	g := Entry{ID: "auto/sol", Model: "sol", Group: "auto", Provider: &p}
	if pr, ok := AgentPricer(settings.Load(), group)(g); !ok || pr.Input != 4 {
		t.Fatalf("a mixed group's first member in an agent file: %+v %v, want 4", pr, ok)
	}
}
