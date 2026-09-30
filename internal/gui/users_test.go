package gui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		{CallerKeyID: laptop, CallerKeyName: "Laptop", KeyID: "upstream-a", Input: 100},
		{CallerKeyID: laptop, CallerKeyName: "Laptop", KeyID: "upstream-b", Input: 10},
		{CallerKeyID: server, CallerKeyName: "Server", KeyID: "upstream-a", Input: 200},
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
	if err != nil || len(cells) != 3 || cells[1][23] != laptop || cells[2][23] != laptop || strings.Contains(raw, secret) {
		t.Fatal("CSV", err, raw)
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
