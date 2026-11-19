package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// keys/import adds the keys pasted at once (361 on Discord), says how many
// were new, and a key the gateway rests says so in its row.
func TestKeysImportAndRest(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	p := provider.Provider{ID: "import-test", Name: "Import", Chat: "https://example.invalid/v1", Key: "first"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(10 * time.Minute)
	// the vendor's words go with the rest, for the row to show beside
	// "400 · out of credit" (@YsiCing on X: the row said only that)
	said := "Your credit balance is too low to access the API"
	was := keyRestOf
	keyRestOf = func(key string) (gateway.Rest, bool) {
		if key == p.ID+"#"+provider.KeyID("two") {
			return gateway.Rest{Why: "credit", Status: 400, Until: until, By: "credit", Error: said}, true
		}
		return gateway.Rest{}, false
	}
	defer func() { keyRestOf = was }()
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/keys/import", strings.NewReader(`{"id":"import-test","key":"first\ntwo, three\n\n'four'"}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Added != 3 || st.Had != 1 {
		t.Fatalf("added %d had %d", st.Added, st.Had)
	}
	var info *providerJSON
	for i := range st.Providers {
		if st.Providers[i].ID == p.ID {
			info = &st.Providers[i]
		}
	}
	if info == nil || len(info.KeyList) != 4 {
		t.Fatalf("keys: %+v", info)
	}
	for i, k := range info.KeyList {
		if (k.Rest != nil) != (i == 1) {
			t.Fatalf("key %d rest %+v", i, k.Rest)
		}
	}
	if r := info.KeyList[1].Rest; r.Status != 400 || r.Why != "credit" || !r.Until.Equal(until) || r.Error != said || r.By != "credit" {
		t.Fatalf("rest %+v", r)
	}
	for _, k := range []string{`"first"`, `"two"`, `"three"`, `"four"`} {
		if strings.Contains(w.Body.String(), `"key":`+k) {
			t.Fatal("leaked a key")
		}
	}
	// pasting only keys it has is refused, saying so
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/keys/import", strings.NewReader(`{"id":"import-test","key":"two three"}`)))
	if w.Code == 200 || !strings.Contains(w.Body.String(), "already has") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// @YsiCing on X: a key the gateway rested showed "暂停使用 · 400 余额不足 ·
// 13:37 恢复" and nothing of what its vendor answered, where an account's
// row shows it. A real refusal, from a fake upstream through the gateway,
// reaches the key's row in /api/providers with the vendor's words.
func TestKeyRestSaysVendorWords(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	const said = "Your credit balance is too low to access the API. Please go to Plans & Billing to upgrade or purchase credits."
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"`+said+`","type":"invalid_request_error","code":null}}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "keyrest-words", Name: "Relay", Chat: up.URL + "/v1", Models: []string{"m1"}, Key: "k-one", Keys: []provider.KeyAccount{{Key: "k-two"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := gateway.New()
	prev := served.Swap(gw)
	t.Cleanup(func() { served.Store(prev) })
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"keyrest-words/m1","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	var st providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	rested := 0
	for _, pj := range st.Providers {
		if pj.ID != p.ID {
			continue
		}
		for _, k := range pj.KeyList {
			if k.Rest == nil {
				continue
			}
			rested++
			if k.Rest.Status != 400 || k.Rest.Why != "credit" || !strings.Contains(k.Rest.Error, said) {
				t.Fatalf("key %s rest %+v, want its vendor's words", k.Masked, k.Rest)
			}
		}
	}
	// the first key rests; the last one left is rested only on a 429 or
	// 402 (quotaStatus), so a 400 leaves it be
	if rested == 0 {
		t.Fatalf("no key rested: %s", w.Body)
	}
}
