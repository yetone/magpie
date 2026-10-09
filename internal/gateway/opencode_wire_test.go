package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeGoCatalog is models.dev's opencode-go as it lists the models of
// #1215 (https://models.dev/api.json, 2026-10-08): GPT, Grok and Muse
// Spark through @ai-sdk/openai, MiniMax through @ai-sdk/anthropic, GLM
// with no provider of its own (the provider's @ai-sdk/openai-compatible).
const openCodeGoCatalog = `{
  "opencode-go": {"id":"opencode-go","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/zen/go/v1","models": {
    "gpt-6-luna": {"id":"gpt-6-luna","name":"GPT-6 Luna","provider":{"npm":"@ai-sdk/openai"}},
    "grok-4.7": {"id":"grok-4.7","name":"Grok 4.7","provider":{"npm":"@ai-sdk/openai"}},
    "muse-spark-1.3-contributor": {"id":"muse-spark-1.3-contributor","name":"Muse Spark 1.3","provider":{"npm":"@ai-sdk/openai"}},
    "minimax-m2.7": {"id":"minimax-m2.7","name":"MiniMax M2.7","provider":{"npm":"@ai-sdk/anthropic"}},
    "glm-5.3": {"id":"glm-5.3","name":"GLM-5.3"}}},
  "openrouter": {"id":"openrouter","models": {
    "gpt-6-luna": {"id":"gpt-6-luna","name":"GPT-6 Luna","provider":{"npm":"@ai-sdk/openai"}}}}
}`

func writeCatalog(t *testing.T, data string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// #1215: an OpenCode Go provider saved with its chat URL alone (added by
// hand, as the reporter's "OpenCode Go 1x" was) sent every model to
// /zen/go/v1/chat/completions, and Go answers GPT, Grok, Muse Spark and
// MiniMax there with 400 "Model does not support this protocol." whatever
// API the client spoke. Each model now goes on the API Go serves it on,
// as models.dev's opencode-go says, from all three of magpie's APIs; GLM
// keeps going on the client's own.
func TestOpenCodeGoModelsGoOnTheirOwnAPI(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go 1x", Key: "k", Chat: "https://opencode.ai/zen/go/v1",
		Models: []string{"gpt-6-luna", "grok-4.7", "muse-spark-1.3-contributor", "minimax-m2.7", "glm-5.3"}}); err != nil {
		t.Fatal(err)
	}
	// Go's own answers: the model's API takes it (here 401, as Go answers
	// a request with no key it knows, 2026-10-08), the other two turn it
	// away as Go does with a key
	own := map[string]string{"gpt-6-luna": "/zen/go/v1/responses", "grok-4.7": "/zen/go/v1/responses",
		"muse-spark-1.3-contributor": "/zen/go/v1/responses", "minimax-m2.7": "/zen/go/v1/messages"}
	var mu sync.Mutex
	asked := map[string][]string{}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		model := modelOf(b)
		mu.Lock()
		asked[model] = append(asked[model], r.URL.Host+r.URL.Path)
		mu.Unlock()
		reply := func(code int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if r.Method != http.MethodPost {
			return reply(404, `{}`)
		}
		if want, ok := own[model]; ok && r.URL.Path != want {
			return reply(400, `{"type":"error","error":{"type":"invalid_request_error","message":"Model does not support this protocol."}}`)
		}
		return reply(401, `{"type":"error","error":{"type":"AuthError","message":"Missing API key."}}`)
	})}
	clients := map[string]string{
		"/v1/chat/completions": `{"model":"opencode-go/%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/messages":         `{"model":"opencode-go/%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"model":"opencode-go/%s","input":"hi","max_output_tokens":16}`,
	}
	for _, model := range []string{"gpt-6-luna", "grok-4.7", "muse-spark-1.3-contributor", "minimax-m2.7", "glm-5.3"} {
		for path, body := range clients {
			mu.Lock()
			asked = map[string][]string{}
			mu.Unlock()
			req := httptest.NewRequest("POST", path, strings.NewReader(strings.Replace(body, "%s", model, 1)))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			mu.Lock()
			got := asked[model]
			mu.Unlock()
			if len(got) == 0 {
				t.Errorf("%s on %s: upstream never asked (%d %s)", model, path, rec.Code, rec.Body)
				continue
			}
			want := "opencode.ai" + own[model]
			if own[model] == "" {
				// GLM: on the API the client spoke
				want = "opencode.ai/zen/go" + path
			}
			if got[0] != want {
				t.Errorf("%s on %s: asked %v, want %s first (%d %s)", model, path, got, want, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "does not support this protocol") {
				t.Errorf("%s on %s: %d %s", model, path, rec.Code, rec.Body)
			}
		}
	}
}

// The rule is OpenCode's gateway's alone: another relay saved with its
// chat URL alone keeps it, and its models stay on it, though models.dev
// says OpenAI's SDK serves the same model elsewhere.
func TestOtherRelayKeepsItsOneAPI(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Catalog: "openrouter", Chat: "https://relay.test/v1", Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Responses != "" || p.Anthropic != "" || p.Catalog != "openrouter" {
		t.Fatalf("relay changed: %+v", p)
	}
	if apis := p.APIs("gpt-6-luna"); apis != nil {
		t.Errorf("relay gpt-6-luna APIs %v, want none named", apis)
	}
	if got := nativeEndpoints(provider.Entry{Provider: *p, Model: "gpt-6-luna", ID: "relay/gpt-6-luna"}); len(got) != 1 || got[0] != "/v1/chat/completions" {
		t.Errorf("relay native_endpoints %v", got)
	}
}

// When the upstream says the model isn't served on the API asked and no
// other is left, the error says which URL was asked, and which API the
// vendor's list serves the model on that the provider has no URL for
// (#1215 asked for the error to be diagnosable). A key in the query isn't
// repeated.
func TestWrongAPINote(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGoCatalog)
	// as a provider is before normalize fills in Go's other APIs: one the
	// user cleared by hand, say, or a remote's
	p := provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Catalog: "opencode-go", Chat: "https://opencode.ai/zen/go/v1"}
	u, _ := url.Parse("https://opencode.ai/zen/go/v1/chat/completions?key=secret")
	res := &http.Response{Request: &http.Request{URL: u}}
	got := wrongAPINote(p, "gpt-6-luna", res, nil)
	for _, want := range []string{"asked at https://opencode.ai/zen/go/v1/chat/completions", "gpt-6-luna is served on /v1/responses, which OpenCode Go has no URL for"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "secret") {
		t.Errorf("note repeats the query: %q", got)
	}
	if got := wrongAPINote(p, "glm-5.3", res, nil); got != " (asked at https://opencode.ai/zen/go/v1/chat/completions)" {
		t.Errorf("glm note %q", got)
	}
}
