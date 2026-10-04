package gui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

func TestUsagePurposeRoutes(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := usage.Today.Since(time.Now()).Add(time.Second)
	for i, kind := range []string{"thread_title", "thread_title_reconsideration", "review", "", "future_kind", "title_generation"} {
		usage.Append(usage.Record{Time: start.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "m", Kind: kind, Input: 10, Output: 2, Status: 200})
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	q := url.Values{"period": {"today"}, "purpose": {"kind:thread_title"}, "limit": {"1"}, "offset": {"1"}, "agent": {"codex"}}
	get := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path+"?"+q.Encode(), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w
	}
	var page ledgerJSON
	if err := json.Unmarshal(get("GET", "/api/usage/requests").Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Calls != 3 || page.Input != 30 || page.Offset != 1 || len(page.Rows) != 1 || page.Rows[0].Kind != "thread_title_reconsideration" || len(page.Purposes) != 4 {
		t.Fatalf("purpose page: %+v", page)
	}
	var chartCalls int
	for _, p := range page.Series {
		chartCalls += p.Calls
	}
	if chartCalls != 3 {
		t.Fatalf("chart ignored purpose: %d", chartCalls)
	}
	csvData := get("GET", "/api/usage/requests.csv").Body.String()
	rows, err := csv.NewReader(strings.NewReader(csvData)).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("CSV must contain all filtered pages: %v %s", err, csvData)
	}
	col := slices.Index(rows[0], "kind")
	if col < 0 {
		t.Fatalf("CSV kind column missing: %v", rows[0])
	}
	for _, row := range rows[1:] {
		if usage.PurposeOf(row[col]) != "kind:thread_title" {
			t.Fatalf("CSV contains another purpose: %v", row)
		}
	}
	var exported struct {
		Path string
		Rows int
	}
	if err := json.Unmarshal(get("POST", "/api/usage/requests/export").Body.Bytes(), &exported); err != nil || exported.Rows != 3 {
		t.Fatalf("export: %+v %v", exported, err)
	}
	b, err := os.ReadFile(filepath.Join(home, "Downloads", filepath.Base(exported.Path)))
	if err != nil || string(b) != csvData {
		t.Fatalf("desktop export differs from filtered download: %v", err)
	}
}
