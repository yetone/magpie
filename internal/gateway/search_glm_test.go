package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A GLM Coding Plan is known by its OpenAI endpoint, the Coding Plan's
// /api/coding/paas/v4, on Zhipu's or Z.ai's host or at their presets; its
// search MCP server is on the same host.
func TestZhipuCodingSearchAddress(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want string
	}{
		{provider.Provider{Key: "k", Chat: "https://open.bigmodel.cn/api/coding/paas/v4"}, "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp"},
		{provider.Provider{Key: "k", Chat: "https://api.z.ai/api/coding/paas/v4/"}, "https://api.z.ai/api/mcp/web_search_prime/mcp"},
		{provider.Provider{Key: "k", Preset: "zai", Chat: "http://127.0.0.1:9/api/coding/paas/v4"}, "http://127.0.0.1:9/api/mcp/web_search_prime/mcp"},
		{provider.Provider{Chat: "https://open.bigmodel.cn/api/coding/paas/v4"}, ""},           // no key
		{provider.Provider{Key: "k", Chat: "https://open.bigmodel.cn/api/paas/v4"}, ""},        // pay as you go
		{provider.Provider{Key: "k", Anthropic: "https://open.bigmodel.cn/api/anthropic"}, ""}, // a plan's or not: unknown
		{provider.Provider{Key: "k", Chat: "http://127.0.0.1:9/api/coding/paas/v4"}, ""},       // another host, no preset
		{provider.Provider{Key: "k", Preset: "kimi-code-cn", Chat: "http://127.0.0.1:9/api/coding/paas/v4"}, ""},
	} {
		if got := provider.ZhipuCodingSearch(c.p); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
	// a pay-as-you-go provider with a plan's key beside: that key searches
	pr := provider.Preset("zhipu")
	p := provider.Provider{ID: "glm", Preset: "zhipu", Key: "payg", Chat: pr.Chat, Anthropic: pr.Anthropic,
		Keys: []provider.KeyAccount{{Key: "off", Plan: "coding", Off: true}, {Key: "plan", Plan: "coding"}}}
	if !provider.ZhipuSearches(p) {
		t.Fatal("the plan's key doesn't search")
	}
	ks := provider.ZhipuSearchKeys(p)
	if len(ks) != 1 || ks[0].Key != "plan" || provider.ZhipuCodingSearch(ks[0]) != "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp" {
		t.Fatalf("keys %+v", ks)
	}
	p.Keys = p.Keys[:1]
	if provider.ZhipuSearches(p) {
		t.Fatal("a key that's off searches")
	}
}

// The search tool's text is read as a list of results, the list in a JSON
// string, or the list under a key; words are no results.
func TestReadGLMSearch(t *testing.T) {
	list := `[{"title":"Go <b>downloads</b>","link":"https://go.dev/dl/","content":"go1.27.1 is the latest release.","media":"Go","publish_date":"2026-09-30"},{"title":"no link"}]`
	quoted, _ := json.Marshal(list)
	twice, _ := json.Marshal(string(quoted))
	for name, text := range map[string]string{
		"list":          list,
		"string":        string(quoted),
		"string twice":  string(twice),
		"search_result": `{"search_result":` + list + `}`,
		"results":       `{"results":[{"name":"","url":"https://go.dev/dl/","summary":"go1.27.1 is the latest release.","website":"Go downloads","date":"2026-09-30"}]}`,
	} {
		pages := readGLMSearch(text)
		if len(pages) != 1 || pages[0].URL != "https://go.dev/dl/" || pages[0].Title != "Go downloads" || pages[0].Text != "go1.27.1 is the latest release." || pages[0].Age != "2026-09-30" {
			t.Errorf("%s: %+v", name, pages)
		}
	}
	for _, text := range []string{"", "Search quota exhausted", `{"code":1301}`, `"words"`} {
		if pages := readGLMSearch(text); len(pages) != 0 {
			t.Errorf("%q: %+v", text, pages)
		}
	}
}

// glmSearchPlan is a fake GLM Coding Plan: its model calls magpie's web_search
// once, then answers; its search MCP server answers as the docs say it
// does, in an event stream, or with reply in place of its results.
type glmSearchPlan struct {
	*httptest.Server
	mu      sync.Mutex
	mcp     []string // each MCP request: method, session, User-Agent
	results []string // the tool results its model was given
	reply   string   // the tool's result, when set
}

func newGLMSearchPlan(t *testing.T) *glmSearchPlan {
	g := &glmSearchPlan{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case provider.ZhipuSearchPath:
			if r.Header.Get("Authorization") != "Bearer sk-glm" {
				http.Error(w, `{"error":{"message":"bad key"}}`, 401)
				return
			}
			var q struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			json.Unmarshal(b, &q)
			if r.Method == http.MethodDelete {
				q.Method = "DELETE"
			}
			g.mcp = append(g.mcp, strings.Join([]string{q.Method, r.Header.Get("Mcp-Session-Id"), r.Header.Get("User-Agent")}, " | "))
			switch q.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "sess-1")
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(q.ID)+`,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"web_search_prime","version":"1"}}}`)
			case "tools/call":
				if q.Params.Name != "webSearchPrime" || q.Params.Arguments["search_query"] != "latest go" {
					io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(q.ID)+`,"error":{"code":-32602,"message":"bad call"}}`)
					return
				}
				res := g.reply
				if res == "" {
					text, _ := json.Marshal(`[{"title":"Go <b>downloads</b>","link":"https://go.dev/dl/","content":"go1.27.1 is the latest release.","media":"Go","publish_date":"2026-09-30"}]`)
					res = `{"content":[{"type":"text","text":` + string(must(json.Marshal(string(text)))) + `}]}`
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":"+string(q.ID)+",\"result\":"+res+"}\n\n")
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		case "/api/coding/paas/v4/chat/completions":
			var q struct {
				Messages []map[string]any `json:"messages"`
			}
			json.Unmarshal(b, &q)
			if !strings.Contains(string(b), `"web_search"`) {
				http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if last := q.Messages[len(q.Messages)-1]; last["role"] == "tool" {
				s, _ := last["content"].(string)
				g.results = append(g.results, s)
				io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Go 1.27.1 is out."}}]}`,
					`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
					`data: [DONE]`))
				return
			}
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"latest go\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
				`data: [DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// A model of a GLM Coding Plan has its searches done by the plan's search
// MCP server, with the plan's key, as the plan's MCP config would: with
// nothing else set up in magpie it searches all the same. Before, such a
// model had no search at all. A searcher Settings names still searches for
// it; a search the tool refuses says why; and a pay-as-you-go key, which
// the server doesn't serve, never asks it.
func TestGLMCodingPlanSearchesForItsModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no signed-in agent searches
	g := newGLMSearchPlan(t)
	glm := provider.Provider{ID: "glm", Name: "GLM Coding Plan", Preset: "zhipu", Key: "sk-glm",
		Chat: g.URL + "/api/coding/paas/v4", Models: []string{"glm-4.6"}}
	if err := provider.Save(glm); err != nil {
		t.Fatal(err)
	}
	ask := func() string {
		t.Helper()
		body := `{"model":"glm/glm-4.6","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"What is the latest Go?"}],
			"tools":[{"name":"Read","description":"read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if canSearch() {
		t.Fatal("something else searches")
	}
	out := ask()
	if !strings.Contains(out, "Go 1.27.1 is out.") || !strings.Contains(out, `"url":"https://go.dev/dl/"`) || !strings.Contains(out, `"page_age":"2026-09-30"`) {
		t.Fatalf("reply %s", out)
	}
	if want := "initialize |  | " + SearchAgent + "\nnotifications/initialized | sess-1 | " + SearchAgent + "\ntools/call | sess-1 | " + SearchAgent + "\nDELETE | sess-1 | " + SearchAgent; strings.Join(g.mcp, "\n") != want {
		t.Fatalf("MCP asked\n%s\nwant\n%s", strings.Join(g.mcp, "\n"), want)
	}
	if len(g.results) != 1 || !strings.Contains(g.results[0], "1. Go downloads — https://go.dev/dl/\ngo1.27.1 is the latest release.") {
		t.Fatalf("tool results %q", g.results)
	}

	// the tool refuses: what it said reaches the model, the key masked
	g.mcp, g.results = nil, nil
	g.reply = `{"content":[{"type":"text","text":"sk-glm: search quota used up"}],"isError":true}`
	ask()
	if len(g.results) != 1 || !strings.Contains(g.results[0], "search quota used up") || strings.Contains(g.results[0], "sk-glm") {
		t.Fatalf("tool results %q", g.results)
	}
	g.reply = ""

	// a searcher Settings names searches instead, then the search API
	var other []string
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"id":"gpt-5-mini"}]}`)
			return
		}
		g.mu.Lock()
		other = append(other, r.URL.Path)
		g.mu.Unlock()
		http.Error(w, `{"error":{"message":"not this one"}}`, 500)
	}))
	defer oai.Close()
	hosts := searchHosts[provider.Responses]
	searchHosts[provider.Responses] = append(slices.Clone(hosts), provider.HostOf(oai.URL))
	t.Cleanup(func() { searchHosts[provider.Responses] = hosts })
	o := provider.Provider{ID: "oai", Name: "OpenAI", Key: "k", Responses: oai.URL + "/v1"}
	if err := provider.Save(o); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	// named by nobody, the plan's server still comes first
	g.mcp, g.results = nil, nil
	if ask(); len(g.mcp) != 4 || len(other) != 0 || len(apis.asked) != 0 {
		t.Fatalf("MCP %q, the other searcher %q, the APIs %q", g.mcp, other, apis.asked)
	}
	st := settings.Load()
	st.Searcher = "oai"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	g.mcp, g.results = nil, nil
	ask()
	if len(g.mcp) != 0 || len(other) == 0 || strings.Join(apis.asked, "|") != "tavily: latest go" {
		t.Fatalf("MCP %q, the other searcher %q, the APIs %q", g.mcp, other, apis.asked)
	}
	st.Searcher = ""
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}

	// a pay-as-you-go key: the MCP server isn't asked
	glm.Chat = g.URL + "/api/paas/v4"
	glm.Preset = ""
	if err := provider.Save(glm); err != nil {
		t.Fatal(err)
	}
	pp, err := provider.Find("glm")
	if err != nil {
		t.Fatal(err)
	}
	p := *pp
	if searchesOwn(p) {
		t.Fatal("a pay-as-you-go key searches by the plan's server")
	}
	if _, _, err := New().glmSearch(context.Background(), p, "latest go"); err == nil || len(g.mcp) != 0 {
		t.Fatalf("err %v, MCP %q", err, g.mcp)
	}
}
