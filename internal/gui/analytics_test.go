package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestAnalyticsRoutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Create a provider without reading the user's catalog cache.
	p := provider.Provider{
		ID:   "testprov",
		Name: "TestProv",
		Chat: "https://test.example/v1",
		Key: "synthetic-only",
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	// Seed usage records to usage.jsonl
	now := time.Now()
	// Anchor both records inside today's window: relative offsets would move
	// them to yesterday when the test runs shortly after midnight.
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	r1 := usage.Record{
		Time:       day.Add(5 * time.Minute),
		Agent:      "codex",
		Provider:   "testprov",
		Model:      "m1",
		Input:      1000,
		Output:     500,
		CacheRead:  200,
		CacheWrite: 100,
		TTFT:       200,
		Millis:     1200,
		Status:     200,
	}
	r2 := usage.Record{
		Time:     day.Add(10 * time.Minute),
		Agent:    "claude",
		Provider: "testprov",
		Model:    "m2",
		Status:   500,
	}
	usage.Append(r1)
	usage.Append(r2)

	mux := http.NewServeMux()
	analyticsRoutes(mux)

	// 1. Test GET /api/analytics
	req := httptest.NewRequest("GET", "/api/analytics?period=today", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data usage.AnalyticsData
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("decode analytics: %v", err)
	}

	if data.Summary.Calls != 2 {
		t.Fatalf("expected 2 calls, got %d", data.Summary.Calls)
	}
	if data.Summary.ServerErr != 1 {
		t.Fatalf("expected 1 server_err, got %d", data.Summary.ServerErr)
	}
	if len(data.Filters.Model) != 2 {
		t.Fatalf("expected 2 models in filters, got %v", data.Filters.Model)
	}

	// 2. Test GET /api/analytics/calls valid chart_id
	reqCalls := httptest.NewRequest("GET", "/api/analytics/calls?period=today&chart_id=1.1", nil)
	recCalls := httptest.NewRecorder()
	mux.ServeHTTP(recCalls, reqCalls)

	if recCalls.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recCalls.Code, recCalls.Body.String())
	}

	var callsResp struct {
		Calls []usage.CallWithCost `json:"calls"`
	}
	if err := json.NewDecoder(recCalls.Body).Decode(&callsResp); err != nil {
		t.Fatalf("decode calls: %v", err)
	}
	if len(callsResp.Calls) != 1 || callsResp.Calls[0].Status != 500 {
		t.Fatalf("expected 1 error call, got %+v", callsResp.Calls)
	}

	// 3. Test GET /api/analytics/calls invalid chart_id -> 400
	reqBad := httptest.NewRequest("GET", "/api/analytics/calls?period=today&chart_id=invalid", nil)
	recBad := httptest.NewRecorder()
	mux.ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid chart_id, got %d", recBad.Code)
	}
}
