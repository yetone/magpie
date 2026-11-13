package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/yetone/magpie/internal/sessions"
)

// Codex's effort before the last days is kept in effort-v1.json: a start
// pairs only the recent chunks, and tells the same effort a pairing of
// every chunk does. A settled file changed, or no file, pairs them all.
func TestCodexEffortSettledFromDisk(t *testing.T) {
	pairAtOnce(t)
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	write := func(id string, at time.Time, input int) string {
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, input, 10)
		return path
	}
	var settled string
	for i, days := range []int{40, 20, 10, 5, 0} {
		id := string(rune('a' + i))
		at := now.AddDate(0, 0, -days).Add(-time.Hour)
		path := write(id, at, 100+i)
		if days == 20 {
			settled = path
		}
		Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	paired := []int{}
	effortPairing = func(n int) { paired = append(paired, n) }
	t.Cleanup(func() { effortPairing = nil })
	restart := func() {
		codexEffort.Lock()
		codexEffort.periods = map[Period]*effortEntry{}
		codexEffort.Unlock()
		codexEffortBuilding.Lock()
		effortOlds = map[Period]*effortOld{}
		codexEffortBuilding.Unlock()
		requestCache.Lock()
		requestCache.pairs = nil
		requestCache.Unlock()
	}
	restart()
	t.Cleanup(restart)
	same := func(a, b map[string][]sessions.ModelEffort) bool {
		if len(a) != len(b) {
			return false
		}
		for k, as := range a {
			bs := b[k]
			if len(as) != len(bs) {
				return false
			}
			for i := range as {
				x, y := as[i], bs[i]
				if math.Abs(x.Cost-y.Cost) > 1e-9 {
					return false
				}
				x.Cost, y.Cost = 0, 0
				if x != y {
					return false
				}
			}
		}
		return true
	}
	first := CodexModelEffort(All)
	if len(first) != 5 || len(paired) != 1 {
		t.Fatalf("first build: %d threads, pairings %v", len(first), paired)
	}
	all := paired[0]
	if _, err := os.Stat(effortDiskPath()); err != nil {
		t.Fatal(err)
	}
	restart()
	if again := CodexModelEffort(All); paired[len(paired)-1] >= all || !same(again, first) {
		t.Fatalf("started again, paired %d of %d: %+v, was %+v", paired[len(paired)-1], all, again, first)
	}
	writeCodexCall(t, settled, "b", "sb", `"vscode"`, "resp-b", "m", "low", now.AddDate(0, 0, -20).Add(-time.Hour), 1001, 10)
	restart()
	changed := CodexModelEffort(All)
	if paired[len(paired)-1] < all {
		t.Fatalf("a settled file changed, paired %d of %d", paired[len(paired)-1], all)
	}
	if got, _ := findModelEffort(changed["b"], "m", "low"); got.Input == first["b"][0].Input {
		t.Fatalf("the changed file's thread is %+v, was %+v", changed["b"], first["b"])
	}
	restart()
	os.Remove(effortDiskPath())
	if cold := CodexModelEffort(All); !same(cold, changed) {
		t.Fatalf("after a settled file changed %+v, cold %+v", changed, cold)
	}
}

// The Sessions list's All and the overview's period are each kept: one
// entry let the two write over each other, and every start paired one of
// them whole again (#2278).
func TestCodexEffortPeriodsKeptApart(t *testing.T) {
	pairAtOnce(t)
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	for i, days := range []int{40, 20, 10, 5, 0} {
		id := string(rune('a' + i))
		at := now.AddDate(0, 0, -days).Add(-time.Hour)
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", "low", at, 100+i, 10)
		Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	paired := []int{}
	effortPairing = func(n int) { paired = append(paired, n) }
	t.Cleanup(func() { effortPairing = nil })
	restart := func() {
		codexEffort.Lock()
		codexEffort.periods = map[Period]*effortEntry{}
		codexEffort.Unlock()
		codexEffortBuilding.Lock()
		effortOlds = map[Period]*effortOld{}
		codexEffortBuilding.Unlock()
		requestCache.Lock()
		requestCache.pairs = nil
		requestCache.Unlock()
	}
	restart()
	t.Cleanup(restart)
	first := map[Period]int{}
	for _, p := range []Period{All, Month} {
		got := CodexModelEffort(p)
		if len(got) == 0 || len(paired) == 0 {
			t.Fatalf("%s: %d threads, pairings %v", p, len(got), paired)
		}
		first[p] = paired[len(paired)-1]
	}
	restart()
	for _, p := range []Period{All, Month} {
		CodexModelEffort(p)
		if n := paired[len(paired)-1]; n >= first[p] {
			t.Fatalf("%s started again, paired %d of %d: %v", p, n, first[p], paired)
		}
	}
}

// The effort read back from effort-v1.json is kept: its entries share one
// string for each model and effort name, not a copy each (#2278).
func TestCodexEffortNamesReadOnce(t *testing.T) {
	pageHome(t)
	sums := map[string][]sessions.ModelEffort{
		"a": {{Model: "gpt-5", Effort: "high"}, {Model: "gpt-5", Effort: "low"}},
		"b": {{Model: "gpt-5", Effort: "high"}},
	}
	if err := effortDiskWrite("k", time.Time{}, time.Time{}, sums); err != nil {
		t.Fatal(err)
	}
	got, ok := effortDiskRead("k")
	if !ok || len(got["a"]) != 2 || len(got["b"]) != 1 {
		t.Fatalf("read %v %v", got, ok)
	}
	a, b := got["a"][0], got["b"][0]
	if a.Model != "gpt-5" || a.Effort != "high" {
		t.Fatalf("read %+v", a)
	}
	if unsafe.StringData(a.Model) != unsafe.StringData(b.Model) || unsafe.StringData(a.Effort) != unsafe.StringData(b.Effort) ||
		unsafe.StringData(a.Model) != unsafe.StringData(got["a"][1].Model) {
		t.Fatal("each entry has its own copy of a name")
	}
}
