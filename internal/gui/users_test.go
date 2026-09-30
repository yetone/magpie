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

func TestUserManagementAndUsageRoutes(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	userRoutes(mux)
	usageRoutes(mux, folderOnly{})
	type state struct {
		Users  []access.User `json:"users"`
		Secret string        `json:"secret"`
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
	request("POST", "/api/users/add-user", `{"name":"Alice"}`, &s)
	alice := s.Users[0].ID
	request("POST", "/api/users/add-key", `{"user":"`+alice+`","name":"Laptop"}`, &s)
	secret, laptop := s.Secret, s.Users[0].Keys[0].ID
	request("POST", "/api/users/add-key", `{"user":"`+alice+`","name":"Server"}`, &s)
	server := s.Users[0].Keys[1].ID
	request("POST", "/api/users/add-user", `{"name":"Bob"}`, &s)
	bob := s.Users[1].ID
	request("POST", "/api/users/add-key", `{"user":"`+bob+`","name":"Work"}`, &s)
	work := s.Users[1].Keys[0].ID
	list := request("GET", "/api/users", "", &s)
	if strings.Contains(list, secret) || strings.Contains(list, `"secret"`) {
		t.Fatal("list leaked credentials")
	}
	for _, rec := range []usage.Record{
		{UserID: alice, UserName: "Alice", CallerKeyID: laptop, CallerKeyName: "Laptop", Input: 100},
		{UserID: alice, UserName: "Alice", CallerKeyID: server, CallerKeyName: "Server", Input: 200},
		{UserID: bob, UserName: "Bob", CallerKeyID: work, CallerKeyName: "Work", Input: 300},
		{Input: 40},
	} {
		rec.Time, rec.Provider, rec.Model, rec.Status = time.Now(), "relay", "unknown", 200
		usage.Append(rec)
	}
	request("POST", "/api/users/rename-user", `{"user":"`+alice+`","name":"Alice renamed"}`, &s)
	request("POST", "/api/users/rename-key", `{"user":"`+alice+`","key":"`+laptop+`","name":"Main"}`, &s)
	var summary usageJSON
	request("GET", "/api/usage?period=all", "", &summary)
	if len(summary.Users) != 3 || len(summary.CallerKeys) != 3 || summary.Calls != 4 {
		t.Fatal(summary.Users, summary.CallerKeys)
	}
	for _, g := range summary.CallerKeys {
		if g.CallerKeyID == laptop && (g.Name != "Main" || g.Sub != "Alice renamed") {
			t.Fatal("current names", g)
		}
	}
	var ledger ledgerJSON
	query := "?period=all&user=" + alice + "&callerKey=" + laptop
	request("GET", "/api/usage/requests"+query, "", &ledger)
	if ledger.Total != 1 || ledger.Input != 100 || ledger.Rows[0].UserLabel != "Alice renamed" || ledger.Rows[0].CallerKeyLabel != "Main" ||
		len(ledger.Users) != 3 || len(ledger.CallerKeys) != 3 {
		t.Fatal(ledger)
	}
	raw := request("GET", "/api/usage/requests.csv"+query, "", nil)
	cells, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil || len(cells) != 2 || cells[1][23] != alice || cells[1][25] != laptop || strings.Contains(raw, secret) {
		t.Fatal("CSV", err, raw)
	}
	request("POST", "/api/users/remove-user", `{"user":"`+alice+`"}`, &s)
	ledger = ledgerJSON{}
	request("GET", "/api/usage/requests"+query, "", &ledger)
	if ledger.Total != 1 || ledger.Rows[0].UserLabel != "Alice" || ledger.Rows[0].CallerKeyLabel != "Laptop" {
		t.Fatal("deleted user's history", ledger)
	}
	ledger = ledgerJSON{}
	request("GET", "/api/usage/requests?period=all&user=-", "", &ledger)
	if ledger.Total != 1 || ledger.Input != 40 {
		t.Fatal("unassigned calls", ledger)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/users/add-key", strings.NewReader(`{"user":"missing","name":"Wrong"}`)))
	if w.Code != 400 {
		t.Fatal("unknown user accepted", w.Code)
	}
}
