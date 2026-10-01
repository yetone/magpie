package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func newCaller(t *testing.T, names ...string) ([]access.Key, []string) {
	t.Helper()
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for _, name := range names {
		secret, err := access.Update("add-key", access.Change{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	keys, err := access.List()
	if err != nil {
		t.Fatal(err)
	}
	return keys[len(keys)-len(names):], secrets
}

func TestCallerUsageAcrossKeys(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("caller credential reached provider")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Laptop", "Server", "Work")
	h := New().Handler()
	call := func(secret, header string) int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		if header == "Authorization" {
			secret = "Bearer " + secret
		}
		r.Header.Set(header, secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i, secret := range secrets {
		header := []string{"Authorization", "x-api-key", "x-goog-api-key"}[i]
		if code := call(secret, header); code != 200 {
			t.Fatal("request", code)
		}
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 3 || recs[0].CallerKeyID != keys[0].ID || recs[1].CallerKeyID != keys[1].ID || recs[2].CallerKeyName != "Work" {
		t.Fatalf("attribution: %+v", recs)
	}
	for _, rec := range recs {
		if rec.Input != 30 || rec.Output != 5 {
			t.Fatal(rec)
		}
	}
	s := usage.Summarize(usage.All)
	if len(s.CallerKeys) != 3 || s.Calls != 3 {
		t.Fatalf("summary: %+v", s)
	}
	rows, totals, _ := usage.Ledger(usage.All, usage.Filter{CallerKey: keys[1].ID})
	if len(rows) != 1 || totals.Input != 30 || rows[0].CallerKeyName != "Server" {
		t.Fatal(rows, totals)
	}
	access.Update("off-key", access.Change{Key: keys[0].ID})
	if code := call(secrets[0], "Authorization"); code != 401 {
		t.Fatal("disabled key", code)
	}
	if code := call(secrets[2], "Authorization"); code != 200 {
		t.Fatal("other key", code)
	}
	access.Update("remove-key", access.Change{Key: keys[2].ID})
	if code := call(secrets[2], "Authorization"); code != 401 {
		t.Fatal("deleted key", code)
	}
	log, _ := os.ReadFile(usage.Path())
	for _, secret := range secrets {
		if strings.Contains(string(log), secret) {
			t.Fatal("caller credential in ledger")
		}
	}
}

func TestManagedLANGuard(t *testing.T) {
	fresh(t)
	keys, secrets := newCaller(t, "Remote")
	shared := settings.Load()
	shared.LAN = true
	if err := settings.Save(shared); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if who.KeyID != keys[0].ID || r.URL.Query().Get("key") != Token {
			t.Fatal("query key or user not normalized", who)
		}
	}))
	r := httptest.NewRequest("POST", "/v1beta/models/m:generateContent?key="+secrets[0], nil)
	r.RemoteAddr = "192.168.1.9:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	access.Update("off-key", access.Change{Key: keys[0].ID})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("disabled remote key", w.Code)
	}
}

func TestMigratedLANKeyUsageAndRevocation(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("caller credential reached provider")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.LAN, s.LANKey = true, "sk-magpie-test-legacy-lan"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := access.MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	keys, err := access.List()
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	h := lanGuard(New().Handler())
	call := func() int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = "192.168.1.9:5000"
		r.Header.Set("Authorization", "Bearer "+s.LANKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(); code != http.StatusOK {
		t.Fatal("migrated remote caller", code)
	}
	rec := lastUsage(t)
	if rec.CallerKeyID != keys[0].ID || rec.CallerKeyName != keys[0].Name || rec.Input != 30 || rec.Output != 5 {
		t.Fatal("remote usage attribution", rec)
	}
	if err := access.ConfigureLAN(true, true); err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusUnauthorized {
		t.Fatal("rotated LAN credential still works", code)
	}
	s.LANKey, err = access.Update("copy-key", access.Change{Key: keys[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusOK || lastUsage(t).CallerKeyID != keys[0].ID {
		t.Fatal("rotation changed remote usage identity", code)
	}
	if _, err := access.Update("remove-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusUnauthorized {
		t.Fatal("revoked old LAN credential still works", code)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 2 {
		t.Fatal("unauthorized request counted as usage", recs)
	}
}

func TestImageUsageIncludesCaller(t *testing.T) {
	s, _ := easeled(t)
	keys, secrets := newCaller(t, "Drawing")
	r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"art/gpt-image-1","prompt":"a bird"}`))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	rec := lastUsage(t)
	if rec.CallerKeyID != keys[0].ID || rec.Input != 7 {
		t.Fatal(rec)
	}
}

func TestCallerIdentitySurvivesStreamingFailover(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer first" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Key: "first", Chat: up.URL + "/v1", Models: []string{"m1"},
		Keys: []provider.KeyAccount{{Key: "backup", Name: "Backup"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Stream")
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal(w.Code, w.Body.String())
	}
	rec := lastUsage(t)
	if rec.CallerKeyID != keys[0].ID || rec.Input != 100 || rec.Output != 20 {
		t.Fatal(rec)
	}
}
