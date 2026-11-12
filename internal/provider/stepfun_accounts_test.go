package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// stepfunHome is a sandbox with the given providers saved.
func stepfunHome(t *testing.T, ps ...Provider) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forgetPlanQuotas()
	if err := store(file{Providers: ps}); err != nil {
		t.Fatal(err)
	}
}

func stepfunOn(id, name, site string) Provider {
	return Provider{ID: id, Name: name, Icon: "stepfun", Chat: "https://api.stepfun." + site + "/step_plan/v1"}
}

// stepfunFile is stepfun-auth.json as written.
func stepfunFile(t *testing.T) (map[string]stepfunSession, []byte) {
	t.Helper()
	b, err := os.ReadFile(stepfunPath())
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]stepfunSession{}
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return all, b
}

func writeStepFunFile(t *testing.T, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(stepfunPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stepfunPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func keysOf(all map[string]stepfunSession) []string {
	var out []string
	for k := range all {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// fiveHoursUsed is what a card's 5-hour window has used, or -1.
func fiveHoursUsed(cards []SubscriptionQuota, id string) float64 {
	for _, q := range cards {
		if q.Provider == id {
			for _, w := range q.Windows {
				if w.Name == "5 hours" {
					return w.Used
				}
			}
		}
	}
	return -1
}

// Two StepFun providers on one site signed in as two accounts each show
// their own account's windows, and signing one out, or removing it, leaves
// the other signed in (#2384: one session per site showed both the same
// windows, and one sign-out signed both out).
func TestStepFunTwoAccountsOneSite(t *testing.T) {
	stepfunHome(t, stepfunOn("stepfun", "StepFun", "ai"), stepfunOn("stepfun-work", "StepFun Work", "ai"))
	f := newFakeStepFun(t, "ai")
	ctx := context.Background()

	acc, ref := stepfunTokens("4161", time.Now().Add(time.Hour), true)
	f.access.Store(acc)
	if err := SaveStepFunSession(ctx, "stepfun", "ai", acc, ref, "web-1"); err != nil {
		t.Fatal(err)
	}
	if StepFunSignedIn("stepfun-work") {
		t.Fatal("one provider's sign-in signed in the other on its site")
	}
	work, workRef := stepfunTokens("5555", time.Now().Add(time.Hour), true)
	f.others.Store(work, `{"five_hour_usage_left_rate":0.1,"five_hour_usage_reset_time":"1790680000"}`)
	if err := SaveStepFunSession(ctx, "stepfun-work", "ai", work, workRef, "web-1"); err != nil {
		t.Fatal(err)
	}

	cards := stepPlanQuotas(ctx)
	if len(cards) != 2 {
		t.Fatalf("%d cards, want one per provider: %+v", len(cards), cards)
	}
	if a, b := fiveHoursUsed(cards, "stepfun"), fiveHoursUsed(cards, "stepfun-work"); a < 39.9 || a > 40.1 || b < 89.9 || b > 90.1 {
		t.Fatalf("5 hours used: stepfun %v (want 40), stepfun-work %v (want 90)", a, b)
	}

	if err := SignOutStepFun("stepfun"); err != nil {
		t.Fatal(err)
	}
	if StepFunSignedIn("stepfun") || !StepFunSignedIn("stepfun-work") {
		t.Fatalf("after signing stepfun out: stepfun %v, stepfun-work %v", StepFunSignedIn("stepfun"), StepFunSignedIn("stepfun-work"))
	}
	if cards := stepPlanQuotas(ctx); len(cards) != 1 || fiveHoursUsed(cards, "stepfun-work") < 89.9 {
		t.Fatalf("after a sign-out: %+v", cards)
	}

	// removed, its sign-in goes with it: not one a provider added later
	// under the same id would have
	if err := SaveStepFunSession(ctx, "stepfun", "ai", acc, ref, "web-1"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("stepfun-work"); err != nil {
		t.Fatal(err)
	}
	if all, _ := stepfunFile(t); !slices.Equal(keysOf(all), []string{"stepfun"}) {
		t.Fatalf("after removing stepfun-work: %v", keysOf(all))
	}
	// renamed, it keeps its sign-in
	if err := Rename("stepfun", "mine"); err != nil {
		t.Fatal(err)
	}
	if all, _ := stepfunFile(t); !slices.Equal(keysOf(all), []string{"mine"}) || all["mine"].Account != "4161" {
		t.Fatalf("after renaming stepfun: %+v", all)
	}
}

// A stepfun-auth.json from before #2384 kept one session per site. Read,
// it is given to the StepFun providers on that site, so nobody signs in
// again: with one, it is that one's; with several, each has it (and the
// copies are renewed as one sign-in) until another account is brought to
// one of them. With no provider on the site yet, or providers.json
// unreadable, it is left as it is.
func TestStepFunOldSessionMoved(t *testing.T) {
	acc, ref := stepfunTokens("4161", time.Now().Add(time.Hour), true)
	old := stepfunSession{Access: acc, Refresh: ref, WebID: "web-1", Account: "4161"}
	oldFile, _ := json.Marshal(map[string]stepfunSession{"ai": old})

	t.Run("one provider", func(t *testing.T) {
		stepfunHome(t, stepfunOn("stepfun", "StepFun", "ai"), stepfunOn("stepfun-cn", "StepFun (China)", "com"))
		writeStepFunFile(t, oldFile)
		if !StepFunSignedIn("stepfun") || StepFunSignedIn("stepfun-cn") {
			t.Fatalf("signed in: stepfun %v, stepfun-cn %v", StepFunSignedIn("stepfun"), StepFunSignedIn("stepfun-cn"))
		}
		all, _ := stepfunFile(t)
		if !slices.Equal(keysOf(all), []string{"stepfun"}) || all["stepfun"] != old {
			t.Fatalf("written back as %+v", all)
		}
		if fi, err := os.Stat(stepfunPath()); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Fatalf("written back as %v %v", fi, err)
		}
	})

	t.Run("two providers", func(t *testing.T) {
		stepfunHome(t, stepfunOn("stepfun", "StepFun", "ai"), stepfunOn("stepfun-work", "StepFun Work", "ai"))
		f := newFakeStepFun(t, "ai")
		ctx := context.Background()
		// three minutes left: the first card asked renews it
		soon, soonRef := stepfunTokens("4161", time.Now().Add(3*time.Minute), true)
		f.access.Store(soon)
		b, _ := json.Marshal(map[string]stepfunSession{"ai": {Access: soon, Refresh: soonRef, WebID: "web-1", Account: "4161"}})
		writeStepFunFile(t, b)
		if !StepFunSignedIn("stepfun") || !StepFunSignedIn("stepfun-work") {
			t.Fatal("a provider on the site lost its windows")
		}
		if all, _ := stepfunFile(t); !slices.Equal(keysOf(all), []string{"stepfun", "stepfun-work"}) {
			t.Fatalf("written back as %v", keysOf(all))
		}
		cards := stepPlanQuotas(ctx)
		if len(cards) != 2 || cards[0].Error != "" || cards[1].Error != "" {
			t.Fatalf("cards: %+v", cards)
		}
		if n := f.renewed.Load(); n != 1 {
			t.Fatalf("renewed %d times, want once for the one sign-in both hold", n)
		}
		if all, _ := stepfunFile(t); all["stepfun"].Access != f.access.Load().(string) || all["stepfun-work"] != all["stepfun"] {
			t.Fatalf("the copies weren't renewed together: %+v", all)
		}

		// the other account brought to one of them: theirs alone from then
		work, workRef := stepfunTokens("5555", time.Now().Add(time.Hour), true)
		f.others.Store(work, `{"five_hour_usage_left_rate":0.1,"five_hour_usage_reset_time":"1790680000"}`)
		if err := SaveStepFunSession(ctx, "stepfun-work", "ai", work, workRef, "web-1"); err != nil {
			t.Fatal(err)
		}
		if err := SignOutStepFun("stepfun"); err != nil {
			t.Fatal(err)
		}
		if all, _ := stepfunFile(t); !slices.Equal(keysOf(all), []string{"stepfun-work"}) || all["stepfun-work"].Account != "5555" {
			t.Fatalf("after re-signing one and signing out the other: %+v", all)
		}
	})

	t.Run("no provider on its site", func(t *testing.T) {
		stepfunHome(t, stepfunOn("stepfun-cn", "StepFun (China)", "com"))
		writeStepFunFile(t, oldFile)
		if StepFunSignedIn("stepfun-cn") {
			t.Fatal("another site's session was given")
		}
		if _, b := stepfunFile(t); string(b) != string(oldFile) {
			t.Fatalf("written over: %s", b)
		}
		// added later, the provider on its site takes it
		if err := store(file{Providers: []Provider{stepfunOn("stepfun-cn", "StepFun (China)", "com"), stepfunOn("stepfun", "StepFun", "ai")}}); err != nil {
			t.Fatal(err)
		}
		if !StepFunSignedIn("stepfun") {
			t.Fatal("the provider added on its site didn't take it")
		}
	})

	t.Run("providers.json unreadable", func(t *testing.T) {
		stepfunHome(t)
		if err := os.WriteFile(Path(), []byte(`{"providers": [`), 0o600); err != nil {
			t.Fatal(err)
		}
		writeStepFunFile(t, oldFile)
		if StepFunSignedIn("stepfun") {
			t.Fatal("given to a provider no providers.json said there was")
		}
		if _, b := stepfunFile(t); string(b) != string(oldFile) {
			t.Fatalf("written over on an unread providers.json: %s", b)
		}
	})
}

// A stepfun-auth.json that doesn't parse is unknown, not no sign-in: read,
// signed out of, a provider removed, nothing writes over it; a sign-in
// that does write keeps it aside first.
func TestStepFunUnparsedKept(t *testing.T) {
	stepfunHome(t, stepfunOn("stepfun", "StepFun", "ai"), stepfunOn("stepfun-work", "StepFun Work", "ai"))
	f := newFakeStepFun(t, "ai")
	ctx := context.Background()
	// valid JSON with one entry of another shape: none of it is read, not
	// the old site's session either, so it isn't moved and written over
	bad := []byte(`{"ai": {"access": "a.b.c", "refresh": "a.b.c", "webId": "web-1", "account": "4161"}, "other": 5}`)
	writeStepFunFile(t, bad)

	if StepFunSignedIn("stepfun") {
		t.Fatal("signed in from a file that doesn't parse")
	}
	if cards := stepPlanQuotas(ctx); len(cards) != 0 {
		t.Fatalf("cards: %+v", cards)
	}
	if err := SignOutStepFun("stepfun"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("stepfun-work"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stepfunPath()); string(b) != string(bad) {
		t.Fatalf("written over: %s", b)
	}

	acc, ref := stepfunTokens("4161", time.Now().Add(time.Hour), true)
	f.access.Store(acc)
	if err := SaveStepFunSession(ctx, "stepfun", "ai", acc, ref, "web-1"); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(stepfunPath() + ".bad-*")
	if len(kept) != 1 {
		t.Fatalf("kept aside: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != string(bad) {
		t.Fatalf("kept aside as %s", b)
	}
}
