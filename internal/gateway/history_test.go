package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// historyNoon holds the history's clock at noon of a past day and returns
// it: a test's routes, a second apart, then fall on that one day, and
// saveRoute prunes by it rather than by the real day, which may have
// turned since the test began.
func historyNoon(t *testing.T) time.Time {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	historyClock = func() time.Time { return now }
	t.Cleanup(func() { historyClock = time.Now })
	return now
}

func TestHistoryKeepsSessionAndTokenTiers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	records := routeUsage("relay", "m", Usage{Input: 20, Output: 5, CacheRead: 100, CacheWrite: 40})
	saveRoute(Route{ID: 1, Time: now, Session: "conversation", ParentSession: "parent-conversation", Kind: "thread_title", Usage: records, Done: true})
	saveRoute(Route{ID: 2, Time: now, Tokens: 1000, Done: true}) // legacy route
	pruneHistory(HistoryDir(), now.AddDate(0, 0, 1))
	_, routes, _ := History(now.Format(dayForm))
	if len(routes) != 2 || routes[0].Session != "conversation" || routes[0].ParentSession != "parent-conversation" || len(routes[0].Usage) != 1 || routes[0].Usage[0] != (RouteUsage{Provider: "relay", Model: "m", Input: 20, Output: 5, CacheRead: 100, CacheWrite: 40}) {
		t.Fatalf("compressed history lost accounting: %+v", routes)
	}
	if routes[1].Session != "" || len(routes[1].Usage) != 0 {
		t.Fatalf("invented legacy accounting: %+v", routes[1])
	}
}

// A done route is kept on disk by its day, a day that is over gzipped and
// still read, and a day too old or past the size kept dropped.
func TestHistoryKeepsDaysAndDropsOld(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	yday := now.AddDate(0, 0, -1)
	saveRoute(Route{ID: 1, Time: yday, Model: "a", Done: true})
	saveRoute(Route{ID: 2, Time: now, Model: "b", Done: true})
	saveRoute(Route{ID: 3, Time: now.Add(time.Second), Model: "c", Done: true})
	dir := HistoryDir()
	old := filepath.Join(dir, now.AddDate(0, 0, -historyDays-2).Format(dayForm)+".jsonl")
	os.WriteFile(old, []byte(`{"id":9,"model":"z"}`+"\n"), 0o600)

	pruneHistory(dir, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("a day older than the days kept is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, yday.Format(dayForm)+".jsonl.gz")); err != nil {
		t.Fatal("yesterday isn't gzipped:", err)
	}
	days, rs, _ := History(now.Format(dayForm))
	if len(days) != 2 || days[0].Requests != 2 || days[1].Requests != 1 || len(rs) != 2 || rs[0].Model != "b" || rs[1].Model != "c" {
		t.Fatalf("days %+v routes %+v", days, rs)
	}
	if _, rs, _ := History(yday.Format(dayForm)); len(rs) != 1 || rs[0].Model != "a" {
		t.Fatalf("yesterday, gzipped: %+v", rs)
	}
	// over the bytes kept: the oldest go, today stays
	big := strings.Repeat("x", historyBytes/3)
	for i := 3; i <= 5; i++ {
		os.WriteFile(filepath.Join(dir, now.AddDate(0, 0, -i).Format(dayForm)+".jsonl.gz"), []byte(big), 0o600)
	}
	pruneHistory(dir, now)
	var left []string
	for _, d := range historyFiles(dir) {
		left = append(left, d.day)
	}
	want := []string{now.AddDate(0, 0, -4).Format(dayForm), now.AddDate(0, 0, -3).Format(dayForm), yday.Format(dayForm), now.Format(dayForm)}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want %v", left, want)
	}
}

// A route that began on a day gzipped already, its reply streamed past
// midnight and past that hour's prune, is kept with the day's other
// routes: its file is added to the day's .gz, not written over it, and the
// day is listed once with all of them, before and after. Its file added and
// left behind (magpie stopped before removing it) adds none of them again,
// however often it is left, and late routes written to it in between.
func TestHistoryKeepsALateRouteWithItsDay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	y, m, d := now.AddDate(0, 0, -1).Date()
	yday := time.Date(y, m, d, 23, 59, 0, 0, time.Local)
	before := yday.AddDate(0, 0, -1)
	day, dir := yday.Format(dayForm), HistoryDir()
	late := filepath.Join(dir, day+".jsonl")
	kept := func(when string, ids ...int64) {
		t.Helper()
		want := []HistoryDay{{day, len(ids)}, {before.Format(dayForm), 1}}
		days, rs, _ := History(day)
		var got []int64
		for _, r := range rs {
			got = append(got, r.ID)
		}
		if !slices.Equal(days, want) || !slices.Equal(got, ids) {
			t.Errorf("%s: days %+v, routes %v; want %+v, routes %v", when, days, got, want, ids)
		}
		if days, _, _ := History(""); !slices.Equal(days, want) {
			t.Errorf("%s: days live %+v; want %+v", when, days, want)
		}
		for _, id := range ids {
			if _, ok := HistoryRoute(id, yday); !ok {
				t.Errorf("%s: route %d not found", when, id)
			}
		}
	}
	prune := func() {
		t.Helper()
		pruneHistory(dir, now)
		if _, err := os.Stat(late); !os.IsNotExist(err) {
			t.Errorf("the late file is still there: %v", err)
		}
	}
	saveRoute(Route{ID: 9, Time: before, Model: "z", Done: true})
	saveRoute(Route{ID: 1, Time: yday, Model: "a", Done: true})
	prune()
	saveRoute(Route{ID: 2, Time: yday.Add(10 * time.Second), Model: "b", Done: true}) // done after the prune
	kept("beside the day's .gz", 1, 2)
	left, err := os.ReadFile(late)
	if err != nil {
		t.Fatal(err)
	}
	prune()
	kept("added to the day's .gz", 1, 2)
	os.WriteFile(late, left, 0o600)
	prune()
	kept("left behind", 1, 2)
	ids := []int64{1, 2, 3, 4}
	for _, id := range ids[2:] {
		os.WriteFile(late, left, 0o600)
		saveRoute(Route{ID: id, Time: yday.Add(time.Duration(id) * 10 * time.Second), Model: "c", Done: true})
		if left, err = os.ReadFile(late); err != nil {
			t.Fatal(err)
		}
		prune()
		kept("left behind, a late route written to it since", ids[:id]...)
	}
}

// A day's .gz cut short, by an older magpie stopped (or a disk filled) while
// writing it in place, or by a route cut short in the file it was gzipped
// from, is written anew from the whole routes it has and its file's: after
// the break none of the file's routes could be read, and after a route cut
// short the first of them would join it. A last route whole but for its
// newline is kept.
func TestHistoryKeepsADayCutShort(t *testing.T) {
	route := func(id int) string { return `{"id":` + strconv.Itoa(id) + `,"model":"m"}` + "\n" }
	for _, c := range []struct {
		name string
		gz   func(z *gzip.Writer) // writes the .gz as it was left
		file string
		ids  []int64
	}{
		// gzipped in place from its file, all three routes, and stopped
		// after the first and a half
		{"stopped while written in place", func(z *gzip.Writer) {
			z.Write([]byte(route(1) + route(2)[:9]))
			z.Flush()
		}, route(1) + route(2) + route(3), []int64{1, 2, 3}},
		// gzipped whole from a file whose second route was cut short, and a
		// late route written to the file since
		{"its file cut short", func(z *gzip.Writer) {
			z.Write([]byte(route(1) + route(2)[:9]))
			z.Close()
		}, route(3), []int64{1, 3}},
		// gzipped whole from a file whose last route was written without its
		// newline, and a late route written to the file since
		{"its last route without its newline", func(z *gzip.Writer) {
			z.Write([]byte(route(1) + strings.TrimSuffix(route(2), "\n")))
			z.Close()
		}, route(3), []int64{1, 2, 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			now := time.Now()
			day, dir := now.AddDate(0, 0, -1).Format(dayForm), HistoryDir()
			path := filepath.Join(dir, day+".jsonl")
			os.MkdirAll(dir, 0o700)
			var gz bytes.Buffer
			c.gz(gzip.NewWriter(&gz))
			os.WriteFile(path+".gz", gz.Bytes(), 0o600)
			os.WriteFile(path, []byte(c.file), 0o600)
			pruneHistory(dir, now)
			days, rs, _ := History(day)
			var got []int64
			for _, r := range rs {
				got = append(got, r.ID)
			}
			if len(days) != 1 || days[0] != (HistoryDay{day, len(c.ids)}) || !slices.Equal(got, c.ids) {
				t.Errorf("days %+v, routes %v; want %s with %v", days, got, day, c.ids)
			}
			b, _ := os.ReadFile(path + ".gz")
			z, err := gzip.NewReader(bytes.NewReader(b))
			if err == nil {
				_, err = io.Copy(io.Discard, z)
			}
			if err != nil {
				t.Errorf("the .gz isn't whole: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("the file is still there: %v", err)
			}
		})
	}
}

// A day's .gz.tmp left by a magpie stopped before renaming it over the .gz
// goes at a prune once it is an hour old. A younger one, maybe another
// magpie's still being written, stays.
func TestHistoryDropsALeftGzTmp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	dir := HistoryDir()
	os.MkdirAll(dir, 0o700)
	left := filepath.Join(dir, now.AddDate(0, 0, -2).Format(dayForm)+".jsonl.gz.tmp")
	young := filepath.Join(dir, now.AddDate(0, 0, -1).Format(dayForm)+".jsonl.gz.tmp")
	os.WriteFile(left, []byte("\x1f\x8b\x08"), 0o600)
	os.WriteFile(young, []byte("\x1f\x8b\x08"), 0o600)
	then := now.Add(-time.Hour - time.Minute)
	if err := os.Chtimes(left, then, then); err != nil {
		t.Fatal(err)
	}
	pruneHistory(dir, now)
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Errorf("the .gz.tmp left over an hour ago is still there: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("a .gz.tmp written just now is gone: %v", err)
	}
}

// A day's .gz that can't be read (left by magpie run as another user, say)
// is unknown, not empty: it isn't written over from the day's file, and the
// file stays for a prune that can read the .gz.
func TestHistoryLeavesADayItCantRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	day, dir := now.AddDate(0, 0, -1).Format(dayForm), HistoryDir()
	path := filepath.Join(dir, day+".jsonl")
	os.MkdirAll(dir, 0o700)
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write([]byte(`{"id":1,"model":"m"}` + "\n"))
	z.Close()
	os.WriteFile(path+".gz", gz.Bytes(), 0o600)
	os.WriteFile(path, []byte(`{"id":2,"model":"m"}`+"\n"), 0o600)
	if err := os.Chmod(path+".gz", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path + ".gz"); err == nil {
		t.Skip("a file at mode 0 still reads here: root, Windows, or a file system without Unix permissions")
	}
	pruneHistory(dir, now)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file is gone with the .gz unread: %v", err)
	}
	os.Chmod(path+".gz", 0o600)
	pruneHistory(dir, now)
	days, rs, _ := History(day)
	var got []int64
	for _, r := range rs {
		got = append(got, r.ID)
	}
	if want := []int64{1, 2}; len(days) != 1 || days[0] != (HistoryDay{day, 2}) || !slices.Equal(got, want) {
		t.Errorf("read again: days %+v, routes %v; want %s with %v", days, got, day, want)
	}
}

// A day dropped for the size kept is dropped whole: a late route's file left
// beside its day's .gz goes with the .gz. The .gz can't be read at the prune,
// which leaves the file beside it, and reads again before the day is listed:
// the day isn't left listed without its late route.
func TestHistoryDropsADayWhole(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	y, m, d := now.AddDate(0, 0, -2).Date()
	late := time.Date(y, m, d, 23, 59, 0, 0, time.Local)
	day, dir := late.Format(dayForm), HistoryDir()
	path := filepath.Join(dir, day+".jsonl")
	saveRoute(Route{ID: 1, Time: late, Model: "a", Done: true})
	pruneHistory(dir, now)
	saveRoute(Route{ID: 2, Time: late.Add(10 * time.Second), Model: "b", Done: true}) // done after the prune
	if err := os.Chmod(path+".gz", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path + ".gz"); err == nil {
		t.Skip("a file at mode 0 still reads here: root, Windows, or a file system without Unix permissions")
	}
	gz, err := os.Stat(path + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// a newer day takes the rest of the size kept: the day's two files are
	// over it, and either of them alone isn't
	newer := filepath.Join(dir, now.AddDate(0, 0, -1).Format(dayForm)+".jsonl.gz")
	if err := os.WriteFile(newer, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(newer, historyBytes-max(gz.Size(), file.Size())); err != nil {
		t.Fatal(err)
	}
	pruneHistory(dir, now)
	os.Chmod(path+".gz", 0o600)
	var left []string
	for _, f := range historyFiles(dir) {
		left = append(left, filepath.Base(f.path))
	}
	days, rs, _ := History(day)
	var got []int64
	for _, r := range rs {
		got = append(got, r.ID)
	}
	if want := []string{filepath.Base(newer)}; !slices.Equal(left, want) || len(got) > 0 {
		t.Errorf("kept %v, days %+v, routes %v; want %v alone: the day dropped whole", left, days, got, want)
	}
}

func TestRouteUsageCompactAndLegacyJSON(t *testing.T) {
	// Ignore fields that full ledger records wrote into earlier route history.
	var old Route
	if err := json.Unmarshal([]byte(`{"usage":[{"t":"0001-01-01T00:00:00Z","agent":"","ms":0,"status":0,"provider":"relay","model":"m","in":20,"out":5,"cache_read":100,"cache_write":40,"reasoning":3}]}`), &old); err != nil {
		t.Fatal(err)
	}
	want := RouteUsage{Provider: "relay", Model: "m", Input: 20, Output: 5, CacheRead: 100, CacheWrite: 40, Reasoning: 3}
	if len(old.Usage) != 1 || old.Usage[0] != want {
		t.Fatalf("legacy accounting: %+v", old.Usage)
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Usage []map[string]json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Usage) != 1 || len(saved.Usage[0]) != 7 {
		t.Fatalf("unexpected accounting fields: %s", data)
	}
	for _, key := range []string{"t", "agent", "ms", "status"} {
		if _, ok := saved.Usage[0][key]; ok {
			t.Fatalf("unneeded field %q: %s", key, data)
		}
	}
}

func TestHistoryRouteOutsideDayLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	for _, at := range []time.Time{yesterday, now} {
		var data strings.Builder
		for i := 1; i <= historyMax+1; i++ {
			b, err := json.Marshal(Route{ID: at.UnixMilli() + int64(i), Time: at, Model: "model", Done: true})
			if err != nil {
				t.Fatal(err)
			}
			data.Write(b)
			data.WriteByte('\n')
		}
		if err := os.MkdirAll(HistoryDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(HistoryDir(), at.Format(dayForm)+".jsonl")
		if err := os.WriteFile(path, []byte(data.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		if at == yesterday {
			gzipFile(path)
		}
		_, rows, cut := History(at.Format(dayForm))
		if !cut || len(rows) != historyMax {
			t.Fatalf("history: %d, cut %v", len(rows), cut)
		}
		id := at.UnixMilli() + 1
		if r, ok := HistoryRoute(id, at); !ok || r.ID != id || r.Model != "model" {
			t.Fatalf("route: %+v, found %v", r, ok)
		}
	}
	if _, ok := HistoryRoute(42, now); ok {
		t.Fatal("found missing route")
	}
}

func TestHistoryRouteDayAndNoLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	os.MkdirAll(HistoryDir(), 0o700)
	path := filepath.Join(HistoryDir(), day.AddDate(0, 0, -1).Format(dayForm)+".jsonl")
	os.WriteFile(path, []byte(`{"id":1234,"model":"wrong"}`+"\n"+`{"id":123,"model":"wanted"}`+"\n"), 0o600)
	gzipFile(path)
	// A lookup must finish even while a history writer holds its mutex.
	history.mu.Lock()
	done := make(chan Route, 1)
	go func() { r, _ := HistoryRoute(123, day); done <- r }()
	select {
	case r := <-done:
		history.mu.Unlock()
		if r.ID != 123 || r.Model != "wanted" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(time.Second):
		history.mu.Unlock()
		t.Fatal("lookup waited for history mutex")
	}
	if _, ok := HistoryRoute(123, day.AddDate(0, 0, -2)); !ok {
		t.Fatal("next day was not checked")
	}
	if _, ok := HistoryRoute(123, day.AddDate(0, 0, 2)); ok {
		t.Fatal("searched outside the three-day window")
	}
}
