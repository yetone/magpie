package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Routing page groups requests by the gateway key they came with
// (acezan on Discord: each key's use, as each agent's): every route names
// its key by id and name, never by its secret, and one sent with no key
// names none.
func TestTraceNamesCallerKey(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Laptop")
	s := New()
	h := s.Handler()
	send := func(remote, secret string) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = remote
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(remote, w.Code, w.Body.String())
		}
	}
	send("192.168.1.9:5000", secrets[0])
	send("127.0.0.1:5000", "")
	b, err := json.Marshal(s.Trace(context.Background(), 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secrets[0]) {
		t.Fatal("the key's secret is in the trace")
	}
	var got struct {
		Routes []map[string]any `json:"routes"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 2 {
		t.Fatalf("routes: %s", b)
	}
	by := map[string]map[string]any{}
	for _, r := range got.Routes {
		id, _ := r["callerKey"].(string)
		by[id] = r
	}
	if r := by[keys[0].ID]; r == nil || r["callerKeyName"] != "Laptop" {
		t.Fatalf("the key's route doesn't name it (%s): %s", keys[0].ID, b)
	}
	if r := by[""]; r == nil || r["callerKeyName"] != nil {
		t.Fatalf("the route sent with no key names one: %s", b)
	}
}
