package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// With the provider left out of WorkBuddy's names, magpie gives it each
// model's name as its id (buddyIDs in internal/agent), as WorkBuddy shows
// a custom model "name:id" (elis_pina on X). WorkBuddy asks for the model
// by that name, which reaches the model it names, before a model another
// provider serves under the same bare id. Any
// other agent's request for that name is not taken to it.
func TestWorkBuddyAsksByName(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	got := map[string][]string{}
	upstream := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got[name] = append(got[name], gjson.GetBytes(b, "model").String())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	first := upstream("first").URL + "/v1"
	if err := provider.Save(provider.Provider{ID: "first", Name: "First", Key: "k", Models: []string{"other"}, Chat: first}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "second", Name: "Second", Key: "k", Models: []string{"glm-5.3"}, Chat: upstream("second").URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAgentSuffix("workbuddy", provider.SuffixOff); err != nil {
		t.Fatal(err)
	}
	shown, _ := provider.CatalogFor("workbuddy")
	var name string
	for i, l := range provider.LabelsFor("workbuddy", shown) {
		if shown[i].ID == "second/glm-5.3" {
			name = l
		}
	}
	if name == "" || strings.Contains(name, "Second") {
		t.Fatalf("name %q", name)
	}
	// the provider listed first serves a model under that same bare id,
	// hidden from WorkBuddy's list, which a bare id finds first
	if err := provider.Save(provider.Provider{ID: "first", Name: "First", Key: "k", Models: []string{name}, Chat: first}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetHiddenModels("workbuddy", []string{"first/" + name}); err != nil {
		t.Fatal(err)
	}
	if p, _, ok := provider.Resolve(name); !ok || p.ID != "first" {
		t.Fatalf("setup: a bare %q goes to %q, not the provider listed first", name, p.ID)
	}
	if id, ok := provider.AgentNamed("workbuddy", name); !ok || id != "second/glm-5.3" {
		t.Fatalf("AgentNamed(%q) = %q, %v", name, id, ok)
	}
	handler := lanGuard(New().Handler())
	send := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+name+`","messages":[{"role":"user","content":"hi"}]}`))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "CLI/2.161.0 WorkBuddy/2.161.0")
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	rec := send(TokenFor("workbuddy"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(got["second"]) != 1 || got["second"][0] != "glm-5.3" || len(got["first"]) != 0 {
		t.Fatalf("asked for %q, upstreams saw %v", name, got)
	}
	mu.Unlock()
	// another agent's key: the name is no id of its list's
	send(TokenFor("opencode"))
	mu.Lock()
	defer mu.Unlock()
	if len(got["second"]) != 1 || len(got["first"]) != 1 {
		t.Fatalf("another agent's request was taken by WorkBuddy's names: %v", got)
	}
}
