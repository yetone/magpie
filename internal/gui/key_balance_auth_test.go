package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keys/balance gives one key its own account's access token and user id
// for its balance (Kylin_Cloud on X: keys of two New API accounts in one
// provider). The row tells the page the key has one and its user id,
// never the token; the provider row tells the page its keys take one; a
// Save of the editor keeps it; clear takes it off.
func TestKeysBalanceAuth(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-a", KeyName: "Mine",
		Keys:       []provider.KeyAccount{{Name: "Team", Key: "sk-b"}},
		Headers:    map[string]string{"New-Api-User": "1"},
		BalanceURL: "http://127.0.0.1:1/api/user/self", BalanceToken: "tok-a"}); err != nil {
		t.Fatal(err)
	}
	b := provider.KeyID("sk-b")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := do("POST", "/api/keys/balance", `{"id":"relay","ref":"`+b+`","token":"tok-b","user":"2"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if p, _ := provider.Find("relay"); p.KeyBalanceAuth[b] != (provider.KeyBalanceAuth{Token: "tok-b", User: "2"}) {
		t.Fatalf("saved %+v", p.KeyBalanceAuth)
	}
	w := do("GET", "/api/providers", "")
	if strings.Contains(w.Body.String(), "tok-b") {
		t.Fatal("the key's token went to the page")
	}
	var list struct {
		Providers []struct {
			ID             string `json:"id"`
			KeyBalanceAuth bool   `json:"keyBalanceAuth"`
			KeyList        []struct {
				ID          string                       `json:"id"`
				BalanceAuth *provider.KeyBalanceAuthInfo `json:"balanceAuth"`
			} `json:"keyList"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list.Providers {
		if p.ID != "relay" {
			continue
		}
		found = true
		if !p.KeyBalanceAuth {
			t.Fatal("the provider row doesn't say its keys take a token")
		}
		for _, k := range p.KeyList {
			if (k.ID == b) != (k.BalanceAuth != nil) || k.BalanceAuth != nil && *k.BalanceAuth != (provider.KeyBalanceAuthInfo{User: "2", Token: true}) {
				t.Fatalf("%s: %+v", k.ID, k.BalanceAuth)
			}
		}
	}
	if !found {
		t.Fatalf("no relay in %s", w.Body)
	}
	// the editor's Save, which knows nothing of it, keeps it
	if w := do("POST", "/api/provider/save", `{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","balanceURL":"http://127.0.0.1:1/api/user/self","key":""}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if p, _ := provider.Find("relay"); p.KeyBalanceAuth[b].Token != "tok-b" {
		t.Fatalf("Save dropped it: %+v", p.KeyBalanceAuth)
	}
	// a user id that isn't one, and no token on a key with none, are refused
	for _, body := range []string{`{"id":"relay","ref":"` + b + `","token":"x","user":"two"}`, `{"id":"relay","ref":"Mine","user":"1"}`} {
		if w := do("POST", "/api/keys/balance", body); w.Code == 200 {
			t.Fatalf("%s taken", body)
		}
	}
	if w := do("POST", "/api/keys/balance", `{"id":"relay","ref":"`+b+`","clear":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if p, _ := provider.Find("relay"); p.KeyBalanceAuth != nil {
		t.Fatalf("clear left %+v", p.KeyBalanceAuth)
	}
}
