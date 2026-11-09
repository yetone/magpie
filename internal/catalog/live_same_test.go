package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// A list fetched again as it was (an account's, each 45 minutes) moves only
// when it was fetched: the agents aren't told (Changed), as each time they
// were every agent's lists were built and compared again for nothing. A
// list that differs in anything else — a model, a part, what it was asked
// as — tells them. The held catalog isn't dropped either (Forget moves the
// stamp dsh's rounds build magpie's models again by); the file's next
// read, under a request's hold too, is the new one.
func TestLiveSavedAgainSameTellsNoAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	changed, forgot := 0, 0
	oldC, oldF := Changed, Forget
	Changed, Forget = func() { changed++ }, func() { forgot++ }
	defer func() { Changed, Forget = oldC, oldF }()

	a := []Model{{ID: "gpt-a", Context: 200000}, {ID: "gpt-b"}}
	save := func(ms []Model, asked string) {
		t.Helper()
		if err := SaveLiveAsked("acct", "https://a/v1", asked, ms); err != nil {
			t.Fatal(err)
		}
	}
	save(a, "1.0")
	if changed != 1 || forgot != 1 {
		t.Fatalf("first save: changed %d, forgot %d", changed, forgot)
	}
	// read as a list saved a while ago is: taken as unchanged under the
	// hold, until it is looked at again
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(LivePath("acct"), old, old); err != nil {
		t.Fatal(err)
	}
	release := filememo.Hold()
	defer release()
	first, _ := readLive("acct")
	time.Sleep(2 * time.Millisecond)
	save([]Model{{ID: "gpt-a", Context: 200000}, {ID: "gpt-b"}}, "1.0")
	if changed != 1 || forgot != 1 {
		t.Errorf("the same list again: changed %d, forgot %d; want 1, 1", changed, forgot)
	}
	if again, _ := readLive("acct"); !again.Fetched.After(first.Fetched) {
		t.Errorf("fetched %v, not after %v", again.Fetched, first.Fetched)
	}
	for _, c := range []struct {
		name  string
		ms    []Model
		asked string
	}{
		{"a model's window", []Model{{ID: "gpt-a", Context: 400000}, {ID: "gpt-b"}}, "1.0"},
		{"a model more", []Model{{ID: "gpt-a", Context: 400000}, {ID: "gpt-b"}, {ID: "gpt-c"}}, "1.0"},
		{"asked as another", []Model{{ID: "gpt-a", Context: 400000}, {ID: "gpt-b"}, {ID: "gpt-c"}}, "2.0"},
	} {
		before := changed
		save(c.ms, c.asked)
		if changed != before+1 {
			t.Errorf("%s: agents not told", c.name)
		}
	}
	before := changed
	if err := SaveLiveSides("acct", "https://a/v1", a, map[string][]Model{"https://a/v1": a[:1]}); err != nil {
		t.Fatal(err)
	}
	if err := SaveLiveSides("acct", "https://a/v1", a, map[string][]Model{"https://a/v1": a}); err != nil {
		t.Fatal(err)
	}
	if changed != before+2 {
		t.Errorf("a part changed: told %d times, want 2", changed-before)
	}
}
