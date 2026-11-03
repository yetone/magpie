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

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// fakeChatGPT stands in for the ChatGPT backend: its search answers with
// the reply a real Plus account got for "Go 1.26" on 2026-11-03 (three of
// its 34 pages), /responses as a model that searched.
type fakeChatGPT struct {
	mu          sync.Mutex
	paths       []string
	bodies      []map[string]any
	searchFails bool
}

func newFakeChatGPT(t *testing.T) *fakeChatGPT {
	t.Helper()
	reply, err := os.ReadFile("testdata/codex_alpha_search.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeChatGPT{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, q)
		fails := f.searchFails
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/alpha/search"):
			if fails {
				http.Error(w, `{"error":{"message":"upstream down"}}`, 502)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(reply)
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-luna"}}`,
				`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"Go 1.26","sources":[{"type":"url","url":"https://go.dev/doc/go1.26"}]}}}`,
				`data: {"type":"response.output_text.delta","delta":"Go 1.26 was released on 10 February 2026."}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9000,"output_tokens":20}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := catalog.SaveLive("codex", provider.CodexBase, []catalog.Model{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeChatGPT) asked() ([]string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths, bodies := slices.Clone(f.paths), slices.Clone(f.bodies)
	f.paths, f.bodies = nil, nil
	return paths, bodies
}

// A ChatGPT account searches for a model that can't (DeepSeek, MiniMax,
// … in Codex) by the ChatGPT backend's own search, the one Codex's web.run
// asks, not by asking gpt-6-luna to search: lucy_85908 on Discord. 151
// such searches on a real Plus account left its 5-hour window where it
// was, while 51 by gpt-6-luna took 1% of it. The search is made in the
// client's mode (#5018: Codex's web_search = "cached" sends
// external_web_access false), live when none is said.
func TestChatGPTAccountSearchesByItsWebSearch(t *testing.T) {
	codexSignedIn(t)
	f := newFakeChatGPT(t)
	s := New()

	if p, m, ok := searcher(); !ok || p.ID != "codex" || m != codexSearchModel {
		t.Fatalf("searcher = %s/%s %v, want codex by its web search", p.ID, m, ok)
	}
	if got := AutoSearcher(); !strings.HasPrefix(got, "Codex") || strings.Contains(got, "@search") {
		t.Fatalf("AutoSearcher = %q", got)
	}
	said, hits, err := s.webSearch(context.Background(), "Go 1.26")
	if err != nil {
		t.Fatal(err)
	}
	paths, bodies := f.asked()
	if !slices.Equal(paths, []string{"/backend-api/codex/alpha/search"}) {
		t.Fatalf("asked %v, want the backend's search alone, no model", paths)
	}
	q := bodies[0]
	cmds, _ := q["commands"].(map[string]any)
	sq, _ := cmds["search_query"].([]any)
	set, _ := q["settings"].(map[string]any)
	if len(sq) != 1 || sq[0].(map[string]any)["q"] != "Go 1.26" || q["model"] != "gpt-6-luna" || q["id"] == "" ||
		set["external_web_access"] != true || !slices.Equal(set["allowed_callers"].([]any), []any{"direct"}) {
		t.Fatalf("body %v", q)
	}
	if strings.Contains(string(mustJSON(q)), "magpie") {
		t.Fatalf("magpie's own words went to the vendor: %v", q)
	}
	urls := []string{}
	for _, h := range hits {
		urls = append(urls, h.URL)
	}
	if !slices.Equal(urls, []string{"https://golang.google.cn/blog/go1.26", "https://golang-org.appspot.com/go.dev/blog/go1.26", "https://go.dev/doc/devel/release"}) {
		t.Fatalf("hits %v", urls)
	}
	if !strings.Contains(said, "Green Tea garbage collector") || !strings.Contains(said, "go1.27.2") ||
		strings.ContainsAny(said, "") || strings.Contains(said, "wordlim") || strings.Contains(said, "-----") {
		t.Fatalf("said %q", said)
	}

	// Codex's cached web search: the search is cached too
	if _, _, err := s.webSearch(withWebAccess(context.Background(), json.RawMessage("false")), "Go 1.26"); err != nil {
		t.Fatal(err)
	}
	if _, bodies := f.asked(); bodies[0]["settings"].(map[string]any)["external_web_access"] != false {
		t.Fatalf("cached went as %v", bodies[0]["settings"])
	}

	// the backend's search failing: the account's small model searches
	f.mu.Lock()
	f.searchFails = true
	f.mu.Unlock()
	said, _, err = s.webSearch(context.Background(), "Go 1.26")
	if err != nil || !strings.Contains(said, "10 February 2026") {
		t.Fatalf("fallback: %q %v", said, err)
	}
	paths, bodies = f.asked()
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/alpha/search") || !strings.HasSuffix(paths[1], "/responses") || bodies[1]["model"] != "gpt-6-luna" {
		t.Fatalf("fallback asked %v %v", paths, bodies)
	}
	f.mu.Lock()
	f.searchFails = false
	f.mu.Unlock()

	// named in Settings: its web search, its small model, a model of it
	for _, c := range []struct{ named, path, model string }{
		{"codex/@search", "/alpha/search", ""},
		{"codex", "/responses", "gpt-6-luna"},
		{"codex/gpt-6.1-sol", "/responses", "gpt-6.1-sol"},
	} {
		st := settings.Load()
		st.Searcher = c.named
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.webSearch(context.Background(), "Go 1.26"); err != nil {
			t.Fatalf("%s: %v", c.named, err)
		}
		paths, bodies := f.asked()
		if len(paths) != 1 || !strings.HasSuffix(paths[0], c.path) || c.model != "" && bodies[0]["model"] != c.model {
			t.Fatalf("%s: asked %v %v", c.named, paths, bodies)
		}
	}
}

// Codex's web_search tool's mode reaches the search magpie makes for a
// model that can't (#5018).
func TestResponsesWebSearchModeKept(t *testing.T) {
	for _, c := range []struct{ tool, want string }{
		{`{"type":"web_search","external_web_access":false}`, "false"},
		{`{"type":"web_search","external_web_access":true}`, "true"},
		{`{"type":"web_search"}`, ""},
	} {
		r, err := readResponsesFor([]byte(`{"model":"deepseek/deepseek-v4","input":"hi","tools":[`+c.tool+`]}`), false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !r.WebSearch || string(r.WebAccess) != c.want {
			t.Fatalf("%s: web search %v, access %q", c.tool, r.WebSearch, r.WebAccess)
		}
		want := c.want
		if want == "" {
			want = "true"
		}
		if got := string(webAccess(withWebAccess(context.Background(), r.WebAccess))); got != want {
			t.Fatalf("%s: searched with %s", c.tool, got)
		}
	}
}

// A search's output is read into pages by the marks Codex's own model
// reads it by; a page with no address is left out.
func TestCodexPagesRead(t *testing.T) {
	rule := codexSearchRule
	out := "Go (https://go.dev/)\nciteturn0search0 [wordlim: 200] Crawled: today; Build simple software.\n" + rule +
		"No address\nciteturn0search9 [wordlim: 200] nothing" + rule +
		"Bare (https://example.com/a)\njust text"
	var reply codexSearchReply
	json.Unmarshal([]byte(`{"results":[{"ref_id":"turn0search0","title":"The Go Programming Language","url":"https://go.dev/"}]}`), &reply)
	pages := codexPages(out, reply)
	if len(pages) != 2 || pages[0].Title != "The Go Programming Language" || pages[0].URL != "https://go.dev/" ||
		!strings.Contains(pages[0].Text, "Build simple software.") || strings.Contains(pages[0].Text, "wordlim") ||
		pages[1].URL != "https://example.com/a" || pages[1].Text != "just text" {
		t.Fatalf("%+v", pages)
	}
}
