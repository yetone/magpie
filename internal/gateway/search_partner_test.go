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

	"github.com/yetone/magpie/internal/provider"
)

// A provider added from a partner that says it searches the web by itself
// (PresetDef.Searches, CoreCode) is sent Claude Code's web_search as sent
// with nothing set, as a relay the user said searches is; it is not
// magpie's searcher for other models unless Settings names it, and
// Codex's web.run, which asks alpha/search, stays magpie's search. One
// moved to another host, and a partner that says nothing, are as before.
func TestPartnerSaidToSearch(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]json.RawMessage
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5"}]}`)
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var q map[string]json.RawMessage
		json.Unmarshal(b, &q)
		mu.Lock()
		got = append(got, q)
		mu.Unlock()
		if string(q["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":5}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Go 1.27.1 is out."}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"Go 1.27.1 is out."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
	}))
	defer relay.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no signed-in agent searches for it
	partner := func(searches bool) {
		restore := provider.PartnersForTest([]provider.Partner{{PresetDef: provider.PresetDef{
			ID: "coretest", Name: "Core Test", Anthropic: relay.URL, Responses: relay.URL + "/v1", Searches: searches,
		}}})
		t.Cleanup(restore)
	}
	partner(true)

	p := provider.Provider{ID: "coretest", Name: "Core Test", Preset: "coretest", Key: "k",
		Anthropic: relay.URL, Responses: relay.URL + "/v1", Models: []string{"claude-haiku-4-5"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if !provider.PresetSearches(p) {
		t.Fatal("a partner's provider at its address isn't said to search")
	}
	body := `{"model":"coretest/claude-haiku-4-5","max_tokens":32000,"stream":false,
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.285.3c7; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"You are an assistant for performing a web search tool use"}],
		"messages":[{"role":"user","content":"Perform a web search for the query: latest go"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}],
		"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"s\"}"}}`
	ask := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("User-Agent", "claude-cli/2.1.285 (external, cli)")
		req.Header.Set("x-app", "cli")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Go 1.27.1 is out.") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	sentTool := func() bool {
		mu.Lock()
		defer mu.Unlock()
		var tools []map[string]any
		json.Unmarshal(got[len(got)-1]["tools"], &tools)
		return len(tools) == 1 && tools[0]["type"] == "web_search_20250305" && tools[0]["max_uses"] == float64(8)
	}

	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sp, _, ok := searcher(); ok {
		t.Fatalf("magpie searches with %s", sp.ID)
	}
	ask()
	if !sentTool() {
		t.Fatalf("the partner's search wasn't sent as Claude Code sent it: %s", got[len(got)-1]["tools"])
	}
	if !searchesItself(p, provider.Responses) {
		t.Error("Codex's web_search on the partner's Responses API isn't its own")
	}
	if upstreamSearches(p) {
		t.Error("Codex's web.run goes to the partner's alpha/search, which it hasn't")
	}
	if r := searchRank(p); r >= 0 {
		t.Errorf("magpie picks the partner as its searcher by itself (rank %d)", r)
	}
	if r, manual := namedSearcherRank(p); r < 0 || !manual {
		t.Errorf("the partner can't be named as the searcher: rank %d manual %v", r, manual)
	}

	// moved to another host by hand, the partner's word is not about it
	moved := p
	moved.Anthropic, moved.Responses = "https://elsewhere.example", "https://elsewhere.example/v1"
	if provider.PresetSearches(moved) || searchesItself(moved, provider.Anthropic) {
		t.Error("a partner's provider moved to another host still searches by itself")
	}

	// a partner that doesn't say so: magpie has no searcher here, so the
	// request goes again without the tool
	partner(false)
	if provider.PresetSearches(p) || searchesItself(p, provider.Anthropic) {
		t.Fatal("a partner that says nothing searches by itself")
	}
	ask()
	if strings.Contains(string(got[len(got)-1]["tools"]), "web_search") {
		t.Fatalf("sent %s", got[len(got)-1]["tools"])
	}
}
