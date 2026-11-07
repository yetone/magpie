package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// decisionAsked is the Decisions guide's request
// (developers.openai.com/api/docs/guides/decisions), as openai-python's
// client.decisions.create sends it: a model, the input every question
// shares (a user message of text and an image) and the questions.
func decisionAsked(model string) string {
	return `{"model":"` + model + `","input":[{"role":"user","content":[{"type":"input_text","text":"Inspect the product in this photo."},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[{"type":"predicate","name":"visible_damage","instructions":"Is there visible damage?"},{"type":"choice","name":"condition","instructions":"Its condition","choices":[{"value":"new"},{"value":"used"}]}]}`
}

// decisionAnswer is a Decisions answer, the shape openai-python's
// Decision reads: typed answers and a usage whose input_tokens has its
// cached and cache-written tokens in it.
const decisionAnswer = `{"answers":[{"type":"predicate","name":"visible_damage","probability":0.92},{"type":"choice","name":"condition","choice":"used","confidence":0.8,"probabilities":[{"value":"new","probability":0.2},{"value":"used","probability":0.8}]}],"model":"gpt-6-luna","usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1000,"cache_write_tokens":50},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":1203}}`

type decisionHit struct {
	host, path, auth, apiKey string
	body                     map[string]any
}

// decider is a vendor that serves the Decisions API at /v1/decisions and
// Azure's /openai/v1/decisions, and keeps each request.
type decider struct {
	mu   sync.Mutex
	hits []decisionHit
}

func (d *decider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	d.mu.Lock()
	d.hits = append(d.hits, decisionHit{host: r.Host, path: r.URL.Path, auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("api-key"), body: body})
	d.mu.Unlock()
	if r.URL.Path != "/v1/decisions" && r.URL.Path != "/openai/v1/decisions" {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, decisionAnswer)
}

func (d *decider) all() []decisionHit {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]decisionHit(nil), d.hits...)
}

// decidingProviders saves an OpenAI API key (reached through the
// provider's proxy, so its host is OpenAI's own), an Azure OpenAI
// resource and a relay that lists gpt-6-luna among its chat models.
func decidingProviders(t *testing.T) (*decider, *decider) {
	t.Helper()
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	openai := &decider{}
	ou := httptest.NewServer(openai)
	t.Cleanup(ou.Close)
	if err := provider.Save(provider.Provider{ID: "openai", Name: "OpenAI", Key: "sk-openai", Chat: "http://api.openai.com/v1", Proxy: ou.URL, Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	other := &decider{}
	au := httptest.NewServer(other)
	t.Cleanup(au.Close)
	az, err := provider.FromPreset(provider.AzurePreset)
	if err != nil {
		t.Fatal(err)
	}
	az.Key, az.Chat, az.Models = "azure-key", au.URL+"/openai/v1", []string{"luna-deploy"}
	az.Responses = az.Chat
	if err := provider.Save(az); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "relay-key", Chat: au.URL + "/v1", Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	return openai, other
}

func decisionRecords(t *testing.T, n int) []usage.Record {
	t.Helper()
	var recs []usage.Record
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		recs = nil
		for _, r := range usage.Load(time.Time{}) {
			if r.Operation == "decisions" {
				recs = append(recs, r)
			}
		}
		if len(recs) >= n {
			return recs
		}
	}
	t.Fatalf("%d decisions recorded, want %d: %+v", len(recs), n, recs)
	return nil
}

// POST /v1/decisions (a Discord user had a 404) goes as the agent sent it
// to OpenAI with its key, to Azure OpenAI at its v1 API's
// /openai/v1/decisions with the key in api-key, and on the bare
// /decisions an SDK given magpie's root posts to. The answer comes back
// as OpenAI said it, and the call is recorded as decisions: its input
// apart from the cached and cache-written tokens, each priced at its
// rate, and no output.
func TestDecisionsReachOpenAIAndAzure(t *testing.T) {
	openai, other := decidingProviders(t)
	s := New()
	for _, tc := range []struct {
		name, path, model, sentModel, host, upPath string
		up                                         *decider
	}{
		{"openai by its ref", "/v1/decisions", "openai/gpt-6-luna", "gpt-6-luna", "api.openai.com", "/v1/decisions", openai},
		// a relay lists the same id, but only OpenAI can serve the API
		{"the bare id", "/v1/decisions", "gpt-6-luna", "gpt-6-luna", "api.openai.com", "/v1/decisions", openai},
		{"the bare path", "/decisions", "openai/gpt-6-luna", "gpt-6-luna", "api.openai.com", "/v1/decisions", openai},
		{"azure", "/v1/decisions", "azure/luna-deploy", "luna-deploy", "", "/openai/v1/decisions", other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(tc.up.all())
			code, raw := postJSON(t, s, tc.path, decisionAsked(tc.model))
			if code != 200 || raw != decisionAnswer {
				t.Fatalf("%d %s", code, raw)
			}
			hits := tc.up.all()
			if len(hits) != before+1 {
				t.Fatalf("vendor asked %d times", len(hits)-before)
			}
			h := hits[len(hits)-1]
			if h.path != tc.upPath || tc.host != "" && h.host != tc.host || h.body["model"] != tc.sentModel {
				t.Fatalf("sent to %s%s as %v", h.host, h.path, h.body["model"])
			}
			if qs, _ := h.body["questions"].([]any); len(qs) != 2 {
				t.Fatalf("questions sent: %v", h.body["questions"])
			}
			in, _ := h.body["input"].([]any)
			if b, _ := json.Marshal(in); !strings.Contains(string(b), `"image_url":"data:image/png;base64,iVBORw0KGgo="`) {
				t.Fatalf("input sent: %s", b)
			}
			if tc.up == other {
				if h.apiKey != "azure-key" || h.auth != "" {
					t.Fatalf("azure signed with %q %q", h.apiKey, h.auth)
				}
			} else if h.auth != "Bearer sk-openai" {
				t.Fatalf("openai signed with %q", h.auth)
			}
		})
	}
	for _, h := range other.all() {
		if h.path == "/v1/decisions" {
			t.Fatal("the relay was sent a decision")
		}
	}
	recs := decisionRecords(t, 4)
	for _, r := range recs {
		if r.Status != 200 || r.Input != 150 || r.CacheRead != 1000 || r.CacheWrite != 50 || r.Output != 0 {
			t.Fatalf("recorded %+v", r)
		}
	}
	if r := recs[0]; r.Provider != "openai" || r.Model != "gpt-6-luna" || r.Requested != "openai/gpt-6-luna" {
		t.Fatalf("recorded %+v", r)
	}
}

// A provider that can't serve the Decisions API is never sent the
// request: a relay, whatever its list says, gets a 400 that says who
// can, not the vendor's 404.
func TestDecisionsRefusedWhereNotServed(t *testing.T) {
	_, other := decidingProviders(t)
	code, raw := postJSON(t, New(), "/v1/decisions", decisionAsked("relay/gpt-6-luna"))
	if code != 400 || !strings.Contains(raw, "Relay can't serve OpenAI's Decisions API") || !strings.Contains(raw, "Azure OpenAI") {
		t.Fatalf("%d %s", code, raw)
	}
	if hits := other.all(); len(hits) != 0 {
		t.Fatalf("the relay was asked: %+v", hits)
	}
	// no model, no JSON
	if code, raw := postJSON(t, New(), "/v1/decisions", `{"input":"hi","questions":[]}`); code != 400 || !strings.Contains(raw, "name the model") {
		t.Fatalf("no model: %d %s", code, raw)
	}
}

// canDecide: OpenAI's API with a key, Azure OpenAI and another magpie;
// never a ChatGPT sign-in on OpenAI's host, a plugin, or a relay.
func TestCanDecide(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    provider.Provider
		want bool
	}{
		{"openai key", provider.Provider{ID: "openai", Key: "sk", Chat: "https://api.openai.com/v1"}, true},
		{"azure", provider.Provider{ID: "az", Preset: provider.AzurePreset, Key: "k", Chat: "https://res.openai.azure.com/openai/v1"}, true},
		{"azure by its host", provider.Provider{ID: "az2", Key: "k", Chat: "https://res.openai.azure.com/openai/v1"}, true},
		{"another magpie", provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Key: "k", Chat: "http://192.168.1.20:3425/v1"}, true},
		{"a chatgpt sign-in", provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}, Chat: "https://api.openai.com/v1"}, false},
		{"a plugin", provider.PluginForTest("grok", "grok"), false},
		{"a relay", provider.Provider{ID: "relay", Key: "k", Chat: "https://relay.example/v1"}, false},
		{"openai with no key", provider.Provider{ID: "openai", Chat: "https://api.openai.com/v1"}, false},
	} {
		if got := canDecide(tc.p); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// Another magpie serves the Decisions API by routing it on: the request
// reaches its /v1/decisions and, through it, OpenAI.
func TestDecisionsThroughAnotherMagpie(t *testing.T) {
	openai, _ := decidingProviders(t)
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key", Models: []string{"openai/gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, raw := postJSON(t, New(), "/v1/decisions", decisionAsked("office/openai/gpt-6-luna"))
	if code != 200 || raw != decisionAnswer {
		t.Fatalf("%d %s", code, raw)
	}
	if hits := openai.all(); len(hits) != 1 || hits[0].body["model"] != "gpt-6-luna" {
		t.Fatalf("openai asked %+v", hits)
	}
	recs := decisionRecords(t, 2)
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.Provider] = true
	}
	if !seen["office"] || !seen["openai"] {
		t.Fatalf("recorded %+v", recs)
	}
}

// A gateway key's rules hold on /v1/decisions as on every route: a key
// held to other models is refused before any vendor is asked, a disabled
// model is refused, and a key's spent limit of the model refuses it.
func TestDecisionsHeldByKeysAndRules(t *testing.T) {
	openai, _ := decidingProviders(t)
	budget.Forget()
	pinLimitClock(t)
	keys, secrets := newCaller(t, "Held", "Capped")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"relay/*"}}); err != nil {
		t.Fatal(err)
	}
	setLimit(t, keys[1].ID, &access.Limit{Period: "day", Tokens: 100, Models: []string{"openai/*"}})
	h := New().Handler()
	if w := keyCall(h, secrets[0], "/v1/decisions", decisionAsked("openai/gpt-6-luna")); w.Code != http.StatusForbidden {
		t.Fatalf("a key held to other models: %d %s", w.Code, w.Body.String())
	}
	if w := keyCall(h, "sk-magpie-not-a-key", "/v1/decisions", decisionAsked("openai/gpt-6-luna")); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("a wrong key: %d %s", w.Code, w.Body.String())
	}
	if n := len(openai.all()); n != 0 {
		t.Fatalf("openai asked %d times", n)
	}
	if w := keyCall(h, secrets[1], "/v1/decisions", decisionAsked("openai/gpt-6-luna")); w.Code != 200 {
		t.Fatalf("the capped key's first call: %d %s", w.Code, w.Body.String())
	}
	decisionRecords(t, 1)
	spentSaid(t, keyCall(h, secrets[1], "/v1/decisions", decisionAsked("openai/gpt-6-luna")), "openai/*")
	if n := len(openai.all()); n != 1 {
		t.Fatalf("openai asked %d times", n)
	}
	if err := provider.DisableModels("openai/gpt-6-luna"); err != nil {
		t.Fatal(err)
	}
	if code, raw := postJSON(t, New(), "/v1/decisions", decisionAsked("openai/gpt-6-luna")); code != http.StatusForbidden {
		t.Fatalf("a disabled model: %d %s", code, raw)
	}
	if n := len(openai.all()); n != 1 {
		t.Fatalf("openai asked %d times", n)
	}
}
