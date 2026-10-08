package sessions

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// inZone runs the test with the local time zone at a fixed offset.
func inZone(t *testing.T, hours int) {
	testenv.Zone(t, time.FixedZone("test", hours*3600))
}

// ccMsg is a line of a Claude Code reply at a time.
func ccMsg(id, at string, in, out int) string {
	return `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5-5","id":"` + id + `","role":"assistant","content":[],"usage":{"input_tokens":` + itoa(in) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":10,"output_tokens":` + itoa(out) + `}},"timestamp":"` + at + `","cwd":"/work/night","sessionId":"33333333-2222-3333-4444-555555555555"}` + "\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

// usageOn sums what a day of the stats spent, of one folder.
func usageOn(s Stats, date, cwd string) (Tokens, int64) {
	var t Tokens
	var sec int64
	for _, d := range s.Days {
		if d.Date != date {
			continue
		}
		for _, u := range d.Usage {
			if u.Cwd == cwd {
				t.add(u.Tokens)
			}
		}
		for _, a := range d.Active {
			if a.Cwd == cwd {
				sec += a.Seconds
			}
		}
	}
	return t, sec
}

var statsNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func TestStatsByDay(t *testing.T) {
	inZone(t, 0)
	setup(t)
	s := statsAt(0, statsNow)
	if s.From != "2026-09-20" || s.To != "2026-09-28" {
		t.Fatalf("range %s … %s", s.From, s.To)
	}
	// Claude Code on the 20th: the session and its subagent, in its folder;
	// Codex on the 20th and, picked up again, on the 23rd. The session's own
	// file was at work 10:00:00–10:05:00, every pause under five minutes.
	if got, sec := usageOn(s, "2026-09-20", "/work/app"); got != (Tokens{1110, 170, 5200, 1000, 0}) || sec != 300 {
		t.Fatalf("claude on the 20th: %+v, %ds active", got, sec)
	}
	if got, _ := usageOn(s, "2026-09-20", "/work/it's"); got != (Tokens{8000, 500, 14000, 0, 0}) {
		t.Fatalf("codex on the 20th: %+v", got)
	}
	if got, _ := usageOn(s, "2026-09-23", "/work/it's"); got != (Tokens{2000, 100, 6000, 0, 0}) {
		t.Fatalf("codex on the 23rd: %+v", got)
	}
	// the days add up to the sessions' totals
	var days, listed Tokens
	for _, d := range s.Days {
		for _, u := range d.Usage {
			days.add(u.Tokens)
		}
	}
	for _, x := range List(0) {
		listed.add(x.Tokens)
	}
	if days != listed {
		t.Fatalf("days %+v, sessions %+v", days, listed)
	}
	// a range leaves out what came before it
	if w := statsAt(7, statsNow); w.From != "2026-09-22" || len(w.Days) != 1 || w.Days[0].Date != "2026-09-23" {
		t.Fatalf("7 days: %+v", w)
	}
	for _, u := range s.Days[0].Usage {
		if u.Model == "claude-opus-5-5" && (!u.Priced || !near(u.Cost, (100*4+50*20+5000*0.2+1000*5)/1e6)) {
			t.Fatalf("opus cost: %+v", u)
		}
	}
}

// A session that runs past midnight counts on both days: each message on
// its own day, a message whose blocks straddle midnight once, on the day of
// its last word, and the work across midnight on the day it ended.
func TestStatsMidnight(t *testing.T) {
	inZone(t, 8) // 16:00Z is midnight
	claude, _ := setup(t)
	path := filepath.Join(claude, "projects", "-work-night", "33333333-2222-3333-4444-555555555555.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	first := ccMsg("m1", "2026-09-20T15:56:00.000Z", 100, 10) + // 23:56
		ccMsg("m2", "2026-09-20T15:59:00.000Z", 200, 20) // 23:59, a block of m2
	rest := ccMsg("m2", "2026-09-20T16:01:00.000Z", 200, 25) + // 00:01, m2's last word
		ccMsg("m3", "2026-09-20T16:03:00.000Z", 400, 40) + // 00:03
		ccMsg("m4", "2026-09-20T18:00:00.000Z", 800, 80) // 02:00, after a long pause
	os.WriteFile(path, []byte(first), 0o644)

	check := func(when string, d20, d21 Tokens, a20, a21 int64) {
		t.Helper()
		s := statsAt(0, statsNow)
		if got, sec := usageOn(s, "2026-09-20", "/work/night"); got != d20 || sec != a20 {
			t.Fatalf("%s, the 20th: %+v %ds, want %+v %ds", when, got, sec, d20, a20)
		}
		if got, sec := usageOn(s, "2026-09-21", "/work/night"); got != d21 || sec != a21 {
			t.Fatalf("%s, the 21st: %+v %ds, want %+v %ds", when, got, sec, d21, a21)
		}
	}
	check("before midnight", Tokens{300, 30, 20, 0, 0}, Tokens{}, 180, 0)

	// read on from where it was left: m2 moves to the 21st, counted once
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(rest)
	f.Close()
	after20, after21 := Tokens{100, 10, 10, 0, 0}, Tokens{1400, 145, 30, 0, 0}
	check("read on", after20, after21, 180, 240)

	// the same from the start, and from the parse kept on disk
	full := parse(file{agent: "claude", path: path, main: true, size: int64(len(first + rest)), mod: time.Now()}, nil)
	if got := full.Days["2026-09-21"].Models["claude-opus-5-5"]; got != after21 {
		t.Fatalf("read whole: %+v", got)
	}
	Reset()
	check("kept on disk", after20, after21, 180, 240)
	// and read again unchanged, nothing counts twice
	check("again", after20, after21, 180, 240)
}

// Sum rolls the days up as the app's Sessions view does: a date for every
// day of the range, totals under a model or a folder, and no active time
// under a model, as it isn't kept by model.
func TestSum(t *testing.T) {
	inZone(t, 0)
	setup(t)
	r := statsAt(7, statsNow).Sum("", "")
	if len(r.Days) != 7 || r.Days[0].Date != "2026-09-22" || r.Days[6].Date != "2026-09-28" {
		t.Fatalf("days %+v", r.Days)
	}
	// only Codex's pick-up on the 23rd is in the last 7 days
	if r.Tokens != (Tokens{2000, 100, 6000, 0, 0}) || r.DaysUsed != 1 || r.Days[1].Spent() != 2100 {
		t.Fatalf("%+v", r)
	}
	if len(r.Models) != 1 || r.Models[0].Name != "gpt-6-astra" || len(r.Folders) != 1 || r.Folders[0].Name != "/work/it's" {
		t.Fatalf("models %+v folders %+v", r.Models, r.Folders)
	}

	all := statsAt(0, statsNow)
	app := all.Sum("", "/work/app")
	if app.Tokens != (Tokens{1110, 170, 5200, 1000, 0}) || app.Active != 300 || len(app.Folders) != 2 {
		t.Fatalf("app: %+v", app)
	}
	opus := all.Sum("claude-opus-5-5", "")
	if opus.Active != -1 || opus.Days[0].Active != -1 || len(opus.Folders) != 1 || opus.Folders[0].Name != "/work/app" {
		t.Fatalf("opus: %+v", opus)
	}
	if opus.Cost == 0 || !near(opus.Cost, opus.Folders[0].Cost) {
		t.Fatalf("opus cost %v, its folder's %v", opus.Cost, opus.Folders[0].Cost)
	}
	if none := all.Sum("no-such-model", ""); none.Spent() != 0 || none.DaysUsed != 0 {
		t.Fatalf("none: %+v", none)
	}
	if Duration(-1) != "—" || Duration(20) != "<1m" || Duration(300) != "5m" || Duration(3*3600+12*60) != "3h 12m" {
		t.Fatal(Duration(20), Duration(300))
	}
}

// The active time is kept by the hour it was put on too, and each session
// in the range comes with what it spent in it and the days it was at work.
func TestStatsHoursAndSessions(t *testing.T) {
	inZone(t, 8)
	claude, _ := setup(t)
	path := filepath.Join(claude, "projects", "-work-night", "33333333-2222-3333-4444-555555555555.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(ccMsg("m1", "2026-09-20T15:56:00.000Z", 100, 10)+ // 23:56
		ccMsg("m2", "2026-09-20T15:59:00.000Z", 200, 20)+ // 23:59
		ccMsg("m3", "2026-09-20T16:03:00.000Z", 400, 40)), 0o644) // 00:03

	hours := func(s Stats, date string) []int64 {
		for _, d := range s.Days {
			for _, a := range d.Active {
				if d.Date == date && a.Cwd == "/work/night" {
					return a.Hours
				}
			}
		}
		return nil
	}
	s := statsAt(0, statsNow)
	if h := hours(s, "2026-09-20"); len(h) != 24 || h[23] != 180 {
		t.Fatalf("the 20th by hour: %v", h)
	}
	if h := hours(s, "2026-09-21"); len(h) != 24 || h[0] != 240 || h[23] != 0 {
		t.Fatalf("the 21st by hour: %v", h)
	}

	key := "claude:33333333-2222-3333-4444-555555555555"
	find := func(s Stats) *Summary {
		for i := range s.Sessions {
			if s.Sessions[i].Key == key {
				return &s.Sessions[i]
			}
		}
		return nil
	}
	x := find(s)
	if x == nil || x.Agent != "claude" || x.Cwd != "/work/night" || x.Active != 420 ||
		x.Tokens != (Tokens{700, 70, 30, 0, 0}) || !x.Priced || x.Cost <= 0 ||
		len(x.Models) != 1 || len(x.Days) != 2 || x.Days[0] != 0 || x.Days[1] != 1 {
		t.Fatalf("summary %+v", x)
	}
	if !sortedByLast(s.Sessions) || len(s.Sessions) < 3 {
		t.Fatalf("sessions %+v", s.Sessions)
	}
	// a range counts only its own days
	day := statsAt(1, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	if x := find(day); x == nil || x.Tokens != (Tokens{400, 40, 10, 0, 0}) || x.Active != 240 || len(x.Days) != 1 || x.Days[0] != 0 {
		t.Fatalf("the 21st alone: %+v", x)
	}
	if len(day.Sessions) != 1 {
		t.Fatalf("the 21st: %d sessions", len(day.Sessions))
	}

	// Get reads a session by its key, as Find does past the listed ones
	if g, ok := Get(key); !ok || g.Cwd != "/work/night" || g.Tokens != (Tokens{700, 70, 30, 0, 0}) {
		t.Fatalf("get %+v %v", g, ok)
	}
	if _, ok := Get("claude:nope"); ok {
		t.Fatal("got a session that isn't there")
	}
	if f, ok := Find("claude", "33333333-2222-3333-4444-555555555555"); !ok || f.Cwd != "/work/night" {
		t.Fatalf("find %+v", f)
	}
	// the overview: all, then one folder, then one model
	o := s.Overview("", "", "", 2)
	if o.Count != len(s.Sessions) || len(o.Days) != 9 || o.Days[0] < 2 || o.Days[1] != 1 ||
		len(o.Top["tokens"]) != 2 || o.Top["tokens"][0].Input+o.Top["tokens"][0].Output < o.Top["tokens"][1].Input+o.Top["tokens"][1].Output ||
		o.Top["active"][0].Key != key || o.Median == 0 || o.P90 < o.Median {
		t.Fatalf("overview %+v", o)
	}
	if o := s.Overview("claude", "", "/work/night", 5); o.Count != 1 || o.Median != 770 || o.P90 != 770 || o.Days[0] != 1 || o.Days[1] != 1 || len(o.Top["cost"]) != 1 {
		t.Fatalf("one folder %+v", o)
	}
	if o := s.Overview("", "no-such-model", "", 5); o.Count != 0 || o.Median != 0 || len(o.Top["tokens"]) != 0 {
		t.Fatalf("no model %+v", o)
	}
	if clip("abcdef", 4) != "abc…" || clip("abc", 4) != "abc" {
		t.Fatal("clip")
	}
}

func sortedByLast(ss []Summary) bool {
	for i := 1; i < len(ss); i++ {
		if ss[i].Last.After(ss[i-1].Last) {
			return false
		}
	}
	return true
}
