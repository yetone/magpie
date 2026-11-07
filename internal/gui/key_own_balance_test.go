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
	call("POST", "/api/provider/keyownbalance", `{"id":"relay","account":"`+key+`","balance":null}`)
	if _, b := listed(); b != nil {
		t.Fatalf("taken off: %+v", b)
	}
}
