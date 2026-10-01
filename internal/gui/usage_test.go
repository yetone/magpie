package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// The Usage page's Requests: a page of the ledger at a time, newest first,
// with the filters' rows counted on every page, and Export CSV writing all
// of them to Downloads, never over an earlier file.
func TestUsageLedgerRoutes(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	y, m, d := time.Now().Date()
	at := time.Date(y, m, d, 0, 0, 1, 0, time.Local) // today, whenever the test runs
	for i, r := range []usage.Record{
		{RouteID: 123, Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Requested: "sol", Served: "gpt-6-luna", Input: 10, Output: 1, Status: 200},
		{Agent: "claude", Provider: "anthropic", Model: "claude-sonnet-5", Requested: "sonnet", Input: 20, Output: 2, Status: 200},
		{RouteID: 123, Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Requested: "sol", Status: 429},
	} {
		r.Time = at.Add(time.Duration(i) * time.Second)
		usage.Append(r)
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(q string) ledgerJSON {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests?"+q, nil))
		var l ledgerJSON
		if err := json.Unmarshal(w.Body.Bytes(), &l); w.Code != 200 || err != nil {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		return l
	}

	l := get("period=all&route=123")
	if l.Total != 2 || l.Rows[0].RouteID != 123 || l.Rows[1].RouteID != 123 {
		t.Fatalf("route attempts: %+v", l)
	}
	if l = get("period=all&route=999"); l.Total != 0 {
		t.Fatalf("missing route: %+v", l)
	}
	l = get("period=today&offset=1&limit=1")
	if l.Total != 3 || l.Offset != 1 || len(l.Rows) != 1 || l.Rows[0].Requested != "sonnet" {
		t.Fatalf("second page: %+v", l)
	}
	if len(l.Agents) != 2 || l.Calls != 3 || l.Errors != 1 {
		t.Fatalf("agents and totals: %+v", l)
	}
	l = get("period=today")
	if len(l.Rows) != 3 || l.Rows[0].Status != 429 || !l.Rows[2].Swapped || l.Rows[2].Served != "gpt-6-luna" {
		t.Fatalf("newest first, the swap marked: %+v", l.Rows)
	}
	if l = get("period=today&failed=1"); l.Total != 1 || l.Rows[0].Status != 429 {
		t.Fatalf("failed only: %+v", l)
	}
	if l = get("period=today&agent=claude"); l.Total != 1 || l.Rows[0].Agent != "claude" {
		t.Fatalf("one agent: %+v", l)
	}
	if l = get("period=today&q=LUNA"); l.Total != 1 || l.Rows[0].Served != "gpt-6-luna" {
		t.Fatalf("search: %+v", l)
	}
	if l = get("period=today&offset=99"); l.Total != 3 || len(l.Rows) != 0 {
		t.Fatalf("past the end: %+v", l)
	}

	export := func() (string, int) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/usage/requests/export?period=today&agent=codex", strings.NewReader("{}")))
		var out struct {
			Path string
			Rows int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil {
			t.Fatalf("export: %d %s", w.Code, w.Body)
		}
		return out.Path, out.Rows
	}
	p1, n := export()
	p2, _ := export()
	day := time.Now().Format("2006-01-02")
	if n != 2 || p1 != "~/Downloads/magpie-requests-today-"+day+".csv" || p2 != "~/Downloads/magpie-requests-today-"+day+"-2.csv" {
		t.Fatalf("export: %s %s %d", p1, p2, n)
	}
	b, err := os.ReadFile(filepath.Join(home, "Downloads", "magpie-requests-today-"+day+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "time,agent,requested_model,") || !strings.Contains(lines[2], ",sol,relay,,gpt-6-sol,gpt-6-luna,true,") {
		t.Fatalf("csv:\n%s", b)
	}
}
