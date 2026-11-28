package budget

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// A call inside one of its provider's rate windows (#2463) costs its rate
// against a gateway key's cost limit, so the same tokens spend the cap in
// fewer calls than outside the window: the records are priced at their
// own time, by the same Pricer the Usage page prices with.
func TestCostLimitCountsTheRateWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	// $1000 a million input: 300 tokens of input a call costs $0.30
	// outside the window, $0.90 inside it
	in, zero := 1000.0, 0.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"plan/m1": {Input: &in, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1",
		RateWindows: []provider.RateWindow{{Start: "10:00", End: "14:00", Rate: 3}}}); err != nil {
		t.Fatal(err)
	}
	newKey := func(name string) access.Key {
		t.Helper()
		if _, err := access.Update("add-key", access.Change{Name: name}); err != nil {
			t.Fatal(err)
		}
		ks, err := access.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range ks {
			if k.Name == name {
				if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: "day", Cost: 1.0}}); err != nil {
					t.Fatal(err)
				}
				ks, err := access.List()
				if err != nil {
					t.Fatal(err)
				}
				for _, k := range ks {
					if k.Name == name {
						return k
					}
				}
			}
		}
		t.Fatal("the key is not there")
		return access.Key{}
	}
	day := time.Now()
	at := func(h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, time.Local)
	}
	spent := func(k access.Key, when time.Time) bool {
		t.Helper()
		who := access.Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More}
		_, refused := Reserve(who, -1, "", when)
		return refused != nil
	}
	call := func(k access.Key, h, m int) {
		t.Helper()
		Append(usage.Record{Time: at(h, m), Provider: "plan", Model: "m1", Input: 300, Status: 200, CallerKeyID: k.ID})
	}

	// two calls at the peak's 3×, $0.90 each: $1.80 of a $1 cap, the next
	// is out; the same two off the peak are $0.60 and leave room
	peak := newKey("Peak")
	call(peak, 11, 0)
	call(peak, 11, 30)
	if !spent(peak, at(11, 40)) {
		t.Fatal("a key over $1 of peak calls is let in")
	}
	off := newKey("Off")
	call(off, 15, 0)
	call(off, 15, 10)
	if spent(off, at(15, 20)) {
		t.Fatal("two off-peak calls spent a $1 cap")
	}
	call(off, 15, 30)
	if spent(off, at(15, 40)) {
		t.Fatal("three off-peak calls, $0.90 of a $1 cap, are spent")
	}
	call(off, 15, 50)
	if !spent(off, at(16, 0)) {
		t.Fatal("four off-peak calls, $1.20 of a $1 cap, are let in")
	}
}

// A cost limit without CacheReads still prices an in-flight body's reservation
// over every token spent in the window (cache reads included): the body holds
// the full cached context, and s.cost includes cache-read cost, so dividing by
// uncached tokens alone over-reserved ~30x and refused parallel calls far
// below the cap (#7803). Token limits with CacheReads off still leave cache
// reads out.
func TestCostReservationIncludesCacheReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	Forget()
	t.Cleanup(func() { catalog.Reset(); Forget() })

	// $10/M input, $30/M output, $0.50/M cache read: one call with 4k input,
	// 1k output, and 195k cache read (200k total tokens) costs
	// $0.04 + $0.03 + $0.0975 = $0.1675 -> $0.8375/M over all tokens, vs
	// $33.50/M over uncached tokens alone (40x higher).
	in, out, cr, cw := 10.0, 30.0, 0.5, 10.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/m1": {Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	const bodyBytes = 800_000 // est = 200_000 + mean output (1_000) = 201_000

	for _, cacheReads := range []bool{false, true} {
		name := "cacheReads=off"
		if cacheReads {
			name = "cacheReads=on"
		}
		t.Run(name, func(t *testing.T) {
			keyID := "k-" + name
			lim := &access.Limit{Period: "week", Cost: 10.0, CacheReads: cacheReads}
			who := access.Identity{KeyID: keyID, KeyName: name, Limit: lim}
			// 10 historical calls: $1.675 spent of $10.00 cap ($8.325 left).
			for i := range 10 {
				Append(usage.Record{
					Time:        now.Add(-time.Duration(i+1) * time.Minute),
					Provider:    "relay",
					Model:       "m1",
					Input:       4_000,
					Output:      1_000,
					CacheRead:   195_000,
					Status:      200,
					CallerKeyID: keyID,
				})
			}

			// Under the old bug with CacheReads=false, one 800 KB request held
			// 201_000 * $1.675 / 50_000 = $6.7335, so a second parallel call
			// ($1.675 + $6.7335 + $6.7335 > $10) was refused at 17% of the cap.
			// With cache reads in the denominator, each holds
			// 201_000 * $1.675 / 2_000_000 = $0.1683375, so 4 parallel calls
			// easily fit.
			var releases []func()
			t.Cleanup(func() {
				for _, rel := range releases {
					rel()
				}
			})
			for i := range 4 {
				rel, refused := Reserve(who, bodyBytes, "relay/m1", now)
				if refused != nil {
					t.Fatalf("parallel call %d refused far below cap: %v", i+1, refused)
				}
				releases = append(releases, rel)
			}
			st := Of(access.Key{ID: keyID, Name: name, Limit: lim}, now)
			wantOne := 201_000.0 * 1.675 / 2_000_000.0
			wantHeld := 4 * wantOne
			if math.Abs(st.ReservedCost-wantHeld) > 1e-6 {
				t.Fatalf("ReservedCost = %.6f, want %.6f (per call %.6f)", st.ReservedCost, wantHeld, wantOne)
			}
		})
	}

	t.Run("heavily cached history does not inflate cost reservation", func(t *testing.T) {
		keyID := "k-heavy-cr"
		lim := &access.Limit{Period: "week", Cost: 10.0, CacheReads: false}
		who := access.Identity{KeyID: keyID, KeyName: "HeavyCR", Limit: lim}
		Append(usage.Record{
			Time:        now.Add(-time.Minute),
			Provider:    "relay",
			Model:       "m1",
			Output:      100,     // $0.003
			CacheRead:   999_900, // $0.49995 -> $0.50295 total over 1_000_000 all tokens
			Status:      200,
			CallerKeyID: keyID,
		})
		rel1, refused := Reserve(who, 400_000, "relay/m1", now) // est = 100_000 + 100 = 100_100
		if refused != nil {
			t.Fatalf("first call refused: %v", refused)
		}
		defer rel1()
		// Under the old bug, rel1 reserved 100_100 * $0.50295 / 100 = $503.45,
		// so a second parallel call was refused immediately.
		rel2, refused := Reserve(who, 400_000, "relay/m1", now)
		if refused != nil {
			t.Fatalf("second parallel call refused: %v", refused)
		}
		defer rel2()
		st := Of(access.Key{ID: keyID, Name: "HeavyCR", Limit: lim}, now)
		wantCost := 100*out/1e6 + 999_900*cr/1e6
		wantHeld := 2 * (100_100.0 * wantCost / 1_000_000.0)
		if math.Abs(st.ReservedCost-wantHeld) > 1e-6 {
			t.Fatalf("ReservedCost = %.6f, want %.6f", st.ReservedCost, wantHeld)
		}
	})

	t.Run("token limit with CacheReads off still excludes cache reads", func(t *testing.T) {
		keyID := "k-tok-cap"
		lim := &access.Limit{Period: "week", Tokens: 100_000, Cost: 100.0, CacheReads: false}
		who := access.Identity{KeyID: keyID, KeyName: "TokCap", Limit: lim}
		Append(usage.Record{
			Time:        now.Add(-time.Minute),
			Provider:    "relay",
			Model:       "m1",
			Input:       1_000,
			Output:      500,
			CacheRead:   500_000, // exceeds Tokens cap if mistakenly counted
			Status:      200,
			CallerKeyID: keyID,
		})
		rel, refused := Reserve(who, 4_000, "relay/m1", now)
		if refused != nil {
			t.Fatalf("cache reads should not exhaust token cap when CacheReads is false: %v", refused)
		}
		defer rel()
		st := Of(access.Key{ID: keyID, Name: "TokCap", Limit: lim}, now)
		if st.Tokens != 1_500 {
			t.Fatalf("Status.Tokens = %d, want 1500", st.Tokens)
		}
	})
}
