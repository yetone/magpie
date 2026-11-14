package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// modelCatalogText is the text of the <model_catalog> listings Codex puts
// in a request's developer messages when it keeps spawn_agent's models
// in the conversation (features.model_catalog_in_context), "" with none.
func modelCatalogText(t *testing.T, body []byte) string {
	t.Helper()
	var q struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %.300s", err, body)
	}
	var out []string
	for _, it := range q.Input {
		for _, c := range it.Content {
			if it.Type == "message" && it.Role == "developer" && strings.HasPrefix(c.Text, "<model_catalog>") {
				out = append(out, c.Text)
			}
		}
	}
	return strings.Join(out, "\n")
}

// Codex 0.169 hands its lead spawn_agent's models in places other than a
// request's tools, and the lead saw only Codex's first five there
// (@m0eaki on X, Codex 0.169: another magpie's models could be spawned by
// their full slug but weren't listed). In multi-agent V2 on one of
// OpenAI's own models (a Lite request, use_responses_lite) spawn_agent is
// in the additional_tools input item; with
// features.model_catalog_in_context the models are in a <model_catalog>
// developer message, spawn_agent pointing there. A Lite request has no
// tools member, its V1 spawn_agent in a tool_search_output. Each fixture
// is real Codex 0.169.0's second request of a turn whose first searched
// for spawn_agent, captured in a sandbox CODEX_HOME on gpt-5.6-luna with
// a catalog of six of OpenAI's models and four of magpie's.
func TestSpawnAgentNamesEveryModelInCodex0169(t *testing.T) {
	var models []string
	for i := 1; i <= 12; i++ {
		models = append(models, fmt.Sprintf("m%d", i))
	}
	f := &fake{t: t, reply: sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`)}
	up := setup(t, provider.Responses, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Responses: up.URL + "/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	var upstream []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		upstream, _ = io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	// subagents on any model: a lead on OpenAI's model is told of magpie's
	if err := provider.SetCodexAgentsV1(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.SetCodexAgentsV1(false) })

	shapes := []struct {
		name, file string
		listing    func(t *testing.T, body []byte) string
	}{
		{"V2, spawn_agent in additional_tools", "codex-0.169.0-v2-lite.json", searchedSpawnDescription},
		{"model_catalog_in_context", "codex-0.169.0-model-catalog.json", modelCatalogText},
		{"Lite, spawn_agent searched", "codex-0.169.0-lite-tool-search.json", searchedSpawnDescription},
		{"spawn_agent searched", "codex-0.169.0-tool-search.json", searchedSpawnDescription},
	}
	for _, sh := range shapes {
		b, err := os.ReadFile("testdata/workdir/" + sh.file)
		if err != nil {
			t.Fatal(err)
		}
		if l := sh.listing(t, b); !strings.Contains(l, "- `gpt-6-astra`") || strings.Contains(l, "fake/") {
			t.Fatalf("%s: the fixture should list Codex's five alone:\n%s", sh.file, l)
		}
		namesAll := func(t *testing.T, body []byte) {
			t.Helper()
			l := sh.listing(t, body)
			for _, m := range models {
				if !strings.Contains(l, "- `fake/"+m+"`") {
					t.Fatalf("doesn't name fake/%s:\n%s", m, l)
				}
			}
			if strings.Count(l, "- `gpt-6-astra`") != 1 {
				t.Fatalf("Codex's own named twice:\n%s", l)
			}
		}
		t.Run(sh.name+", CodexPath, a lead on OpenAI's model", func(t *testing.T) {
			code, body := codexPost(t, string(withModel(b, "gpt-6-sol")))
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			namesAll(t, upstream)
		})
		// a lead on magpie's model, at CodexPath and at /v1 (where a
		// ChatGPT account's model in magpie is Lite too): as the request
		// goes on, before a provider that takes neither Lite's items nor
		// a tool search has them made over. The body as Codex wrote it,
		// and as withModel writes it again when the request is moved to
		// another model, its angle brackets escaped
		for _, codexPath := range []bool{true, false} {
			for _, moved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s, a lead on magpie's model, CodexPath %v, moved %v", sh.name, codexPath, moved), func(t *testing.T) {
					path := "/v1/responses"
					if codexPath {
						path = CodexPath + "/responses"
					}
					r := httptest.NewRequest("POST", path, nil)
					r.Header.Set("User-Agent", "codex_exec/0.169.0 (Mac OS 27.2.0; arm64) unknown (codex_exec; 0.169.0)")
					in := b
					if moved {
						in = withModel(b, "fake/m1")
					}
					out := spawnAgentModels(r, in, "fake/m1", codexPath)
					if !json.Valid(out) {
						t.Fatalf("not JSON: %.300s", out)
					}
					namesAll(t, out)
				})
			}
		}
	}
}

// An app that runs `codex app-server` names Codex's requests after itself:
// Codex makes the host's clientInfo.name its originator and the head of
// its User-Agent (codex-rs app-server initialize_processor.rs,
// set_default_originator). LobeHub's desktop app runs Codex 0.169.0 so,
// with magpie as its provider at /v1, and its lead saw only Codex's first
// five models of a catalog of 13 (@m0eaki on X): the gate named only
// Codex's own names. Any host's Codex turn carries the headers Codex puts
// on every turn (x-codex-window-id, x-codex-turn-metadata), and is
// widened by them; a request with neither and an app's name is left as
// it came.
func TestSpawnAgentNamesEveryModelForAnAppServerHost(t *testing.T) {
	var models []string
	for i := 1; i <= 12; i++ {
		models = append(models, fmt.Sprintf("m%d", i))
	}
	f := &fake{t: t, reply: sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`)}
	up := setup(t, provider.Responses, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Responses: up.URL + "/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	post := func(t *testing.T, body []byte, h map[string]string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body)))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	// what Codex 0.169.0 sends under LobeHub's app-server
	lobehub := map[string]string{
		"User-Agent":        "lobehub-desktop/0.169.0 (Mac OS 26.5.0; arm64) unknown (lobehub-desktop; 2.1.40)",
		"originator":        "lobehub-desktop",
		"session_id":        "019e1a2b-3c4d-7e5f-8a9b-0c1d2e3f4a5b",
		"x-codex-window-id": "019e1a2b-3c4d-7e5f-8a9b-0c1d2e3f4a5b:0",
	}
	shapes := []struct {
		name, file string
		listing    func(t *testing.T, body []byte) string
	}{
		{"spawn_agent in tools", "codex-0.162.0.json", spawnDescription},
		{"V2, spawn_agent in additional_tools", "codex-0.169.0-v2-lite.json", searchedSpawnDescription},
		{"model_catalog_in_context", "codex-0.169.0-model-catalog.json", modelCatalogText},
		{"spawn_agent searched", "codex-0.169.0-tool-search.json", searchedSpawnDescription},
	}
	for _, sh := range shapes {
		b, err := os.ReadFile("testdata/workdir/" + sh.file)
		if err != nil {
			t.Fatal(err)
		}
		b = withModel(b, "fake/m1")
		t.Run(sh.name+", LobeHub's Codex", func(t *testing.T) {
			post(t, b, lobehub)
			l := sh.listing(t, f.got)
			for _, m := range models {
				if !strings.Contains(l, "- `fake/"+m+"`") {
					t.Fatalf("doesn't name fake/%s:\n%s", m, l)
				}
			}
		})
		t.Run(sh.name+", an app's request without Codex's turn headers", func(t *testing.T) {
			post(t, b, map[string]string{"User-Agent": lobehub["User-Agent"], "originator": "lobehub-desktop"})
			if l := sh.listing(t, f.got); strings.Contains(l, spawnMore) {
				t.Fatalf("widened:\n%s", l)
			}
		})
	}
}
