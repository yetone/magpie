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

// A save's topUpRate (@YIXIE233 on X): taken and listed back, kept when
// the save leaves it out, taken off with null, and refused out of range
// or past four decimals with nothing written.
func TestProviderSaveTopUpRate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-a", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(extra string) int {
		t.Helper()
		w := httptest.NewRecorder()
		body := `{"id":"relay","from":"relay","name":"Relay","key":"","chat":"http://127.0.0.1:1/v1","icon":"generic","models":[],"headers":{}` + extra + `}`
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w.Code
	}
	listed := func() float64 {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID        string  `json:"id"`
				TopUpRate float64 `json:"topUpRate"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, w.Body)
		}
		for _, p := range got.Providers {
			if p.ID == "relay" {
				return p.TopUpRate
			}
		}
		t.Fatal("no relay")
		return 0
	}
	if c := post(`,"topUpRate":0.07`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if got := listed(); got != 0.07 {
		t.Fatalf("listed %v", got)
	}
	if c := post(""); c != 200 || listed() != 0.07 {
		t.Fatalf("left out, not kept: %d %v", c, listed())
	}
	for _, bad := range []string{"-1", "1001", "0.07142", `"abc"`} {
		if c := post(`,"topUpRate":` + bad); c == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
	if got := listed(); got != 0.07 {
		t.Fatalf("a refused save changed it: %v", got)
	}
	if c := post(`,"topUpRate":null`); c != 200 || listed() != 0 {
		t.Fatalf("null didn't take it off: %d %v", c, listed())
	}
}
