package gui

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// The Routing stage names a request by the gateway key it came with
// (acezan on Discord: a script's requests read "Python-urllib", its
// User-Agent). A day gone by is drawn from /api/gateway/history, so a kept
// route keeps the key's id and name through the GUI's priced routes, as
// the live trace does (TestTraceNamesCallerKey); a route with no key names
// none, and its agent stays the User-Agent's product name.
func TestKeptRoutesNameTheirCallerKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := keptMux(t)
	traceRoutes(mux)

	dir := gateway.HistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	var b bytes.Buffer
	for i, key := range [][2]string{{"k-nightly", "Nightly job"}, {"", ""}} {
		r := ctxRoute(int64(i+1), "Python-urllib", "", 3000, 100, 200000, 0)
		r.Time = now.Add(-time.Duration(2-i) * time.Minute)
		r.CallerKey, r.CallerKeyName = key[0], key[1]
		j, _ := json.Marshal(r)
		b.Write(append(j, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, day+".jsonl"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/gateway/history?day="+day, nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var hist struct {
		Routes []map[string]any `json:"routes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Routes) != 2 {
		t.Fatalf("routes: %s", rec.Body)
	}
	by := map[float64]map[string]any{}
	for _, r := range hist.Routes {
		by[r["id"].(float64)] = r
	}
	if r := by[1]; r["callerKey"] != "k-nightly" || r["callerKeyName"] != "Nightly job" || r["agent"] != "Python-urllib" {
		t.Fatalf("the keyed route doesn't name its key: %v", r)
	}
	if r := by[2]; r["callerKey"] != nil || r["callerKeyName"] != nil || r["agent"] != "Python-urllib" {
		t.Fatalf("the route with no key names one: %v", r)
	}
}
