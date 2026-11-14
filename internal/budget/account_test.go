package budget

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A limit that follows a subscription account's window (ljayic on X)
// counts the key's calls from when that account's 5 hours began and
// starts again when the account's does, at the reset its reading tells.
// While the window can't be read it is unknown: the key is held to what
// it spent since the earliest the window can have begun, never reset or
// let through on a guess, and told to ask again in five minutes.
func TestAccountWindowLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Cleanup(Forget)
	now := time.Now().Round(0)
	set := now.AddDate(0, 0, -10) // the limit is set ten days ago
	access.Now = func() time.Time { return set }
	t.Cleanup(func() { access.Now = time.Now })

	// the account's 5 hours: begun two hours ago, ends in three
	type win struct {
		start, reset time.Time
		known        bool
	}
	w := win{now.Add(-2 * time.Hour), now.Add(3 * time.Hour), true}
	var asked []string
	t.Cleanup(provider.AccountWindowVia(func(account, name string, at time.Time) (time.Time, time.Time, bool) {
		asked = append(asked, account+" "+name)
		if account != "codex/me@example.com" || name != "5h" {
			return time.Time{}, time.Time{}, false
		}
		return w.start, w.reset, w.known
	}))

	if _, err := access.Update("add-key", access.Change{Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := access.List()
	k := ks[0]
	// an account limit names its account and window
	if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: "account", Account: "codex/me@example.com", Tokens: 1000}}); err == nil {
		t.Fatal("an account limit with no window was taken")
	}
	if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: "account", Account: "codex/me@example.com", AccountWindow: "5h", Tokens: 1000}}); err != nil {
		t.Fatal(err)
	}
	// the same account's week, and another account's 5 hours, are limits of their own
	for _, l := range []access.Limit{
		{Period: "account", Account: "codex/me@example.com", AccountWindow: "Weekly", Tokens: 100000},
		{Period: "account", Account: "claude/me@example.com", AccountWindow: "5h", Tokens: 100000},
	} {
		n := 1
		if l.Account == "claude/me@example.com" {
			n = 2
		}
		if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &l, Index: &n}); err != nil {
			t.Fatalf("%s %s: %v", l.Account, l.AccountWindow, err)
		}
	}
	n := 3
	if _, err := access.Update("limit-key", access.Change{Key: k.ID, Index: &n, Limit: &access.Limit{Period: "account", Account: "Codex/ME@example.com", AccountWindow: "5H", Tokens: 5}}); err == nil {
		t.Fatal("one account's window was taken twice")
	}
	ks, _ = access.List()
	k = ks[0]
	if len(k.Limits()) != 3 || !k.Limit.Since.Equal(set) {
		t.Fatalf("limits: %+v", k.Limits())
	}
	who := access.Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More}
	refused := func(at time.Time) *Refusal {
		t.Helper()
		Forget() // counted afresh from the ledger, as after a restart
		_, r := Reserve(who, -1, "", at)
		return r
	}

	// before the window: not counted
	Append(usage.Record{Time: now.Add(-3 * time.Hour), Provider: "p", Model: "m", Input: 900, Status: 200, CallerKeyID: k.ID})
	Append(usage.Record{Time: now.Add(-time.Hour), Provider: "p", Model: "m", Input: 400, Status: 200, CallerKeyID: k.ID})
	if r := refused(now); r != nil {
		t.Fatalf("a call before the account's window counted: %v", r)
	}
	st := All(k, now)[0]
	if st.Tokens != 400 || !st.Start.Equal(w.start) || !st.Reset.Equal(w.reset) || st.ResetUnknown || st.Account != "codex/me@example.com" || st.AccountWindow != "5h" {
		t.Fatalf("status: %+v", st)
	}
	Append(usage.Record{Time: now.Add(-30 * time.Minute), Provider: "p", Model: "m", Input: 700, Status: 200, CallerKeyID: k.ID})
	r := refused(now)
	if r == nil {
		t.Fatal("not refused with 1100 of 1000 used in the account's window")
	}
	if !strings.Contains(r.Error(), "5h window of codex/me@example.com") || !strings.Contains(r.Error(), "resets at") || !r.ResetAt().Equal(w.reset) {
		t.Errorf("refusal: %s", r.Error())
	}
	if got := r.RetryAfter(now); got < 3*3600-1 || got > 3*3600+1 {
		t.Errorf("retry after %ds", got)
	}

	// the reset passed, the next window not read yet: still held, from
	// the reset that passed, which is when the next can begin earliest
	after := w.reset.Add(time.Minute)
	Append(usage.Record{Time: w.reset.Add(30 * time.Second), Provider: "p", Model: "m", Input: 1000, Status: 200, CallerKeyID: k.ID})
	w = win{w.reset, time.Time{}, false}
	r = refused(after)
	if r == nil {
		t.Fatal("let through on a window not read")
	}
	if !r.ResetUnknown || !strings.Contains(r.Error(), "can't read") || r.RetryAfter(after) != 300 {
		t.Errorf("unknown: %+v %s retry %d", r.Status, r.Error(), r.RetryAfter(after))
	}
	if st := All(k, after)[0]; !st.ResetUnknown || !st.Start.Equal(w.start) || st.Tokens != 1000 {
		t.Errorf("unknown status: %+v", st)
	}
	// nothing read at all: counted from when the limit was set, every
	// call of ten days, and still held
	w = win{}
	if r := refused(after); r == nil || !r.ResetUnknown {
		t.Fatalf("nothing read: %v", r)
	}
	if st := All(k, after)[0]; !st.Start.Equal(set) || st.Tokens != 3000 {
		t.Errorf("nothing read: %+v", st)
	}
	// the new window is read: begun at the reset, so the last call counts,
	// the ones before don't; a new one, read a little later, begun after it
	w = win{after.Add(-time.Minute), after.Add(5*time.Hour - time.Minute), true}
	if st := All(k, after)[0]; st.ResetUnknown || st.Tokens != 1000 || !st.Reset.Equal(w.reset) {
		t.Errorf("read again: %+v", st)
	}
	w = win{after.Add(-10 * time.Second), after.Add(5 * time.Hour), true}
	if r := refused(after); r != nil {
		t.Fatalf("held in a new window: %v", r)
	}
	if !slices.Contains(asked, "codex/me@example.com Weekly") || !slices.Contains(asked, "claude/me@example.com 5h") {
		t.Errorf("windows asked: %v", asked)
	}
}
