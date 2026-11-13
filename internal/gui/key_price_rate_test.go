package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A save's keyPriceRates (@tnt12231 on X): each key's own rate is taken
// and /api/providers gives it back; left out (an editor of one key, or the
// CLI's own) they are kept; {} takes them all off; a rate of 0 or out of
// range is refused and nothing is written.
func TestProviderSaveKeyPriceRates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-half", Chat: "http://127.0.0.1:1/v1",
		Keys: []provider.KeyAccount{{Name: "Premium", Key: "sk-premium"}}}); err != nil {
		t.Fatal(err)
	}
	half, premium := provider.KeyID("sk-half"), provider.KeyID("sk-premium")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(extra string) int {
		t.Helper()
		w := httptest.NewRecorder()
		body := `{"id":"relay","from":"relay","name":"Relay","key":"","chat":"http://127.0.0.1:1/v1","icon":"generic","models":[],"headers":{}` + extra + `}`
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w.Code
	}
	listed := func() map[string]float64 {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID            string             `json:"id"`
				KeyPriceRates map[string]float64 `json:"keyPriceRates"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, w.Body)
		}
		for _, p := range got.Providers {
			if p.ID == "relay" {
				return p.KeyPriceRates
			}
		}
		t.Fatal("no relay")
		return nil
	}
	if c := post(`,"keyPriceRates":{"` + half + `":0.5,"` + premium + `":1.5}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 2 || got[half] != 0.5 || got[premium] != 1.5 {
		t.Fatalf("listed %v", got)
	}
	if c := post(""); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 2 || got[half] != 0.5 {
		t.Fatalf("left out, kept: %v", got)
	}
	for _, bad := range []string{"0", "-1", "1001", "0.1234"} {
		if c := post(`,"keyPriceRates":{"` + half + `":` + bad + `}`); c == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
	if got := listed(); len(got) != 2 || got[half] != 0.5 {
		t.Fatalf("a refused save changed them: %v", got)
	}
	if c := post(`,"keyPriceRates":{}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("{} took them off: %v", got)
	}
}
