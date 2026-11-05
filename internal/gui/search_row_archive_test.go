package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// A search magpie asked a search API for (axelburks on Discord: 用量-请求
// 页面 能看到请求记录 但是无请求存档) is a row of the Requests page with the
// route the Routing page lists it under and the archive that keeps its
// request and answer, which the row's detail opens (View routing, the
// archive panel), though it rides the probes' lane.
func TestSearchRowKeepsRouteAndArchive(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	usage.Append(usage.Record{Time: time.Now(), Agent: "claude", Kind: "web_search", Source: usage.SourceSearch, Requested: "Tavily",
		Host: "api.tavily.com", ProviderKeyName: "tvly…ab12", Status: 200, Millis: 800, RouteID: 1793851649094, Archive: "2026-11-05/040654-a02c8d68dfe256d8"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests?period=all", nil))
	var ledger ledgerJSON
	if err := json.Unmarshal(w.Body.Bytes(), &ledger); err != nil || w.Code != 200 {
		t.Fatalf("%d %v %s", w.Code, err, w.Body)
	}
	if len(ledger.Rows) != 1 || ledger.Rows[0].Source != usage.SourceSearch || ledger.Rows[0].RouteID != 1793851649094 ||
		ledger.Rows[0].Archive != "2026-11-05/040654-a02c8d68dfe256d8" {
		t.Fatalf("rows %s", w.Body)
	}
}
