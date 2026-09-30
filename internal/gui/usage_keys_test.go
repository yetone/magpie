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

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestUsageKeyRoutes(t *testing.T) {
	sandboxHome(t)
	personal, team := provider.KeyID("personal-secret"), provider.KeyID("team-secret")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1",
		Key: "personal-secret", KeyName: "Personal", Keys: []provider.KeyAccount{{Name: "Team", Key: "team-secret"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, r := range []usage.Record{
		{Time: now, Provider: "relay", Agent: "codex", Model: "unknown", Input: 9, Status: 200},
		{Time: now, Provider: "relay", Agent: "codex", Model: "unknown", KeyID: personal, KeyName: "Old name", Input: 100, Output: 10, Status: 200},
		{Time: now, Provider: "relay", Agent: "claude", Model: "unknown", KeyID: team, KeyName: "Team", Input: 200, Output: 20, Status: 200},
		{Time: now, Provider: "relay", Agent: "codex", Model: "unknown", KeyID: team, KeyName: "Team", Status: 429},
	} {
		usage.Append(r)
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(path string, out any) string {
		t.Helper()
		if ledger, ok := out.(*ledgerJSON); ok {
			*ledger = ledgerJSON{}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "personal-secret") || strings.Contains(w.Body.String(), "team-secret") {
			t.Fatal("usage API leaked a key")
		}
		return w.Body.String()
	}
	var summary usageJSON
	get("/api/usage?period=all", &summary)
	if len(summary.Keys) != 3 || summary.Calls != 4 || summary.Input != 309 {
		t.Fatalf("summary: %+v", summary)
	}
	if summary.Keys[0].Name != "Team" || summary.Keys[1].Name != "Personal" || summary.Keys[2].Name != "Key not recorded" {
		t.Fatalf("key labels: %+v", summary.Keys)
	}
	var ledger ledgerJSON
	query := "period=all&key=" + url.QueryEscape("relay#"+team)
	get("/api/usage/requests?"+query+"&agent=claude", &ledger)
	if ledger.Total != 1 || ledger.Input != 200 || len(ledger.Keys) != 3 || ledger.Rows[0].KeyLabel != "Team" {
		t.Fatalf("filtered rows and all choices: %+v", ledger)
	}
	get("/api/usage/requests?"+query+"&failed=1", &ledger)
	if ledger.Total != 1 || ledger.Errors != 1 || ledger.Rows[0].KeyID != team {
		t.Fatalf("failed key call: %+v", ledger)
	}
	get("/api/usage/requests?period=all&key="+url.QueryEscape("relay#"), &ledger)
	if ledger.Total != 1 || ledger.Rows[0].KeyID != "" || ledger.Rows[0].KeyLabel != "Key not recorded" {
		t.Fatalf("legacy usage: %+v", ledger)
	}
	get("/api/usage/requests?period=all&key="+url.QueryEscape("relay#missing"), &ledger)
	if ledger.Total != 0 || ledger.Calls != 0 || len(ledger.Keys) != 3 {
		t.Fatalf("unknown key: %+v", ledger)
	}
	if err := provider.RemoveKey("relay", team); err != nil {
		t.Fatal(err)
	}
	get("/api/usage/requests?"+query+"&limit=1&offset=1", &ledger)
	if ledger.Total != 2 || ledger.Calls != 2 || ledger.Input != 200 || len(ledger.Rows) != 1 || ledger.Rows[0].KeyLabel != "Team" {
		t.Fatalf("removed key and pagination: %+v", ledger)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests.csv?"+query, nil))
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if w.Code != 200 || err != nil || len(rows) != 3 {
		t.Fatalf("filtered CSV: %v, %s", err, w.Body)
	}
	for _, row := range rows[1:] {
		if row[21] != team || row[22] != "Team" {
			t.Fatalf("CSV key: %v", row)
		}
	}
}
