package gui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// The Usage page's routes read days and hours in the zone the page names
// in ?tz=, the browser's, not the computer's: `magpie web` read from UTC+8
// showed a call at 9:00 under 1:00 (Outvi V on Discord). Without ?tz= the
// computer's, as the tray and the TUI read them. Run under TZ=UTC and
// TZ=Asia/Shanghai.
func TestUsageRoutesReadTheViewersZone(t *testing.T) {
	sandboxHome(t)
	// a reader's zone v, and a moment and a call before it on one day in v
	// and on two in the computer's zone
	var v *time.Location
	var now, call time.Time
	day := func(at time.Time, l *time.Location) string { return at.In(l).Format(time.DateOnly) }
find:
	for _, name := range []string{"Asia/Shanghai", "America/Los_Angeles"} {
		v = usage.Zone(name)
		for n := 0; n < 48; n++ {
			now = time.Date(2026, 9, 30, n, 0, 0, 0, time.UTC)
			for h := 1; h < 24; h++ {
				call = now.Add(-time.Duration(h) * time.Hour)
				if day(call, v) == day(now, v) && day(call, time.Local) != day(now, time.Local) {
					break find
				}
			}
		}
		v = nil
	}
	if v == nil {
		t.Fatalf("no zone here whose day differs from the computer's (%v)", time.Local)
	}
	old := usage.Clock
	usage.Clock = func() time.Time { return now.In(time.Local) }
	t.Cleanup(func() { usage.Clock = old })
	usage.Append(usage.Record{Time: call, Agent: "claude", Provider: "relay", Model: "unknown", Input: 10, Status: 200})

	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(path string, out any) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(path, err)
			}
		}
		return w.Body.String()
	}
	tz := "&tz=" + url.QueryEscape(v.String())
	hour, date := call.In(v).Format("15"), day(call, v)

	var s usageJSON
	get("/api/usage?period=today"+tz, &s)
	at := ""
	for _, p := range s.Series {
		if p.Calls > 0 {
			at = p.Label
		}
	}
	if s.Calls != 1 || at != hour {
		t.Errorf("/api/usage in %v: %d calls at %q, want 1 at %q", v, s.Calls, at, hour)
	}
	s = usageJSON{}
	if get("/api/usage?period=today", &s); s.Calls != 0 {
		t.Errorf("/api/usage in the computer's zone: %d calls, want 0", s.Calls)
	}

	var l ledgerJSON
	get("/api/usage/requests?period=today"+tz, &l)
	at = ""
	for _, p := range l.Series {
		if p.Calls > 0 {
			at = p.Label
		}
	}
	if l.Total != 1 || at != hour {
		t.Errorf("/api/usage/requests in %v: %d rows at %q, want 1 at %q", v, l.Total, at, hour)
	}
	l = ledgerJSON{}
	if get("/api/usage/requests?period=all&day="+date+tz, &l); l.Total != 1 {
		t.Errorf("/api/usage/requests on the reader's %s: %d rows, want 1", date, l.Total)
	}

	var h usage.Heatmap
	get("/api/usage/heatmap?period=all"+tz, &h)
	if h.To != day(now, v) || len(h.Days) != 1 || h.Days[0].Date != date {
		t.Errorf("/api/usage/heatmap in %v: to %s, %+v; want to %s, the call on %s", v, h.To, h.Days, day(now, v), date)
	}

	cells, err := csv.NewReader(strings.NewReader(get("/api/usage/requests.csv?period=today"+tz, nil))).ReadAll()
	if err != nil || len(cells) != 2 {
		t.Errorf("CSV of the reader's today: %v, %d lines, want a row under the header", err, len(cells))
	}
}
