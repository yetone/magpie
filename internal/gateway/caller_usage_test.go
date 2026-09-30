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
	"github.com/yetone/magpie/internal/usage"
)

func newCaller(t *testing.T, name string, keyNames ...string) (access.User, []string) {
	t.Helper()
	if _, err := access.Update("add-user", access.Change{Name: name}); err != nil {
		t.Fatal(err)
	}
	users, _ := access.List()
	u := users[len(users)-1]
	var secrets []string
	for _, name := range keyNames {
		secret, err := access.Update("add-key", access.Change{User: u.ID, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	users, _ = access.List()
	return users[len(users)-1], secrets
}

func TestCallerUsageAcrossUsersAndKeys(t *testing.T) {
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
	alice, aKeys := newCaller(t, "Alice", "Laptop", "Server")
	bob, bKeys := newCaller(t, "Bob", "Work")
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
	for i, secret := range append(aKeys, bKeys...) {
		header := []string{"Authorization", "x-api-key", "x-goog-api-key"}[i]
		if code := call(secret, header); code != 200 {
			t.Fatal("request", code)
		}
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 3 || recs[0].UserID != alice.ID || recs[1].UserID != alice.ID || recs[2].UserID != bob.ID ||
		recs[0].CallerKeyID == recs[1].CallerKeyID || recs[2].CallerKeyName != "Work" {
		t.Fatalf("attribution: %+v", recs)
	}
	for _, rec := range recs {
		if rec.KeyID != provider.KeyID("upstream-secret") || rec.Input != 30 || rec.Output != 5 {
			t.Fatal(rec)
		}
	}
	s := usage.Summarize(usage.All)
	if len(s.Users) != 2 || len(s.CallerKeys) != 3 || s.Calls != 3 || s.Users[0].Calls != 2 {
		t.Fatalf("summary: %+v", s)
	}
	rows, totals, _ := usage.Ledger(usage.All, usage.Filter{User: alice.ID, CallerKey: alice.Keys[1].ID})
	if len(rows) != 1 || totals.Input != 30 || rows[0].CallerKeyName != "Server" {
		t.Fatal(rows, totals)
	}
	access.Update("off-user", access.Change{User: alice.ID})
	if code := call(aKeys[0], "Authorization"); code != 401 {
		t.Fatal("disabled user", code)
	}
	if code := call(bKeys[0], "Authorization"); code != 200 {
		t.Fatal("other user", code)
	}
	access.Update("remove-key", access.Change{User: bob.ID, Key: bob.Keys[0].ID})
	if code := call(bKeys[0], "Authorization"); code != 401 {
		t.Fatal("deleted key", code)
	}
	log, _ := os.ReadFile(usage.Path())
	for _, secret := range append(aKeys, bKeys...) {
		if strings.Contains(string(log), secret) {
			t.Fatal("caller credential in ledger")
		}
	}
}

func TestManagedLANGuard(t *testing.T) {
	fresh(t)
	u, secrets := newCaller(t, "LAN user", "Remote")
	legacy := "legacy-shared-secret"
	lanKey.Store(&legacy)
	t.Cleanup(func() { empty := ""; lanKey.Store(&empty) })
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if who.UserID != u.ID || r.URL.Query().Get("key") != Token {
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
	access.Update("off-key", access.Change{User: u.ID, Key: u.Keys[0].ID})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("disabled remote key", w.Code)
	}
}

func TestImageUsageIncludesCaller(t *testing.T) {
	s, _ := easeled(t)
	u, secrets := newCaller(t, "Artist", "Drawing")
	r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"art/gpt-image-1","prompt":"a bird"}`))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	rec := lastUsage(t)
	if rec.UserID != u.ID || rec.CallerKeyID != u.Keys[0].ID || rec.Input != 7 {
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
	u, secrets := newCaller(t, "Streaming user", "Stream")
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal(w.Code, w.Body.String())
	}
	rec := lastUsage(t)
	if rec.UserID != u.ID || rec.CallerKeyID != u.Keys[0].ID || rec.KeyID != provider.KeyID("backup") || rec.Input != 100 || rec.Output != 20 {
		t.Fatal(rec)
	}
}
