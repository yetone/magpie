package gui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

func TestCallerKeyManagementAndUsageRoutes(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	usageRoutes(mux, folderOnly{})
	type state struct {
		Keys   []access.Key `json:"keys"`
		Secret string       `json:"secret"`
	}
	request := func(method, path, body string, out any) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}
	var s state
	request("POST", "/api/caller-keys/add-key", `{"name":"Laptop"}`, &s)
	secret, laptop := s.Secret, s.Keys[0].ID
	request("POST", "/api/caller-keys/add-key", `{"name":"Server"}`, &s)
	server := s.Keys[1].ID
	list := request("GET", "/api/caller-keys", "", &s)
	if strings.Contains(list, secret) || strings.Contains(list, `"secret"`) {
		t.Fatal("list leaked credentials")
	}
	for _, rec := range []usage.Record{
		{RouteID: 123, ProviderKeyID: "upstream", ProviderKeyName: "Upstream", CallerKeyID: laptop, CallerKeyName: "Laptop", Input: 100},
		{RouteID: 456, CallerKeyID: laptop, CallerKeyName: "Laptop", Input: 10},
		{RouteID: 123, CallerKeyID: server, CallerKeyName: "Server", Input: 200},
		{Input: 40},
	} {
		rec.Time, rec.Provider, rec.Model, rec.Status = time.Now(), "relay", "unknown", 200
		usage.Append(rec)
	}
	request("POST", "/api/caller-keys/rename-key", `{"key":"`+laptop+`","name":"Main"}`, &s)
	var summary usageJSON
	request("GET", "/api/usage?period=all", "", &summary)
	if len(summary.CallerKeys) != 2 || summary.Calls != 4 {
		t.Fatal(summary.CallerKeys)
	}
	for _, g := range summary.CallerKeys {
		if g.CallerKeyID == laptop && g.Name != "Main" {
			t.Fatal(g)
		}
	}
	var ledger ledgerJSON
	query := "?period=all&callerKey=" + laptop
	request("GET", "/api/usage/requests"+query, "", &ledger)
	if ledger.Total != 2 || ledger.Input != 110 || ledger.Rows[0].CallerKeyLabel != "Main" || len(ledger.CallerKeys) != 2 {
		t.Fatal(ledger)
	}
	raw := request("GET", "/api/usage/requests.csv"+query, "", nil)
	cells, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil || len(cells) != 3 || cells[1][slices.Index(usage.CSVHeader, "caller_key_id")] != laptop || cells[2][slices.Index(usage.CSVHeader, "caller_key_id")] != laptop || strings.Contains(raw, secret) {
		t.Fatal("CSV", err, raw)
	}
	request("GET", "/api/usage/requests"+query+"&route=123", "", &ledger)
	if ledger.Total != 1 || ledger.Input != 100 || ledger.Rows[0].RouteID != 123 || ledger.Rows[0].ProviderKeyID != "upstream" || ledger.Rows[0].CallerKeyID != laptop {
		t.Fatal("combined route and caller filter", ledger)
	}
	raw = request("GET", "/api/usage/requests.csv"+query+"&route=123", "", nil)
	cells, err = csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil || len(cells) != 2 || cells[1][slices.Index(usage.CSVHeader, "route_id")] != "123" || cells[1][slices.Index(usage.CSVHeader, "provider_key_id")] != "upstream" || cells[1][slices.Index(usage.CSVHeader, "caller_key_id")] != laptop || strings.Contains(raw, secret) {
		t.Fatal("combined-filter CSV", err, raw)
	}
	request("POST", "/api/caller-keys/rotate-key", `{"key":"`+laptop+`"}`, &s)
	if s.Secret == "" || s.Secret == secret || s.Keys[0].ID != laptop || s.Keys[0].Name != "Main" {
		t.Fatal("rotation lost identity")
	}
	if _, ok := access.Authenticate(secret); ok {
		t.Fatal("rotated key still authenticates")
	}
	request("GET", "/api/usage/requests"+query, "", &ledger)
	if ledger.Total != 2 || ledger.Input != 110 || ledger.Rows[0].CallerKeyLabel != "Main" {
		t.Fatal("rotation lost usage history", ledger)
	}
	request("POST", "/api/caller-keys/remove-key", `{"key":"`+laptop+`"}`, &s)
	ledger = ledgerJSON{}
	request("GET", "/api/usage/requests"+query, "", &ledger)
	if ledger.Total != 2 || ledger.Rows[0].CallerKeyLabel != "Laptop" {
		t.Fatal("historical name", ledger)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/caller-keys/off-key", strings.NewReader(`{"key":"missing"}`)))
	if w.Code != 400 {
		t.Fatal("unknown key accepted", w.Code)
	}
}
