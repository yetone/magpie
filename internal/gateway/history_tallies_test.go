package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The live Routing page asks for today's tallies every 15s (#3351), and a
// busy day's file is hundreds of MB (#3383): each look folds in only the
// lines added since the last, a line still being written waits for its
// end, and a look at another day's folder starts again.
func TestDayTalliesFoldOnlyWhatWasAdded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	route := func(i int, who string, status int) Route {
		w := Weighed{ID: "p/" + who, Provider: "p", Name: "P", Kind: "account", Who: who, Model: "m"}
		tr := Try{ID: w.ID, Start: now.Add(time.Duration(i) * time.Second), Done: true, Millis: 100, Status: status}
		if status >= 400 {
			tr.Fail = "rate"
		}
		return Route{ID: int64(i + 1), Time: tr.Start, Done: true, Model: "m", Provider: "p", Order: []Weighed{w}, Tries: []Try{tr}}
	}
	for i := range 100 {
		status := 200
		if i == 0 || i == 45 {
			status = 429
		}
		saveRoute(route(i, "A", status))
	}
	lines := tallyLines.Load()
	got := DayTallies()
	busy := got[`["p","a"]`]
	if busy.Tried != 100 || busy.OK != 98 || busy.Fails["rate"] != 2 {
		t.Fatalf("the day: tried %d, answered %d, fails %v", busy.Tried, busy.OK, busy.Fails)
	}
	if n := tallyLines.Load() - lines; n != 100 {
		t.Fatalf("the first look read %d lines, want the day's 100", n)
	}

	// one route more: one line read, not the day again
	saveRoute(route(100, "B", 200))
	lines = tallyLines.Load()
	got = DayTallies()
	if n := tallyLines.Load() - lines; n != 1 {
		t.Errorf("a look after one route read %d lines, want 1", n)
	}
	if got[`["p","a"]`].Tried != 100 || got[`["p","b"]`].Tried != 1 {
		t.Errorf("after one route: A tried %d, B tried %d", got[`["p","a"]`].Tried, got[`["p","b"]`].Tried)
	}
	// nothing added: nothing read
	lines = tallyLines.Load()
	DayTallies()
	if n := tallyLines.Load() - lines; n != 0 {
		t.Errorf("a look with nothing added read %d lines", n)
	}

	// what a look hands out is a copy: counting on doesn't change it
	held := DayTallies()[`["p","a"]`]
	saveRoute(route(101, "A", 429))
	DayTallies()
	if held.Tried != 100 || held.Fails["rate"] != 2 {
		t.Errorf("a tally handed out changed after: tried %d, fails %v", held.Tried, held.Fails)
	}

	// half a line, as another magpie writing it leaves it: not counted
	// until its end is there, and then counted once
	b, _ := json.Marshal(route(102, "B", 200))
	path := filepath.Join(HistoryDir(), now.Format(dayForm)+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(b[:len(b)/2])
	if got := DayTallies()[`["p","b"]`].Tried; got != 1 {
		t.Errorf("with half a line: B tried %d, want 1", got)
	}
	f.Write(append(b[len(b)/2:], '\n'))
	f.Close()
	if got := DayTallies()[`["p","b"]`].Tried; got != 2 {
		t.Errorf("the line finished: B tried %d, want 2", got)
	}

	// another folder (another HOME): its own day, nothing of the last
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveRoute(route(0, "C", 200))
	got = DayTallies()
	if _, ok := got[`["p","a"]`]; ok || got[`["p","c"]`].Tried != 1 {
		t.Errorf("another folder's day: %v", got)
	}
}

// The Routing page asks to sign an account in again while its latest
// refusal for its sign-in is newer than its latest answer (adorable_piglet
// on Discord: "2 需要重新登录" and nowhere to sign in): the day's tally
// says when that refusal was, beside when it last answered.
func TestDayTalliesSayWhenSignInWasRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	route := func(i int, status int, fail string) Route {
		w := Weighed{ID: "codex@b", Provider: "codex", Name: "Codex", Kind: "account", Who: "b@example.com", Agent: "codex", Model: "gpt-5"}
		tr := Try{ID: w.ID, Start: now.Add(time.Duration(i) * time.Minute), Done: true, Millis: 100, Status: status, Fail: fail}
		return Route{ID: int64(i + 1), Time: tr.Start, Done: true, Model: "gpt-5", Provider: "codex", Order: []Weighed{w}, Tries: []Try{tr}}
	}
	saveRoute(route(0, 200, ""))
	saveRoute(route(1, 401, failAuth))
	saveRoute(route(2, 502, "other"))
	a := DayTallies()[`["codex","b@example.com"]`]
	refused := now.Add(time.Minute).UnixMilli() + 100
	if a.AuthAt != refused || a.Fails[failAuth] != 1 {
		t.Fatalf("refused at %d (%d of them), want %d", a.AuthAt, a.Fails[failAuth], refused)
	}
	if a.Last >= a.AuthAt {
		t.Fatalf("answered at %d, after the refusal at %d", a.Last, a.AuthAt)
	}
	// it answers again: the refusal is older than that answer, and stays
	// when it was
	saveRoute(route(3, 200, ""))
	a = DayTallies()[`["codex","b@example.com"]`]
	if a.AuthAt != refused || a.Last <= a.AuthAt {
		t.Errorf("after an answer: refused at %d, answered at %d", a.AuthAt, a.Last)
	}
}
