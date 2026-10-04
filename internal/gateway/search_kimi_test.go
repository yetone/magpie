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

// A Kimi Code plan is known by its hosts' /coding endpoints, or by its
// preset; its search service is beside its OpenAI endpoint.
func TestKimiCodeSearchAddress(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want string
	}{
		{provider.Provider{Key: "k", Chat: "https://api.kimi.com/coding/v1"}, "https://api.kimi.com/coding/v1/search"},
		{provider.Provider{Key: "k", Anthropic: "https://api.kimi.ai/coding"}, "https://api.kimi.ai/coding/v1/search"},
		{provider.Provider{Key: "k", Preset: "kimi-code-cn", Chat: "http://127.0.0.1:9/coding/v1"}, "http://127.0.0.1:9/coding/v1/search"},
		{provider.Provider{Chat: "https://api.kimi.com/coding/v1"}, ""}, // no key
		{provider.Provider{Key: "k", Chat: "https://api.moonshot.cn/v1"}, ""},
		{provider.Provider{Key: "k", Chat: "https://api.kimi.com/v1"}, ""},
	} {
		if got := provider.KimiCodeSearch(c.p); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
}

// kimiPlan is a fake Kimi Code plan: its model calls magpie's web_search
// once, then answers; its search service answers as kimi-cli reads it,
// or 500 while broken is set.
type kimiPlan struct {
	*httptest.Server
	mu       sync.Mutex
	searched []string // query, with the headers kimi-cli sends
	results  []string // the tool results its model was given
	broken   bool
}

func newKimiPlan(t *testing.T) *kimiPlan {
	k := &kimiPlan{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.mu.Lock()
		defer k.mu.Unlock()
		switch r.URL.Path {
		case "/coding/v1/search":
			var q map[string]any
			json.Unmarshal(b, &q)
			if r.Header.Get("Authorization") != "Bearer sk-kimi" {
				http.Error(w, `{"error":{"message":"bad key"}}`, 401)
				return
			}
			k.searched = append(k.searched, strings.Join([]string{q["text_query"].(string), r.Header.Get("User-Agent"), r.Header.Get("X-Msh-Tool-Call-Id"),
				strings.Trim(string(must(json.Marshal([]any{q["limit"], q["enable_page_crawling"], q["timeout_seconds"]}))), "[]")}, " | "))
			if k.broken {
				http.Error(w, `{"error":{"message":"search down"}}`, 500)
				return
			}
			io.WriteString(w, `{"search_results":[{"site_name":"Go","title":"Go <b>downloads</b>","url":"https://go.dev/dl/","snippet":"go1.27.1 is the latest release.","content":"","date":"2026-09-30","icon":"","mime":"text/html"},{"site_name":"","title":"","url":""}]}`)
		case "/coding/v1/chat/completions":
			var q struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
				Messages []map[string]any `json:"messages"`
			}
			json.Unmarshal(b, &q)
			if !slices.ContainsFunc(q.Tools, func(x struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}) bool {
				return x.Function.Name == "web_search"
			}) {
				http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if last := q.Messages[len(q.Messages)-1]; last["role"] == "tool" {
				s, _ := last["content"].(string)
				k.results = append(k.results, s)
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
	t.Cleanup(k.Close)
	return k
}

func must(b []byte, _ error) []byte { return b }

// A model of a Kimi Code plan has its searches done by the plan's search
// service, as kimi-cli asks it, before the searcher magpie picks (here an
// API that searches by itself, which a Codex account outranks the same
// way) and the search APIs; with the service down, they come next. Before,
// a Kimi user's searches went to the Codex account, and with nothing else
// set up the model had no search at all.
func TestKimiCodePlanSearchesForItsModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no signed-in agent searches
	kimi := newKimiPlan(t)
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-kimi",
		Chat: kimi.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	ask := func() string {
		t.Helper()
		body := `{"model":"kimi/kimi-for-coding","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"What is the latest Go?"}],
			"tools":[{"name":"Read","description":"read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// nothing else set up: the plan searches all the same
	if canSearch() {
		t.Fatal("something else searches")
	}
	out := ask()
	if !strings.Contains(out, "Go 1.27.1 is out.") || !strings.Contains(out, `"url":"https://go.dev/dl/"`) || !strings.Contains(out, `"page_age":"2026-09-30"`) {
		t.Fatalf("reply %s", out)
	}
	if want := "latest go | " + kimiSearchAgent + " | call_1 | 6,false,30"; strings.Join(kimi.searched, "\n") != want {
		t.Fatalf("searched %q, want %q", kimi.searched, want)
	}
	if len(kimi.results) != 1 || !strings.Contains(kimi.results[0], "1. Go downloads — https://go.dev/dl/\ngo1.27.1 is the latest release.") {
		t.Fatalf("tool results %q", kimi.results)
	}

	// a provider that searches by itself, and a search API: the plan's
	// own search still comes first, and they aren't asked
	var other []string
	var mu sync.Mutex
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"id":"gpt-5-mini"}]}`)
			return
		}
		mu.Lock()
		other = append(other, r.URL.Path)
		mu.Unlock()
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
	if p, _, ok := autoSearcher(); !ok || p.ID != "oai" {
		t.Fatalf("magpie's pick %q %v", p.ID, ok)
	}
	kimi.searched, kimi.results = nil, nil
	if out := ask(); !strings.Contains(out, "Go 1.27.1 is out.") || len(kimi.searched) != 1 || len(other) != 0 || len(apis.asked) != 0 {
		t.Fatalf("kimi %q, the other searcher %q, the APIs %q", kimi.searched, other, apis.asked)
	}

	// the service down: magpie's pick, then the search API
	kimi.broken, kimi.searched, kimi.results = true, nil, nil
	ask()
	if len(kimi.searched) != 1 || len(other) == 0 || strings.Join(apis.asked, "|") != "tavily: latest go" {
		t.Fatalf("kimi %q, the other searcher %q, the APIs %q", kimi.searched, other, apis.asked)
	}
	if len(kimi.results) != 1 || !strings.Contains(kimi.results[0], "go1.27.1 is the latest release.") {
		t.Fatalf("tool results %q", kimi.results)
	}
}

// A Kimi Code plan isn't magpie's pick for other providers' models, which
// keeps the searcher users have; Settings may name it, by itself, with no
// model, and then it searches for every model.
func TestKimiCodePlanNamedToSearch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir())
	kimi := newKimiPlan(t)
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-kimi",
		Chat: kimi.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := autoSearcher(); ok || canSearch() || Searcher() != "" {
		t.Fatal("the plan is picked by itself for other models")
	}
	cs := Searchers()
	if len(cs) != 1 || cs[0].Provider.ID != "kimi" || !cs[0].Service || !cs[0].ManualOnly || cs[0].Small != "" || len(cs[0].Models) != 0 {
		t.Fatalf("choices %+v", cs)
	}
	st := settings.Load()
	st.Searcher = "kimi"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if !canSearch() || Searcher() != "Kimi Code" || SearcherUnused() != "" {
		t.Fatalf("named: can %v, %q, unused %q", canSearch(), Searcher(), SearcherUnused())
	}
	said, hits, err := New().webSearch(context.Background(), "latest go")
	if err != nil || len(hits) != 1 || hits[0].URL != "https://go.dev/dl/" || !strings.Contains(said, "go1.27.1") {
		t.Fatalf("%q %+v %v", said, hits, err)
	}
	if len(kimi.searched) != 1 || !strings.HasPrefix(kimi.searched[0], "latest go | ") {
		t.Fatalf("searched %q", kimi.searched)
	}
}
