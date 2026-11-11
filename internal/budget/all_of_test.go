package budget

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// a big ledger: perDay calls a day for days days over keys keys, each key
// holding a day, a week and a month limit
func bigLedger(tb testing.TB, days, perDay, keys int) []access.Key {
	home := tb.TempDir()
	tb.Setenv("HOME", home)
	tb.Setenv("USERPROFILE", home)
	tb.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	tb.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	Forget()
	var ks []access.Key
	for i := 0; i < keys; i++ {
		name := fmt.Sprintf("k%d", i)
		if _, err := access.Update("add-key", access.Change{Name: name}); err != nil {
			tb.Fatal(err)
		}
	}
	list, _ := access.List()
	for _, k := range list {
		for _, p := range []string{"day", "week", "month"} {
			if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: p, Tokens: 1 << 40}}); err != nil {
				tb.Fatal(err)
			}
		}
	}
	list, _ = access.List()
	ks = list
	now := time.Now()
	for d := 0; d < days; d++ {
		for i := 0; i < perDay; i++ {
			at := now.Add(-time.Duration(d)*24*time.Hour - time.Duration(i)*time.Second*86400/time.Duration(perDay))
			usage.Append(usage.Record{Time: at, Provider: "p", Model: "m", Input: 1000, Output: 100, Status: 200, CallerKeyID: ks[i%len(ks)].ID})
		}
	}
	return ks
}

func BenchmarkAll(b *testing.B) {
	ks := bigLedger(b, 31, 12000, 8)
	All(ks[0], time.Now()) // the log's first parse is the ledger's, not this read's
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := time.Now()
		for _, k := range ks {
			All(k, now)
		}
	}
}

// AllOf counts what read counts for each window of each key, from one pass
// and from what the last call kept: keys with several limits, a scoped
// limit, a key issued under another, the log growing past full blocks
// between calls, and the log rewritten.
func TestAllOfIsWhatEachWindowRead(t *testing.T) {
	ks := bigLedger(t, 3, 700, 3)
	if _, err := access.Update("limit-key", access.Change{Key: ks[0].ID, Limit: &access.Limit{Period: "day", Tokens: 5000, Models: []string{"p/m"}}}); err != nil {
		t.Fatal(err)
	}
	ks, _ = access.List()
	now := time.Now()
	same := func(when string) int {
		t.Helper()
		all := AllOf(ks, now)
		if len(all) != len(ks) {
			t.Fatalf("%s: %d rows for %d keys", when, len(all), len(ks))
		}
		n := 0
		for i, k := range ks {
			ls := k.Limits()
			if len(all[i]) != len(ls) {
				t.Fatalf("%s: key %d: %d statuses for %d limits", when, i, len(all[i]), len(ls))
			}
			for j, l := range ls {
				start, _ := l.Window(now)
				want := read(k.ID, start, l)
				got := all[i][j]
				if got.Calls != want.calls || got.Tokens != want.tokens(l) || got.Cost != want.cost {
					t.Fatalf("%s: key %d limit %d (%s): got %d calls %d tokens, read %d calls %d tokens", when, i, j, l.Period, got.Calls, got.Tokens, want.calls, want.tokens(l))
				}
				n += got.Calls
			}
		}
		return n
	}
	if same("first") == 0 {
		t.Fatal("no call was counted at all")
	}
	same("again")
	for round := 0; round < 3; round++ {
		for i := 0; i < 400+round*300; i++ {
			rec := usage.Record{Time: now.Add(-time.Duration(i) * time.Second), Provider: "p", Model: "m", Input: 10, Status: 200, CallerKeyID: ks[i%len(ks)].ID}
			if i%5 == 0 {
				rec.CallerKeyID, rec.CallerIssuerID = "child", ks[1].ID
			}
			Append(rec)
		}
		same(fmt.Sprintf("grown %d", round))
	}
	// rewritten: the log is another file's
	if err := os.WriteFile(usage.Path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n := same("cleared"); n != 0 {
		t.Fatalf("a cleared log counted %d calls", n)
	}
	if one := All(ks[1], now); len(one) != len(ks[1].Limits()) {
		t.Fatalf("All: %+v", one)
	}
}

// BenchmarkReadEach is how All answered before AllOf: the ledger read
// again for each window of each key.
func BenchmarkReadEach(b *testing.B) {
	ks := bigLedger(b, 31, 12000, 8)
	All(ks[0], time.Now()) // the log's first parse is the ledger's, not this read's
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := time.Now()
		for _, k := range ks {
			for _, l := range k.Limits() {
				start, _ := l.Window(now)
				read(k.ID, start, l)
			}
		}
	}
}

// A window AllOf kept is another limit's when that limit started in the
// same second: a 10-day limit reset, then made a 5-hour one a millisecond
// later (TestGatewayKeyLimitCLI). The 5-hour window starts at its own
// Since, not the 10-day one's.
func TestAllOfKeptWindowIsOfItsOwnStart(t *testing.T) {
	bigLedger(t, 0, 0, 0)
	t0 := time.Unix(time.Now().Unix(), 100_000_000)
	ten := access.Key{ID: "k", Limit: &access.Limit{Period: "days", Days: 10, Cost: 800, Since: t0}}
	if st := All(ten, t0.Add(time.Minute)); len(st) != 1 || !st[0].Start.Equal(t0) {
		t.Fatalf("10-day: %+v", st)
	}
	t1 := t0.Add(1700 * time.Microsecond)
	five := access.Key{ID: "k", Limit: &access.Limit{Period: "hours", Hours: 5, Tokens: 200_000, Since: t1}}
	st := All(five, t1.Add(time.Minute))
	if len(st) != 1 || !st[0].Start.Equal(t1) || st[0].Reset.Sub(st[0].Start) != 5*time.Hour {
		t.Fatalf("5-hour after a 10-day in the same second: %+v", st)
	}
}
