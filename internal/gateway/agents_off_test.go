package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Models turned off for agents on the Gateway page (辰林鸭~ on Discord):
// every agent's /v1/models leaves them out, a key of no agent's too, while
// the routing group made of them is listed, and a request to the group or
// naming one of them is served.
func TestAgentsOffModelsList(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	up := httptest.NewServer(f)
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pool", Name: "Pool", Members: []string{"fake/m1", "fake/m2"}}); err != nil {
		t.Fatal(err)
	}
	list := func(key string) map[string]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		New().Handler().ServeHTTP(rec, req)
		var got struct {
			Data []struct{ ID string } `json:"data"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		out := map[string]bool{}
		for _, m := range got.Data {
			out[m.ID] = true
		}
		return out
	}
	keys := []string{TokenFor("opencode"), TokenFor("cursor"), TokenFor("claude"), "magpie"}
	for _, k := range keys {
		if got := list(k); !got["fake/m1"] || !got["group/pool"] {
			t.Fatalf("before: %v", got)
		}
	}
	if err := provider.SetAgentsOff([]string{"fake/m1", "fake/m2"}, true); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if got := list(k); got["fake/m1"] || got["fake/m2"] || !got["fake/m3"] || !got["group/pool"] {
			t.Errorf("%s: %v", k, got)
		}
	}
	for _, model := range []string{"group/pool", "fake/m2"} {
		f.got = nil
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+TokenFor("opencode"))
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(string(f.got), `"m`) {
			t.Errorf("%s: %d %s; upstream got %s", model, rec.Code, rec.Body, f.got)
		}
	}
}

// Codex's own ChatGPT models turned off for agents leave the backend's
// /models answer, as one taken out of Codex's own list does.
func TestCodexModelListAgentsOff(t *testing.T) {
	codexSignedIn(t)
	forgetCodexLists(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5", "gpt-6"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1},{"slug":"gpt-6","priority":2}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	list := func() []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		var native []string
		for _, m := range got.Models {
			if slug, _ := m["slug"].(string); strings.HasPrefix(slug, "gpt-") {
				native = append(native, slug)
			}
		}
		return native
	}
	if native := list(); !slices.Equal(native, []string{"gpt-5.5", "gpt-6"}) {
		t.Fatalf("before: %v", native)
	}
	if err := provider.SetAgentsOff([]string{"codex/gpt-6"}, true); err != nil {
		t.Fatal(err)
	}
	forgetCodexLists(t)
	if native := list(); !slices.Equal(native, []string{"gpt-5.5"}) {
		t.Errorf("turned off for agents: %v", native)
	}
}
