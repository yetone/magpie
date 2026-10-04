package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// fakeDeepSeek answers as DeepSeek's APIs do when magpie's searcher asks
// them to search: Chat has no search tool, and deepseek-flash writes its
// own tool-call markup as text; Responses runs web_search (found nothing
// while blind is set); Anthropic runs web_search_20250305 and answers
// web_search_tool_result blocks with DeepSeek's encrypted_content.
type fakeDeepSeek struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string // the API of each search asked
	blind bool
}

const deepSeekNative = `{"id":"m","type":"message","role":"assistant","model":"deepseek-flash","content":[
	{"type":"thinking","thinking":"Let me search.","signature":"sig"},
	{"type":"server_tool_use","id":"call_00_x","name":"web_search","input":{"query":"今天上海天气"},"caller":{"type":"direct"}},
	{"type":"web_search_tool_result","tool_use_id":"call_00_x","content":[
		{"type":"web_search_result","title":"上海天气预报","url":"https://www.weather.com.cn/weather/101020100.shtml","encrypted_content":"DEEPSEEK-SEALED","page_age":null},
		{"type":"web_search_result","title":"上海当前天气","url":"http://pc.weathercn.com/weather/106577/","encrypted_content":"DEEPSEEK-SEALED-2","page_age":"October 3, 2026"}]},
	{"type":"text","text":"上海今天小雨，22/17℃。"}],
	"stop_reason":"end_turn","usage":{"input_tokens":4672,"output_tokens":825,"server_tool_use":{"web_search_requests":1}}}`

func newFakeDeepSeek(t *testing.T) *fakeDeepSeek {
	f := &fakeDeepSeek{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"deepseek-flash"}]}`)
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		blind := f.blind
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"web_search\">\n<｜DSML｜parameter name=\"query\">今天上海天气</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜function_calls>"}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":116,"completion_tokens":96}}`,
				`data: [DONE]`))
		case "/v1/responses":
			if !strings.Contains(string(b), `"web_search"`) {
				http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if blind {
				io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"deepseek-flash"}}`,
					`data: {"type":"response.output_text.delta","delta":"我无法完成这次检索：没有可用的搜索工具。"}`,
					`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":112,"output_tokens":20}}}`))
				return
			}
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"deepseek-flash"}}`,
				`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"今天上海天气","sources":[{"type":"url","url":"https://www.weather.com.cn/weather/101020100.shtml"}]}}}`,
				`data: {"type":"response.output_text.delta","delta":"上海今天小雨，22/17℃。"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":4000,"output_tokens":20}}}`))
		case "/anthropic/v1/messages":
			if !strings.Contains(string(b), `"web_search_20250305"`) {
				http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, deepSeekNative)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDeepSeek) searches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// a model that can't search (GLM on a Chat API, as in #669): it searches
// once, then answers
func newBlindModel(t *testing.T) (*httptest.Server, func() string) {
	var mu sync.Mutex
	var results []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"glm-flash"}]}`)
			return
		}
		var q struct {
			Messages []map[string]any `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		w.Header().Set("Content-Type", "text/event-stream")
		if last := q.Messages[len(q.Messages)-1]; last["role"] == "tool" {
			mu.Lock()
			s, _ := last["content"].(string)
			results = append(results, s)
			mu.Unlock()
			io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"小雨。"}}]}`,
				`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"今天上海天气\"}"}}]}}]}`,
			`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(results) == 0 {
			return ""
		}
		return results[len(results)-1]
	}
}

// searchAsClaudeCode sends the issue's request (#669): Claude Code's
// web_search offered to glm/glm-flash, which can't search. It gives the
// web_search_tool_result blocks the client got, as sent.
func searchAsClaudeCode(t *testing.T) []map[string]any {
	t.Helper()
	body := `{"model":"glm/glm-flash","max_tokens":3000,"stream":true,"messages":[{"role":"user","content":"用 web_search 工具搜索：今天上海天气"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var blocks []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			ContentBlock map[string]any `json:"content_block"`
		}
		json.Unmarshal([]byte(data), &ev)
		if ev.ContentBlock["type"] == "web_search_tool_result" {
			blocks = append(blocks, ev.ContentBlock)
		}
	}
	return blocks
}

func searchSandbox(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no signed-in agent searches
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
}

func searchesOn(t *testing.T, proto provider.Protocol, url string) {
	hosts := searchHosts[proto]
	searchHosts[proto] = append(slices.Clone(hosts), provider.HostOf(url))
	t.Cleanup(func() { searchHosts[proto] = hosts })
}

func save(t *testing.T, ps ...provider.Provider) {
	t.Helper()
	for _, p := range ps {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// The searcher is asked on the API it searches on (#669): DeepSeek, which
// searches on its Responses API, was asked on its first, Chat, with no
// search tool; deepseek-flash wrote its tool-call markup as text, which
// went to the model as what was found, and the client got
// web_search_tool_result.content [].
func TestSearcherAskedWhereItSearches(t *testing.T) {
	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	glm, result := newBlindModel(t)
	searchesOn(t, provider.Responses, ds.URL)
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: ds.URL + "/v1", Responses: ds.URL + "/v1", Models: []string{"deepseek-flash"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})
	if p, m, ok := searcher(); !ok || p.ID != "ds" || m != "deepseek-flash" {
		t.Fatalf("searcher = %s %s %v", p.ID, m, ok)
	}

	blocks := searchAsClaudeCode(t)
	if got := ds.searches(); !slices.Equal(got, []string{"/v1/responses"}) {
		t.Fatalf("DeepSeek asked on %q", got)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks %v", blocks)
	}
	found, _ := blocks[0]["content"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["url"] != "https://www.weather.com.cn/weather/101020100.shtml" {
		t.Fatalf("found %v", blocks[0])
	}
	if r := result(); strings.Contains(r, "DSML") || !strings.Contains(r, "22/17℃") {
		t.Fatalf("the model was told %q", r)
	}
}

// A relay said to search is for Settings to name: when Automatic's searcher
// finds nothing, the fallback goes on to another provider magpie picks by
// itself, never to the relay and its quota (#359).
func TestSearchFallbackSkipsARelaySaidToSearch(t *testing.T) {
	searchSandbox(t)
	newSearchServer := func(answer string, sources []map[string]any) (*httptest.Server, func() []string) {
		var mu sync.Mutex
		var asked []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/models") {
				io.WriteString(w, `{"data":[{"id":"m1"}]}`)
				return
			}
			mu.Lock()
			asked = append(asked, r.URL.Path)
			mu.Unlock()
			content := []map[string]any{{"type": "text", "text": answer}}
			if len(sources) > 0 {
				content = append(content, map[string]any{"type": "web_search_tool_result", "tool_use_id": "s", "content": sources})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "m1", "content": content, "stop_reason": "end_turn"})
		}))
		t.Cleanup(srv.Close)
		return srv, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(asked)
		}
	}
	blind, blindAsked := newSearchServer("I found nothing.", nil)
	good, goodAsked := newSearchServer("Go 1.27.1 is out.", []map[string]any{{"title": "Go", "url": "https://go.dev/"}})
	relay, relayAsked := newSearchServer("Go 1.27.1 is out.", []map[string]any{{"title": "Go", "url": "https://go.dev/"}})
	searchesOn(t, provider.Anthropic, blind.URL)
	searchesOn(t, provider.Anthropic, good.URL)
	save(t,
		provider.Provider{ID: "blind", Name: "Blind", Key: "k", Anthropic: blind.URL + "/anthropic", Models: []string{"m1"}},
		provider.Provider{ID: "good", Name: "Good", Key: "k", Anthropic: good.URL + "/anthropic", Models: []string{"m1"}},
		provider.Provider{ID: "relay", Name: "Relay", Key: "k", Searches: true, Anthropic: relay.URL + "/anthropic", Models: []string{"m1"}})

	if said, _, err := New().modelSearch(context.Background(), "latest go"); err != nil || !strings.Contains(said, "Go 1.27.1") {
		t.Fatalf("search = %q %v", said, err)
	}
	if got := blindAsked(); !slices.Contains(got, "/anthropic/v1/messages") {
		t.Fatalf("the automatic searcher was asked on %q", got)
	}
	if got := goodAsked(); !slices.Contains(got, "/anthropic/v1/messages") {
		t.Fatalf("the fallback searcher was asked on %q", got)
	}
	for _, path := range relayAsked() {
		if strings.Contains(path, "/messages") {
			t.Fatalf("the relay was asked on %q", path)
		}
	}
}

// DeepSeek's Anthropic API searches by itself: its web_search_tool_result
// is read into pages, with their titles, addresses and ages, and the
// client gets them without DeepSeek's encrypted_content, which is sealed
// for DeepSeek alone.
func TestDeepSeekNativeSearchResults(t *testing.T) {
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range []provider.Protocol{provider.Anthropic, provider.Responses} {
		if !searchesItself(p, proto) {
			t.Errorf("DeepSeek doesn't search by itself on %s", proto)
		}
	}

	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	glm, result := newBlindModel(t)
	searchesOn(t, provider.Anthropic, ds.URL)
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: ds.URL + "/v1", Anthropic: ds.URL + "/anthropic", Models: []string{"deepseek-flash"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})

	blocks := searchAsClaudeCode(t)
	if got := ds.searches(); !slices.Equal(got, []string{"/anthropic/v1/messages"}) {
		t.Fatalf("DeepSeek asked on %q", got)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks %v", blocks)
	}
	found, _ := blocks[0]["content"].([]any)
	if len(found) != 2 {
		t.Fatalf("found %v", blocks[0])
	}
	first, second := found[0].(map[string]any), found[1].(map[string]any)
	if first["url"] != "https://www.weather.com.cn/weather/101020100.shtml" || first["title"] != "上海天气预报" || first["page_age"] != nil ||
		second["url"] != "http://pc.weathercn.com/weather/106577/" || second["page_age"] != "October 3, 2026" {
		t.Fatalf("found %v", found)
	}
	for _, h := range found {
		if h.(map[string]any)["encrypted_content"] != "" {
			t.Fatalf("DeepSeek's encrypted_content reached the client: %v", h)
		}
	}
	if r := result(); !strings.Contains(r, "22/17℃") || !strings.Contains(r, "https://www.weather.com.cn/weather/101020100.shtml") {
		t.Fatalf("the model was told %q", r)
	}
}

// A searcher that answers 200 with nothing found gives way to the next
// searcher, then to the search APIs (#669: deepseek-flash said it couldn't
// search, and Tavily, set up, was never asked).
func TestSearcherFindingNothingGivesWay(t *testing.T) {
	searchSandbox(t)
	ds := newFakeDeepSeek(t)
	ds.blind = true
	glm, result := newBlindModel(t)
	apis := newSearchAPIs(t)
	searchesOn(t, provider.Responses, ds.URL)
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Responses: ds.URL + "/v1", Models: []string{"deepseek-flash"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}

	blocks := searchAsClaudeCode(t)
	if got := ds.searches(); len(got) != 1 {
		t.Fatalf("DeepSeek asked %q", got)
	}
	if strings.Join(apis.asked, "|") != "tavily: 今天上海天气" {
		t.Fatalf("search APIs asked %q", apis.asked)
	}
	found, _ := blocks[0]["content"].([]any)
	if len(blocks) != 1 || len(found) != 1 || found[0].(map[string]any)["url"] != "https://go.dev/dl/" {
		t.Fatalf("blocks %v", blocks)
	}
	if r := result(); strings.Contains(r, "我无法") || !strings.Contains(r, "go1.27.1") {
		t.Fatalf("the model was told %q", r)
	}

	// another searcher, next in magpie's order, answers before the APIs
	apis.asked = nil
	ds2 := newFakeDeepSeek(t)
	searchesOn(t, provider.Anthropic, ds2.URL)
	save(t, provider.Provider{ID: "ds2", Name: "DeepSeek 2", Key: "k", Anthropic: ds2.URL + "/anthropic", Models: []string{"deepseek-flash"}})
	st := settings.Load()
	st.Searcher = "ds"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	blocks = searchAsClaudeCode(t)
	if len(apis.asked) != 0 || len(ds2.searches()) != 1 {
		t.Fatalf("APIs asked %q, the next searcher %q", apis.asked, ds2.searches())
	}
	if found, _ = blocks[0]["content"].([]any); len(found) != 2 {
		t.Fatalf("blocks %v", blocks)
	}
}
