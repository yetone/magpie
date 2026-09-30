package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrokWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing" || r.URL.Query().Get("format") != "credits" || r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"config":{"creditUsagePercent":42.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:17:59.504011+00:00","end":"2026-09-28T02:17:59.504011+00:00"},"onDemandCap":{"val":2000},"onDemandUsed":{"val":500},"billingPeriodEnd":"2026-09-28T02:17:59.504011+00:00"}}`))
	}))
	defer srv.Close()
	defer func(b string) { GrokBase = b }(GrokBase)
	GrokBase = srv.URL

	ws, err := grokWindows(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].Name != "7 days" || ws[0].Used != 42.5 || ws[0].ResetsAt == nil || ws[0].ResetsAt.Day() != 28 {
		t.Fatalf("period = %+v", ws)
	}
	if ws[1].Name != "On-demand" || ws[1].Used != 25 {
		t.Fatalf("on-demand = %+v", ws[1])
	}
}

// An untouched allowance leaves creditUsagePercent out, and no on-demand cap
// means no on-demand meter.
func TestGrokWindowsUnused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","end":"2026-10-01T00:00:00+00:00"},"onDemandCap":{"val":0}}}`))
	}))
	defer srv.Close()
	defer func(b string) { GrokBase = b }(GrokBase)
	GrokBase = srv.URL

	ws, err := grokWindows(context.Background(), "tok")
	if err != nil || len(ws) != 1 || ws[0].Name != "Month" || ws[0].Used != 0 || ws[0].ResetsAt == nil {
		t.Fatalf("windows = %+v, %v", ws, err)
	}
}
