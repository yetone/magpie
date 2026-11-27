package budget

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// A try that failed and handed its request on before its vendor billed
// anything (a group member's 429, now a row of its own: Dazzle_sys on X)
// is no call of the key's and no call whose price is missing, whether the
// window is read from the log or counted as the call is written.
func TestTriedOnlyIsNoCallOfTheKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	Forget()
	t.Cleanup(Forget)
	// a total limit set an hour ago: no midnight falls inside its window
	now := time.Now().Round(0)
	access.Now = func() time.Time { return now.Add(-time.Hour) }
	t.Cleanup(func() { access.Now = time.Now })
	if _, err := access.Update("add-key", access.Change{Name: "K"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := access.List()
	if len(ks) != 1 {
		t.Fatalf("keys %+v", ks)
	}
	if _, err := access.Update("limit-key", access.Change{Key: ks[0].ID, Limit: &access.Limit{Period: "total", Tokens: 1000}}); err != nil {
		t.Fatal(err)
	}
	ks, _ = access.List()
	k := ks[0]
	request := func(at time.Time) {
		Append(usage.Record{Time: at, Provider: "p", Model: "unpriced-a", Status: 429, Error: "rate limited",
			RouteID: at.UnixNano(), HandedOn: true, CallerKeyID: k.ID})
		Append(usage.Record{Time: at.Add(time.Second), Provider: "q", Model: "unpriced-b", Input: 10, Status: 200,
			RouteID: at.UnixNano(), Tries: 2, CallerKeyID: k.ID})
	}
	request(now.Add(-2 * time.Minute))
	Forget()
	if st := Of(k, now); st == nil || st.Calls != 1 || st.Unpriced != 1 || st.Tokens != 10 {
		t.Fatalf("read from the log: %+v", st)
	}
	// the window is kept now; the next request is counted as it is written
	request(now.Add(-time.Minute))
	if st := Of(k, now); st == nil || st.Calls != 2 || st.Unpriced != 2 || st.Tokens != 20 {
		t.Fatalf("counted as written: %+v", st)
	}
}
