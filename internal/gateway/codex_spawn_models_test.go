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

// spawnDescription is the description of the spawn_agent tool in a
// Responses request body, "" when it has none.
func spawnDescription(t *testing.T, body []byte) string {
	t.Helper()
	var q struct {
		Tools []struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Tools       []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %.300s", err, body)
	}
	for _, tl := range q.Tools {
		// a translated request has it flat, multi_agent_v1__spawn_agent
		if tl.Name == "spawn_agent" || strings.HasSuffix(tl.Name, "__spawn_agent") {
			return tl.Description
		}
		for _, in := range tl.Tools {
			if in.Name == "spawn_agent" {
				return in.Description
			}
		}
	}
	return ""
}

// codexSpawnRequest is a real Codex 0.162.0 request, its spawn_agent
// naming the five models Codex names at most, on model.
func codexSpawnRequest(t *testing.T, model string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/workdir/codex-0.162.0.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(withModel(b, model))
}

// Codex's spawn_agent names only the first five models of its catalog
// to the lead (codex-rs MAX_SPAWN_AGENT_MODEL_OVERRIDES), though it takes
// any model of it: with 40 of magpie's in the catalog, the lead saw none
// of them (Gearclickist on Discord: "Codex subagents on any model" on,
// and only a few models could be picked). The tool now names every model
// a subagent of that lead can work on, at CodexPath and at /v1; for a
// lead the ChatGPT backend answers in multi-agent V2, whose subagents'
// tasks only a ChatGPT account opens, only Codex's own.
func TestSpawnAgentNamesEveryModel(t *testing.T) {
	var models []string
	for i := 1; i <= 40; i++ {
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

	namesAll := func(t *testing.T, desc string) {
		t.Helper()
		for _, m := range models {
			if !strings.Contains(desc, "- `fake/"+m+"`") {
				t.Fatalf("spawn_agent doesn't name fake/%s:\n%s", m, desc)
			}
		}
		// Codex's own five kept, and its text after the list
		head, tail, _ := strings.Cut(desc, spawnMore)
		list, rest, ok := strings.Cut(tail, "\n        Spawn a sub-agent")
		// what magpie adds goes upstream in the prompt, to OpenAI too:
		// it names neither magpie nor Codex and its limit
		added := desc[len(head) : len(desc)-len(rest)]
		for _, w := range []string{"magpie", "Magpie", "Codex", "five"} {
			if strings.Contains(added, w) {
				t.Fatalf("the added text names %q:\n%s", w, added)
			}
		}
		if !ok || !strings.Contains(head, "- `gpt-6.1-sol`") || !strings.Contains(head, "- `gpt-5.6-sol`") ||
			!strings.HasPrefix(rest, " for a well-scoped task.") {
			t.Fatalf("description:\n%s", desc)
		}
		for _, l := range strings.Split(strings.TrimPrefix(list, "\n"), "\n") {
			if !strings.HasPrefix(l, "- `") {
				t.Fatalf("line %q in the list:\n%s", l, desc)
			}
		}
	}

	t.Run("CodexPath, a lead on magpie's model", func(t *testing.T) {
		code, body := codexPost(t, codexSpawnRequest(t, "fake/m1"))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		namesAll(t, spawnDescription(t, f.got))
	})
	t.Run("/v1, Codex with magpie as its provider", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexSpawnRequest(t, "fake/m2")))
		req.Header.Set("User-Agent", "codex_cli_rs/0.162.0")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		namesAll(t, spawnDescription(t, f.got))
	})
	t.Run("a lead on Codex's own model, V2", func(t *testing.T) {
		code, body := codexPost(t, codexSpawnRequest(t, "gpt-6.1-sol"))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if d := spawnDescription(t, upstream); d == "" || strings.Contains(d, "fake/") || strings.Contains(d, spawnMore) {
			t.Fatalf("a sealed lead's subagents offered magpie's models:\n%s", d)
		}
	})
	t.Run("a lead on Codex's own model, V1", func(t *testing.T) {
		if err := provider.SetCodexAgentsV1(true); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { provider.SetCodexAgentsV1(false) })
		code, body := codexPost(t, codexSpawnRequest(t, "gpt-6.1-sol"))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		namesAll(t, spawnDescription(t, upstream))
	})
}

// searchedSpawnDescription is the description of the spawn_agent a
// tool_search_output or an additional_tools item in a Responses request
// body hands the model, or of
// the one among its tools when the request was translated (and the
// searched tools put there), "" when it has neither.
func searchedSpawnDescription(t *testing.T, body []byte) string {
	t.Helper()
	var q struct {
		Input []struct {
			Type  string          `json:"type"`
			Tools json.RawMessage `json:"tools"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %.300s", err, body)
	}
	for _, it := range q.Input {
		if it.Type == "tool_search_output" || it.Type == "additional_tools" {
			b, _ := json.Marshal(map[string]json.RawMessage{"tools": it.Tools})
			if d := spawnDescription(t, b); d != "" {
				return d
			}
		}
	}
	return spawnDescription(t, body)
}

// A model that can search tools isn't handed spawn_agent in the request's
// tools: Codex 0.162 defers its multi-agent V1 tools behind tool_search,
// and the lead gets spawn_agent, with Codex's five models, in the
// tool_search_output Codex puts in the conversation. v0.1.1214 widened
// only the tools, so the lead still saw five (Gearclickist on Discord,
// after v0.1.1214). testdata/workdir/codex-0.162.1-tool-search.json is
// real Codex 0.162.1's second request of such a turn, on fake/m1 of a
// catalog of 40, captured in a sandbox CODEX_HOME.
func TestSpawnAgentNamesEveryModelAfterToolSearch(t *testing.T) {
	var models []string
	for i := 1; i <= 40; i++ {
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
	request := func(t *testing.T, model string) string {
		t.Helper()
		b, err := os.ReadFile("testdata/workdir/codex-0.162.1-tool-search.json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(spawnDescription(t, b), "spawn") {
			t.Fatal("the fixture has spawn_agent in its tools; it should be deferred")
		}
		return string(withModel(b, model))
	}
	namesAll := func(t *testing.T, desc string) {
		t.Helper()
		if !strings.Contains(desc, "Available model overrides") {
			t.Fatalf("no spawn_agent from the search:\n%s", desc)
		}
		for _, m := range models {
			if !strings.Contains(desc, "- `fake/"+m+"`") {
				t.Fatalf("spawn_agent doesn't name fake/%s:\n%s", m, desc)
			}
		}
		if _, rest, _ := strings.Cut(desc, spawnMore); !strings.Contains(rest, "Spawn a sub-agent for a well-scoped task.") {
			t.Fatalf("the models aren't between Codex's list and its text:\n%s", desc)
		}
	}

	t.Run("/v1, Codex with magpie as its provider", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(request(t, "fake/m1")))
		req.Header.Set("User-Agent", "codex_cli_rs/0.162.1")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		namesAll(t, searchedSpawnDescription(t, f.got))
	})
	t.Run("CodexPath, a lead on magpie's model", func(t *testing.T) {
		code, body := codexPost(t, request(t, "fake/m1"))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		namesAll(t, searchedSpawnDescription(t, f.got))
	})
	t.Run("CodexPath, a lead on Codex's own model, V1", func(t *testing.T) {
		if err := provider.SetCodexAgentsV1(true); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { provider.SetCodexAgentsV1(false) })
		code, body := codexPost(t, request(t, "gpt-6.1-sol"))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		namesAll(t, searchedSpawnDescription(t, upstream))
	})
}

// Codex sends spawn_agent on every request, so widening it must not
// decode the conversation and write it again (#3394): the body goes on as
// Codex sent it, its members in their order, but for the tools.
func TestSpawnAgentModelsLeavesTheRestAsSent(t *testing.T) {
	up := setup(t, provider.Responses, &fake{t: t})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Responses: up.URL + "/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	var q map[string]json.RawMessage
	if err := json.Unmarshal([]byte(codexSpawnRequest(t, "fake/m1")), &q); err != nil {
		t.Fatal(err)
	}
	// a conversation as Codex writes it: unsorted keys, < > & as they are
	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"a <b> & c ` + strings.Repeat("x", 300000) + `"}]}]`
	body := []byte(`{"model":"fake/m1", "input":` + input + `, "tools":` + string(q["tools"]) + `,"stream":true}`)
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	got := spawnAgentModels(req, body, "fake/m1", false)
	if !strings.Contains(spawnDescription(t, got), "- `fake/m2`") {
		t.Fatalf("spawn_agent not widened:\n%s", spawnDescription(t, got))
	}
	prefix := `{"model":"fake/m1", "input":` + input + `, "tools":`
	if !strings.HasPrefix(string(got), prefix) || !strings.HasSuffix(string(got), `,"stream":true}`) {
		t.Fatalf("the rest of the body changed: %.200s … %.200s", got, got[max(0, len(got)-200):])
	}
}
