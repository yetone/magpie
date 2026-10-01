package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// searchAPIs is a fake of every search API magpie asks, each on its own
// path and checking its own way of carrying the key; tavily answers 500
// while broken is set.
type searchAPIs struct {
	*httptest.Server
	mu     sync.Mutex
	asked  []string // vendor: query
	broken bool
}

func newSearchAPIs(t *testing.T) *searchAPIs {
	f := &searchAPIs{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q struct {
			Query string `json:"query"`
		}
		json.Unmarshal(b, &q)
		vendor, key := "", ""
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tavily/search":
			vendor, key = "tavily", r.Header.Get("Authorization")
		case r.Method == http.MethodGet && r.URL.Path == "/brave/res/v1/web/search":
			vendor, key, q.Query = "brave", r.Header.Get("X-Subscription-Token"), r.URL.Query().Get("q")
		case r.Method == http.MethodPost && r.URL.Path == "/exa/search":
			vendor, key = "exa", r.Header.Get("X-Api-Key")
		case r.Method == http.MethodPost && r.URL.Path == "/firecrawl/v2/search":
			vendor, key = "firecrawl", r.Header.Get("Authorization")
		case r.Method == http.MethodGet && r.URL.Path == "/searxng/search" && r.URL.Query().Get("format") == "json":
			vendor, key, q.Query = "searxng", "none", r.URL.Query().Get("q")
		default:
			http.NotFound(w, r)
			return
		}
		want := map[string]string{"tavily": "Bearer tvly-k", "brave": "brave-k", "exa": "exa-k", "firecrawl": "Bearer fc-k", "searxng": "none"}[vendor]
		if key != want {
			http.Error(w, `{"error":"bad key `+key+`"}`, 401)
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, vendor+": "+q.Query)
		broken := f.broken
		f.mu.Unlock()
		if vendor == "tavily" && broken {
			http.Error(w, `{"detail":{"error":"down"}}`, 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch vendor {
		case "tavily":
			io.WriteString(w, `{"query":"x","results":[{"title":"Go downloads","url":"https://go.dev/dl/","content":"go1.27.1 is the latest release.","score":0.9}]}`)
		case "brave":
			io.WriteString(w, `{"type":"search","web":{"results":[{"title":"Go &amp; <strong>downloads</strong>","url":"https://go.dev/dl/","description":"The <strong>latest</strong> Go is 1.27.1"}]}}`)
		case "exa":
			io.WriteString(w, `{"results":[{"title":"Go downloads","url":"https://go.dev/dl/","text":"Go 1.27.1 was released."}]}`)
		case "firecrawl":
			io.WriteString(w, `{"success":true,"data":{"web":[{"title":"Go downloads","url":"https://go.dev/dl/","description":"Download Go 1.27.1"}]}}`)
		case "searxng":
			io.WriteString(w, `{"query":"x","results":[{"title":"Go downloads","url":"https://go.dev/dl/","content":"Go 1.27.1 is out"},{"title":"","url":""}]}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *searchAPIs) api(vendor, key string) provider.SearchAPI {
	return provider.SearchAPI{Vendor: vendor, Key: key, URL: f.URL + "/" + vendor}
}

// Each search API is asked its own way and read into the pages it found.
func TestSearchAPIsEachAsked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newSearchAPIs(t)
	s := New()
	for _, c := range []struct {
		api  provider.SearchAPI
		text string
	}{
		{f.api("tavily", "tvly-k"), "go1.27.1 is the latest release."},
		{f.api("brave", "brave-k"), "The latest Go is 1.27.1"},
		{f.api("exa", "exa-k"), "Go 1.27.1 was released."},
		{f.api("firecrawl", "fc-k"), "Download Go 1.27.1"},
		{f.api("searxng", ""), "Go 1.27.1 is out"},
	} {
		pages, err := s.askSearchAPI(context.Background(), c.api, "latest go")
		if err != nil {
			t.Errorf("%s: %v", c.api.Vendor, err)
			continue
		}
		if len(pages) != 1 || pages[0].URL != "https://go.dev/dl/" || pages[0].Text != c.text || !strings.HasPrefix(pages[0].Title, "Go ") {
			t.Errorf("%s: %+v", c.api.Vendor, pages)
		}
	}
	if pages, _ := s.askSearchAPI(context.Background(), f.api("brave", "brave-k"), "q"); len(pages) == 1 && pages[0].Title != "Go & downloads" {
		t.Errorf("brave's title kept its tags: %q", pages[0].Title)
	}
	if _, err := s.askSearchAPI(context.Background(), f.api("exa", "wrong"), "q"); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("a refused key: %v", err)
	}
}

// With no provider that can search, a client's web search is given to the
// model as magpie's tool all the same, and answered by the search APIs the
// user set up, in their order: one that fails gives way to the next.
func TestWebSearchBySearchAPI(t *testing.T) {
	var mu sync.Mutex
	var asked [][]map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []map[string]any `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, q.Messages)
		n := len(asked)
		mu.Unlock()
		if len(q.Tools) != 2 || q.Tools[1].Function.Name != "web_search" {
			http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n%2 == 1 {
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"latest go\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Go 1.27.1 is out."}}]}`,
			`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`))
	}))
	defer model.Close()
	apis := newSearchAPIs(t)

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no signed-in agent searches
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := provider.Save(provider.Provider{ID: "deep", Name: "Deep", Key: "k", Chat: model.URL, Models: []string{"deep-chat"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := searcher(); ok {
		t.Fatal("a provider searches")
	}
	for _, a := range []provider.SearchAPI{apis.api("tavily", "tvly-k"), apis.api("searxng", "")} {
		if err := provider.SetSearchAPI(a); err != nil {
			t.Fatal(err)
		}
	}
	ask := func() (string, []Hit, string) {
		t.Helper()
		body := `{"model":"deep/deep-chat","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"What is the latest Go?"}],
			"tools":[{"name":"Read","description":"read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var text strings.Builder
		var found []Hit
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
				ContentBlock struct {
					Type    string `json:"type"`
					Content []Hit  `json:"content"`
				} `json:"content_block"`
			}
			json.Unmarshal([]byte(data), &ev)
			if ev.ContentBlock.Type == "web_search_tool_result" {
				found = ev.ContentBlock.Content
			}
			text.WriteString(ev.Delta.Text)
		}
		mu.Lock()
		defer mu.Unlock()
		last := asked[len(asked)-1]
		if len(asked)%2 == 1 || len(last) < 2 {
			t.Fatalf("model asked %d times: %v", len(asked), asked)
		}
		result, _ := asked[len(asked)-1][len(last)-1]["content"].(string)
		return text.String(), found, result
	}

	text, found, result := ask()
	if text != "Go 1.27.1 is out." || len(found) != 1 || found[0].URL != "https://go.dev/dl/" {
		t.Fatalf("text %q found %+v", text, found)
	}
	if !strings.Contains(result, "Go downloads — https://go.dev/dl/") || !strings.Contains(result, "go1.27.1 is the latest release.") {
		t.Fatalf("tool result %q", result)
	}
	if strings.Join(apis.asked, "|") != "tavily: latest go" {
		t.Fatalf("asked %q", apis.asked)
	}

	// Tavily down: SearXNG, next in line, answers
	apis.asked, apis.broken = nil, true
	if _, found, result = ask(); len(found) != 1 || !strings.Contains(result, "Go 1.27.1 is out") {
		t.Fatalf("found %+v result %q", found, result)
	}
	if strings.Join(apis.asked, "|") != "tavily: latest go|searxng: latest go" {
		t.Fatalf("asked %q", apis.asked)
	}
}

// Without a provider that searches or a search API, the client's search
// tool is left out, as before.
func TestNoSearchAPINoTool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if canSearch() {
		t.Fatal("can search with nothing set up")
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "brave", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if !canSearch() {
		t.Fatal("a search API set up, and magpie can't search")
	}
}
