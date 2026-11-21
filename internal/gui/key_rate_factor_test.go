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

// A save's keyRateFactors (@YIXIE233 on X): each key's factor on its read
// rate is taken and /api/providers gives it back; left out they are kept;
// {} takes them all off; one of 0, out of range or past four decimals is
// refused and nothing is written.
func TestProviderSaveKeyRateFactors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-cheap", Chat: "http://127.0.0.1:1/v1", TopUpRate: 0.07,
		Keys: []provider.KeyAccount{{Name: "Pro", Key: "sk-pro"}}}); err != nil {
		t.Fatal(err)
	}
	cheap, pro := provider.KeyID("sk-cheap"), provider.KeyID("sk-pro")
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
				ID             string             `json:"id"`
				KeyRateFactors map[string]float64 `json:"keyRateFactors"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, w.Body)
		}
		for _, p := range got.Providers {
			if p.ID == "relay" {
				return p.KeyRateFactors
			}
		}
		t.Fatal("no relay")
		return nil
	}
	if c := post(`,"keyRateFactors":{"` + cheap + `":2.5,"` + pro + `":0.6667}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 2 || got[cheap] != 2.5 || got[pro] != 0.6667 {
		t.Fatalf("listed %v", got)
	}
	if c := post(""); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 2 || got[cheap] != 2.5 {
		t.Fatalf("left out, kept: %v", got)
	}
	for _, bad := range []string{"0", "-1", "1001", "1.23456", `"x"`} {
		if c := post(`,"keyRateFactors":{"` + cheap + `":` + bad + `}`); c == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
	if got := listed(); len(got) != 2 || got[cheap] != 2.5 {
		t.Fatalf("a refused save changed them: %v", got)
	}
	if c := post(`,"keyRateFactors":{}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("{} took them off: %v", got)
	}
}
