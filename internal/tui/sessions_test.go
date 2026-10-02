package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// sessionsHome puts the sessions package's fixtures where Claude Code and
// Codex keep their sessions, in a sandbox HOME, their models priced.
func sessionsHome(t *testing.T) {
	t.Helper()
	h := home(t)
	t.Setenv("HERMES_HOME", filepath.Join(h, ".hermes"))
	for from, env := range map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME"} {
		dir := filepath.Join(h, "sessions", from)
		if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "sessions", "testdata", from))); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, dir)
	}
	oldZone, oldPrice := time.Local, sessions.PriceOf
	time.Local = time.UTC
	sessions.PriceOf = func(_ settings.Settings, m string) (catalog.Price, bool) {
		switch m {
		case "claude-opus-5-5":
			return catalog.Price{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}, true
		case "gpt-6-astra":
			return catalog.Price{Input: 10, Output: 50, CacheRead: 1}, true
		}
		return catalog.Price{}, false
	}
	t.Cleanup(func() {
		// the page's index is written behind it: let that write finish before
		// the zone it reads (a stat of the file it writes) goes back
		sessions.Reset()
		time.Local, sessions.PriceOf = oldZone, oldPrice
	})
	sessions.Reset()
}

// The Sessions page shows a range's totals, a chart of its days and its
// top models and folders; a model or a folder picked narrows them.
func TestSessionsPage(t *testing.T) {
	sessionsHome(t)
	m := press(t, model{w: 120, h: 40, srange: 1}, "5", "A", "s")
	if m.page != pageSessions || sessRanges[m.srange].days != 0 || !m.sstat {
		t.Fatalf("page %d, range %d", m.page, m.srange)
	}
	v := m.View()
	for _, want := range []string{"5 sessions", " all ", "all models", "all folders", "11.9K tokens", "cache read 25.2K (69% hit)", "active 7m on 2 days",
		"█", "Sep 20", "tokens · a day", "models", "gpt-6-astra", "claude-opus-5-5", "folders", "/work/app", "/work/it's", "M model", "f folder"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in\n%s", want, v)
		}
	}

	// pick a model: the totals are its, and active time isn't kept by model
	m = press(t, m, "M")
	if m.mode != modePick {
		t.Fatal("no picker")
	}
	m = typeIn(m, "opus")
	m = press(t, m, "enter")
	if m.smodel != "claude-opus-5-5" || m.mode != modeList {
		t.Fatalf("model %q, mode %d", m.smodel, m.mode)
	}
	v = m.View()
	for _, want := range []string{"claude-opus-5-5 · all folders", "active — not kept by model", "folders", "/work/app"} {
		if !strings.Contains(v, want) {
			t.Errorf("opus: missing %q in\n%s", want, v)
		}
	}
	if strings.Contains(v, "gpt-6-astra") {
		t.Errorf("opus: Codex's model still counted\n%s", v)
	}
	// x clears the filters, c charts cost
	m = press(t, m, "x", "c")
	if m.smodel != "" || !strings.Contains(m.View(), "cost · a day") {
		t.Errorf("x, c:\n%s", m.View())
	}
	// today has nothing in the fixtures, and no chart of one day
	m = press(t, m, "t")
	if v := m.View(); !strings.Contains(v, "nothing in this range") || strings.Contains(v, "a day") {
		t.Errorf("today:\n%s", v)
	}
}

// The page opens on the sessions themselves, the latest first, narrowed
// by the range, model and folder as the stats are; ↑↓ pick one, whose
// resume command is shown.
func TestSessionsList(t *testing.T) {
	sessionsHome(t)
	m := press(t, model{w: 160, h: 40, srange: 1}, "5", "A")
	if m.sstat {
		t.Fatal("opened on the stats")
	}
	v := m.View()
	t.Log("\n" + v)
	shown := m.sessShown()
	if len(shown) == 0 {
		t.Fatalf("no sessions:\n%s", v)
	}
	for _, want := range []string{"sessions · stats", "▸ ", "↑↓ session", "↵ resume", "s stats", "↵ runs " + shown[0].Resume} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in\n%s", want, v)
		}
	}
	m = press(t, m, "down")
	if len(shown) > 1 && (m.ssel != 1 || !strings.Contains(m.View(), "↵ runs "+shown[1].Resume)) {
		t.Errorf("down: sel %d\n%s", m.ssel, m.View())
	}
	m = press(t, m, "up", "up")
	if m.ssel != 0 {
		t.Errorf("up past the top: %d", m.ssel)
	}
	// a model picked lists only the sessions that used it
	m.smodel = "gpt-6-astra"
	for _, s := range m.sessShown() {
		if s.Agent != "codex" {
			t.Errorf("gpt-6-astra: %s %s listed", s.Agent, s.ID)
		}
	}
	// today has nothing in the fixtures
	m = press(t, m, "x", "t")
	if v := m.View(); !strings.Contains(v, "nothing in this range") {
		t.Errorf("today:\n%s", v)
	}
	// s goes to the stats and back
	if m = press(t, m, "s"); !m.sstat || !strings.Contains(m.View(), "sessions · stats") {
		t.Errorf("s:\n%s", m.View())
	}
	if m = press(t, m, "s"); m.sstat {
		t.Error("s again stayed on the stats")
	}
}

// Only the sessions active since the range began are listed.
func TestSessFilter(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	all := []sessions.Session{
		{ID: "a", Last: now.Add(-time.Hour), Cwd: "/x", Models: []sessions.Model{{Model: "m1"}}},
		{ID: "b", Last: now.AddDate(0, 0, -1), Cwd: "/y", Models: []sessions.Model{{Model: "m2"}}},
		{ID: "c", Last: now.AddDate(0, 0, -10), Cwd: "/x", Models: []sessions.Model{{Model: "m1"}, {Model: "m2"}}},
	}
	ids := func(ss []sessions.Session) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		days          int
		model, folder string
		want          string
	}{{1, "", "", "a"}, {7, "", "", "a,b"}, {0, "", "", "a,b,c"}, {0, "m2", "", "b,c"}, {0, "", "/x", "a,c"}, {0, "m2", "/x", "c"}} {
		if got := ids(sessFilter(all, c.days, c.model, c.folder, now)); got != c.want {
			t.Errorf("%d %q %q: %s, want %s", c.days, c.model, c.folder, got, c.want)
		}
	}
}

// A list longer than the window shows the rows around the one picked,
// with how many more are above and below.
func TestSessListWindow(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	var list []sessions.Session
	for i := range 30 {
		list = append(list, sessions.Session{Agent: "claude", ID: fmt.Sprint("s", i), Title: fmt.Sprint("task ", i), Last: now.Add(-time.Duration(i) * time.Hour)})
	}
	for _, c := range []struct {
		sel        int
		want, gone []string
	}{
		{0, []string{"▸ just now", "task 0", "↓ 17 more"}, []string{"↑", "task 13\n"}},
		{20, []string{"task 20", "task 21", "↑ 9 more", "↓ 8 more"}, []string{"task 8\n", "task 22"}},
		{29, []string{"task 29", "↑ 17 more"}, []string{"↓"}},
	} {
		ls := sessListLines(list, nil, 4, "", "", c.sel, 120, 24, now)
		if len(ls) > 24-3 {
			t.Errorf("sel %d: %d lines don't fit 24 with the footer", c.sel, len(ls))
		}
		v := strings.Join(ls, "\n") + "\n"
		for _, w := range c.want {
			if !strings.Contains(v, w) {
				t.Errorf("sel %d: missing %q in\n%s", c.sel, w, v)
			}
		}
		for _, g := range c.gone {
			if strings.Contains(v, g) {
				t.Errorf("sel %d: %q in\n%s", c.sel, g, v)
			}
		}
	}
}

// The chart is a column a day with room for gaps, a week a column when
// the days don't fit, eighths of a block for what is between.
func TestSessChart(t *testing.T) {
	day := func(date string, n int) sessions.DayTotal {
		d := sessions.DayTotal{Date: date}
		d.Input = n
		return d
	}
	days := []sessions.DayTotal{day("2026-09-26", 800), day("2026-09-27", 0), day("2026-09-28", 100)}
	ls := sessChart(days, false, 80, 2)
	if len(ls) != 3 {
		t.Fatalf("%d lines: %q", len(ls), ls)
	}
	if ls[0] != "█      800" || ls[1] != "█   ▂  tokens · a day" || ls[2] != "Sep 26" {
		t.Errorf("%q", ls)
	}
	var many []sessions.DayTotal
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	for i := range 200 {
		many = append(many, day(start.AddDate(0, 0, i).Format(time.DateOnly), 1000))
	}
	ls = sessChart(many, false, 60, 4)
	if !strings.Contains(ls[len(ls)-2], "a week") || len([]rune(strings.Fields(ls[3])[0])) != 29 {
		t.Errorf("weeks: %q", ls)
	}
	if sessChart(days[:1], false, 80, 6) != nil {
		t.Error("a chart of one day")
	}
}
