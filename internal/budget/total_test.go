package budget

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// A "total" limit (@Cyrus_4433 on X) never resets by the clock: what a
// key spent 100 days ago still counts against it, where a month limit has
// forgotten it, and only the key's Reset usage starts it again.
func TestTotalLimitNeverResetsByThePeriod(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	now := time.Now().Round(0)
	long := now.AddDate(0, 0, -100)
	// the limits are set 120 days ago, so a total one counts from then
	access.Now = func() time.Time { return now.AddDate(0, 0, -120) }
	t.Cleanup(func() { access.Now = time.Now })
	newKey := func(name, period string) access.Key {
		t.Helper()
		if _, err := access.Update("add-key", access.Change{Name: name}); err != nil {
			t.Fatal(err)
		}
		ks, _ := access.List()
		for _, k := range ks {
			if k.Name == name {
				if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: period, Tokens: 1000}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		ks, _ = access.List()
		for _, k := range ks {
			if k.Name == name {
				return k
			}
		}
		t.Fatal("no key")
		return access.Key{}
	}
	refused := func(k access.Key) *Refusal {
		t.Helper()
		who := access.Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More}
		_, r := Reserve(who, -1, "", now)
		return r
	}
	for _, name := range []string{"Total", "Month"} {
		period := strings.ToLower(name)
		k := newKey(name, period)
		Append(usage.Record{Time: long, Provider: "p", Model: "m", Input: 1000, Status: 200, CallerKeyID: k.ID})
		r := refused(k)
		if period == "month" && r != nil {
			t.Fatalf("a month limit still counts a call of 100 days ago: %v", r)
		}
		if period == "total" {
			if r == nil {
				t.Fatal("a total limit forgot a call of 100 days ago")
			}
			if !strings.Contains(r.Error(), "total limit") || strings.Contains(r.Error(), "resets at") {
				t.Errorf("refusal: %s", r.Error())
			}
			if r.RetryAfter(now) < 365*24*3600 {
				t.Errorf("retry after %ds", r.RetryAfter(now))
			}
			// Reset usage counts it again from now
			access.Now = func() time.Time { return now.Add(-time.Minute) }
			if _, err := access.Update("reset-limit-key", access.Change{Key: k.ID}); err != nil {
				t.Fatal(err)
			}
			ks, _ := access.List()
			for _, x := range ks {
				if x.ID == k.ID {
					k = x
				}
			}
			if r := refused(k); r != nil {
				t.Fatalf("still refused after Reset usage: %v", r)
			}
		}
	}
}
