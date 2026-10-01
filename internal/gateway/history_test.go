package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A done route is kept on disk by its day, a day that is over gzipped and
// still read, and a day too old or past the size kept dropped.
func TestHistoryKeepsDaysAndDropsOld(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
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
