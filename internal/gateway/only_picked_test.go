package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// An agent shown only the models picked for it (nianlee-official, #1337):
// with its key, /v1/models leaves out a model that came after the switch,
// of a new provider or an old one, while another agent's list has it; and
// the model asked for by name still goes through.
func TestOnlyPickedModelsList(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
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
	if err := provider.SetOnlyPicked("opencode", true); err != nil {
		t.Fatal(err)
	}
	if got := list(TokenFor("opencode")); !got["fake/m1"] {
		t.Fatalf("switched on, what was shown went: %v", got)
	}
	// a new model of the provider it had, and a new provider
	up := httptest.NewServer(f)
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "fake", Name: "Fake", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1", "m2"}},
		{ID: "fresh", Name: "Fresh", Key: "k", Chat: up.URL + "/v1", Models: []string{"x1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := list(TokenFor("opencode")); !got["fake/m1"] || got["fake/m2"] || got["fresh/x1"] {
		t.Errorf("only picked: %v", got)
	}
	if got := list(TokenFor("cursor")); !got["fake/m2"] || !got["fresh/x1"] {
		t.Errorf("another agent's list: %v", got)
	}
	// asked for by name, one it isn't shown still answers
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fresh/x1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+TokenFor("opencode"))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(string(f.got), `"x1"`) {
		t.Errorf("a model not picked, by name: %d %s; upstream got %s", rec.Code, rec.Body, f.got)
	}
	// ticked, it is listed
	if err := provider.SetPickedModels("opencode", []string{"fake/m1", "fresh/x1"}); err != nil {
		t.Fatal(err)
	}
	if got := list(TokenFor("opencode")); !got["fresh/x1"] || got["fake/m2"] {
		t.Errorf("fresh/x1 ticked: %v", got)
	}
}

// Codex's own ChatGPT models follow the switch too: a model the account
// gains after it is off Codex's list until ticked.
func TestCodexModelListOnlyPicked(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	slugs := `{"slug":"gpt-5.5","priority":1}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[`+slugs+`]}`)
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
	if err := provider.SetOnlyPicked("codex", true); err != nil {
		t.Fatal(err)
	}
	if native := list(); len(native) != 1 || native[0] != "gpt-5.5" {
		t.Fatalf("switched on: %v", native)
	}
	slugs += `,{"slug":"gpt-6","priority":2}`
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5", "gpt-6"}}); err != nil {
		t.Fatal(err)
	}
	if native := list(); len(native) != 1 || native[0] != "gpt-5.5" {
		t.Errorf("a model the account gained since: %v", native)
	}
	// off: what is shown stays (gpt-6 still off), and one that comes now shows
	if err := provider.SetOnlyPicked("codex", false); err != nil {
		t.Fatal(err)
	}
	if native := list(); len(native) != 1 || native[0] != "gpt-5.5" {
		t.Errorf("switched off: %v", native)
	}
	slugs += `,{"slug":"gpt-6-mini","priority":3}`
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5", "gpt-6", "gpt-6-mini"}}); err != nil {
		t.Fatal(err)
	}
	if native := list(); len(native) != 2 || native[1] != "gpt-6-mini" {
		t.Errorf("switched off, a new model: %v", native)
	}
}
