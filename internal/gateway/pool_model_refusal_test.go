package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cpaNoAuth is CLIProxyAPI's answer, word for word as Jray05343207 quoted
// it on X, once every ChatGPT account it pools for Codex refused
// gpt-6-sol: a 503 whose last upstream error is the model's refusal.
const cpaNoAuth = `{"error":{"message":"auth_unavailable: no auth available (providers=codex, model=gpt-6-sol; last upstream error: model_not_found: The 'gpt-6-sol' model is not supported when using Codex with a ChatGPT account.)","type":"server_error","code":"internal_server_error"}}`

// A relay pooling ChatGPT accounts (CLIProxyAPI) lists gpt-6-sol, which
// its accounts' plans don't serve, and answers it 503 auth_unavailable.
// Read as an outage, that rested the whole key with every model on it.
// Only the model rests: the key's other models are asked straight, and
// no key rests for it.
func TestPoolModelRefusalRestsTheModelNotTheKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
	restingUntil.Unlock()
	routed.Lock()
	routed.failures = map[string]int{}
	routed.Unlock()
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key+":"+req.Model)
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "gpt-6-sol" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, cpaNoAuth)
			return
		}
		io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "cpa", Name: "cpa", Chat: up.URL + "/v1",
		Models: []string{"gpt-6.1-terra", "gpt-6-sol"},
		Key:    "k-one", KeyName: "One", Keys: []provider.KeyAccount{{Name: "Two", Key: "k-two"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("cpa")
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func(model string) (int, string) {
		rec := httptest.NewRecorder()
		body := `{"model":"cpa/` + model + `","messages":[{"role":"user","content":"hi"}]}`
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	for range 3 {
		if code, body := send("gpt-6-sol"); code != http.StatusServiceUnavailable || !strings.Contains(body, "not supported when using Codex with a ChatGPT account") {
			t.Fatalf("gpt-6-sol: %d %s", code, body)
		}
	}
	for _, k := range saved.KeyList() {
		if r, ok := RestOf(saved.RestKey(k)); ok {
			t.Errorf("key %s rests for a model its accounts lack: %+v", k.Name, r)
		}
		if _, ok := RestOf(saved.RestKey(k) + "/gpt-6-sol"); !ok {
			t.Errorf("key %s: gpt-6-sol doesn't rest", k.Name)
		}
	}
	seen = nil
	if code, body := send("gpt-6.1-terra"); code != 200 || !strings.Contains(body, "from-k-one") {
		t.Fatalf("gpt-6.1-terra: %d %s", code, body)
	}
	if strings.Join(seen, ",") != "k-one:gpt-6.1-terra" {
		t.Fatalf("gpt-6.1-terra tried %v; want the first key, straight", seen)
	}
}

// The pool's words are read for what the last refusal said: a model's
// refusal is the model's, its quota or a plain outage is not.
func TestPoolLastErrorRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"model refused", 503, cpaNoAuth, true},
		{"model refused, 500", 500, cpaNoAuth, true},
		{"quota", 503, `{"error":{"message":"auth_unavailable: no auth available (providers=codex, model=gpt-6-sol; last upstream error: usage_limit_reached: The usage limit has been reached)","type":"server_error"}}`, false},
		{"none named", 503, `{"error":{"message":"auth_unavailable: no auth available (providers=codex, model=gpt-6-sol)","type":"server_error"}}`, false},
		{"plain 503 naming a model", 503, `{"error":{"message":"model gpt-6-sol not supported right now, upstream down"}}`, false},
	} {
		if got := modelRefused(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: modelRefused %v, want %v", tc.name, got, tc.want)
		}
	}
}
