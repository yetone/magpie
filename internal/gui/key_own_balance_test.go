package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key's balance typed by hand (tenacious_eagle on Discord), through the
// GUI's own routes: provider/keyownbalance sets it, /api/providers says
// the provider takes one and what is left of it, saving the editor keeps
// it, and none takes it off.
func TestKeyOwnBalanceRoutes(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".data"))
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	call := func(method, path, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	key := provider.KeyID("sk-a")
	type keyRow struct {
		ID      string                    `json:"id"`
		Balance *provider.BalanceEstimate `json:"balance"`
	}
	listed := func() (bool, *provider.BalanceEstimate) {
		t.Helper()
		var out struct {
			Providers []struct {
				ID              string   `json:"id"`
				TakesOwnBalance bool     `json:"takesOwnBalance"`
				KeyList         []keyRow `json:"keyList"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(call("GET", "/api/providers", "")), &out); err != nil {
			t.Fatal(err)
		}
		for _, p := range out.Providers {
			if p.ID != "relay" {
				continue
			}
			for _, k := range p.KeyList {
				if k.ID == key {
					return p.TakesOwnBalance, k.Balance
				}
			}
		}
		t.Fatal("relay's key not listed")
		return false, nil
	}
	call("POST", "/api/provider/save", `{"id":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","key":"sk-a","new":true}`)
	if takes, b := listed(); !takes || b != nil {
		t.Fatalf("before: takes %v, balance %+v", takes, b)
	}
	call("POST", "/api/provider/keyownbalance", `{"id":"relay","account":"`+key+`","balance":20}`)
	if _, b := listed(); b == nil || b.Set != 20 || b.At.IsZero() {
		t.Fatalf("typed 20: %+v", b)
	}
	call("POST", "/api/provider/save", `{"id":"relay","from":"relay","name":"Relay 2","chat":"http://127.0.0.1:1/v1","key":""}`)
	if _, b := listed(); b == nil || b.Set != 20 {
		t.Fatalf("the editor saved, the balance typed is gone: %+v", b)
	}
	// in yuan, refilled on the 1st; a currency it doesn't know refused
	call("POST", "/api/provider/keyownbalance", `{"id":"relay","account":"`+key+`","balance":700,"currency":"cny"}`)
	call("POST", "/api/provider/keybalancerefill", `{"id":"relay","account":"`+key+`","refill":{"every":"month","day":1}}`)
	if _, b := listed(); b == nil || b.Set != 700 || b.Currency != "cny" || b.Left == nil || *b.Left != 700 || b.Refill == nil || b.Refill.Day != 1 || b.Next == nil {
		t.Fatalf("¥700 refilled on the 1st: %+v", b)
	}
	for _, bad := range []struct{ path, body string }{
		{"keyownbalance", `{"id":"relay","account":"` + key + `","balance":7,"currency":"eur"}`},
		{"keybalancerefill", `{"id":"relay","account":"` + key + `","refill":{"every":"month","day":40}}`},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+bad.path, strings.NewReader(bad.body)))
		if w.Code == 200 {
			t.Errorf("%s %s taken", bad.path, bad.body)
		}
	}
	if _, b := listed(); b.Currency != "cny" || b.Refill.Day != 1 {
		t.Fatalf("a refused change changed it: %+v", b)
	}
	call("POST", "/api/provider/keybalancerefill", `{"id":"relay","account":"`+key+`","refill":null}`)
	if _, b := listed(); b == nil || b.Refill != nil || b.Set != 700 {
		t.Fatalf("refill off: %+v", b)
	}
	// a part that expires (tenacious_eagle_96548): ¥50 through a day far
	// ahead, listed apart and in the left; a date that isn't one refused;
	// none takes it off and leaves the rest
	call("POST", "/api/provider/keyexpiring", `{"id":"relay","account":"`+key+`","expiring":{"amount":50,"until":"2099-12-31"}}`)
	if _, b := listed(); b == nil || b.Set != 700 || *b.Left != 750 || len(b.Expiring) != 1 || *b.Expiring[0].Left != 50 || b.Expiring[0].Ends.Year() != 2100 {
		t.Fatalf("¥700 and ¥50 that expires: %+v %+v", b, b.Expiring)
	}
	// a second grant, sooner: listed first, both in the left; one taken
	// off by when it was typed leaves the other
	call("POST", "/api/provider/keyexpiring", `{"id":"relay","account":"`+key+`","expiring":{"amount":20,"until":"2099-12-30"}}`)
	_, b := listed()
	if b == nil || *b.Left != 770 || len(b.Expiring) != 2 || b.Expiring[0].Set != 20 || b.Expiring[0].Until != "2099-12-30" || b.Expiring[1].Set != 50 {
		t.Fatalf("¥50 and ¥20 that expire: %+v", b)
	}
	at, _ := json.Marshal(b.Expiring[1].At)
	call("POST", "/api/provider/keyexpiring", `{"id":"relay","account":"`+key+`","expiring":{"at":`+string(at)+`,"until":"2099-12-31"},"remove":true}`)
	if _, b := listed(); b == nil || *b.Left != 720 || len(b.Expiring) != 1 || b.Expiring[0].Set != 20 {
		t.Fatalf("¥50 taken off: %+v", b)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/keyexpiring", strings.NewReader(`{"id":"relay","account":"`+key+`","expiring":{"at":`+string(at)+`,"until":"2099-12-31"},"remove":true}`)))
	if w.Code == 200 {
		t.Error("taking off a grant already gone")
	}
	for _, body := range []string{`{"amount":50,"until":"next week"}`, `{"amount":0,"until":"2099-12-31"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/keyexpiring", strings.NewReader(`{"id":"relay","account":"`+key+`","expiring":`+body+`}`)))
		if w.Code == 200 {
			t.Errorf("%s taken", body)
		}
	}
	call("POST", "/api/provider/keyexpiring", `{"id":"relay","account":"`+key+`","expiring":null}`)
	if _, b := listed(); b == nil || len(b.Expiring) != 0 || *b.Left != 700 {
		t.Fatalf("expiring off: %+v", b)
	}
	call("POST", "/api/provider/keyownbalance", `{"id":"relay","account":"`+key+`","balance":null}`)
	if _, b := listed(); b != nil {
		t.Fatalf("taken off: %+v", b)
	}
}
