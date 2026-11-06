package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// web_search on the MCP server takes a count (treeman on Discord): how
// many results, 6 when not given, held to 1 through 20. The plugin's
// search API is asked for that many and the tool says that many, though
// the API found more.
func TestSearchMCPCount(t *testing.T) {
	fresh(t)
	const spec = "@acme/opencode-mojeek-search@1.0.0"
	plugged(t, spec, false)
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "mojeek-search:mojeek", Key: "good-key-0003"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var counts []int
	old := runPluginSearch
	t.Cleanup(func() { runPluginSearch = old })
	runPluginSearch = func(_ context.Context, _, _, _ string, count int, _, _ string) ([]plugin.SearchPage, error) {
		mu.Lock()
		counts = append(counts, count)
		mu.Unlock()
		var pages []plugin.SearchPage
		for i := range 30 { // more than any count, as an API may give
			pages = append(pages, plugin.SearchPage{Title: fmt.Sprint("page ", i), URL: fmt.Sprintf("https://ex.test/%d", i), Text: "text"})
		}
		return pages, nil
	}
	h := lanGuard(New().Handler())
	const here = "127.0.0.1:50000"

	var list struct {
		Result struct {
			Tools []struct {
				Name        string
				InputSchema struct {
					Properties map[string]struct {
						Type             string
						Minimum, Maximum int
						Default          int
					}
					Required []string
				}
			}
		}
	}
	if err := json.Unmarshal(mcpPost(h, here, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`).Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	c := list.Result.Tools[0].InputSchema.Properties["count"]
	if list.Result.Tools[0].Name != "web_search" || c.Type != "integer" || c.Minimum != 1 || c.Maximum != 20 || c.Default != 6 ||
		len(list.Result.Tools[0].InputSchema.Required) != 1 {
		t.Fatalf("web_search's schema: %+v", list.Result.Tools[0])
	}

	for _, tc := range []struct {
		arg  string // the count as the call writes it, "" for none
		want int
	}{
		{"", 6},
		{`12`, 12},
		{`20`, 20},
		{`50`, 20},
		{`0`, 1},
		{`-3`, 1},
		{`"8"`, 8},
		{`9.7`, 9},
		{`"many"`, 6},
		{`null`, 6},
	} {
		args := `{"query":"latest go"}`
		if tc.arg != "" {
			args = `{"query":"latest go","count":` + tc.arg + `}`
		}
		mu.Lock()
		counts = nil
		mu.Unlock()
		m := readMCP(t, mcpPost(h, here, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"web_search","arguments":`+args+`}}`))
		if m.Error != nil || m.Result.IsError || len(m.Result.Content) != 1 {
			t.Fatalf("count %s: %+v", tc.arg, m)
		}
		said := m.Result.Content[0].Text
		if n := strings.Count(said, " — https://ex.test/"); n != tc.want {
			t.Errorf("count %s: said %d results, want %d", tc.arg, n, tc.want)
		}
		mu.Lock()
		if len(counts) != 1 || counts[0] != tc.want {
			t.Errorf("count %s: the plugin was asked for %v, want %d", tc.arg, counts, tc.want)
		}
		mu.Unlock()
	}
}

// Every built-in search API, and a Kimi Code plan's search service, is
// asked for the count the search names, in its own field.
func TestSearchCountReachesEveryAPI(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var last string
	f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = r.URL.RawQuery + " " + string(b)
		mu.Unlock()
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(f.Close)
	s := New()
	ctx := withSearchCount(context.Background(), 12)
	for _, c := range []struct{ vendor, want string }{
		{"tavily", `"max_results":12`},
		{"brave", "count=12"},
		{"exa", `"numResults":12`},
		{"firecrawl", `"limit":12`},
		{"deepseek", `"count":12`},
		{"volcengine", `"Count":12`},
		{"wsa", `"Cnt":20`},
		{"you", `"count":12`},
	} {
		s.askSearchAPI(ctx, provider.SearchAPI{Vendor: c.vendor, Key: "k", URL: f.URL}, "latest go")
		mu.Lock()
		if !strings.Contains(last, c.want) {
			t.Errorf("%s was asked %q, not %s", c.vendor, last, c.want)
		}
		mu.Unlock()
	}
	s.askSearchAPI(withSearchCount(context.Background(), 3), provider.SearchAPI{Vendor: "wsa", Key: "k", URL: f.URL}, "q")
	mu.Lock()
	if !strings.Contains(last, `"Cnt":10`) {
		t.Errorf("Yuanbao Search for 3 was asked %q, not its fewest, 10", last)
	}
	mu.Unlock()

	kimi := newKimiPlan(t)
	plan := provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-kimi",
		Chat: kimi.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}
	for _, n := range []int{0, 12} {
		ctx := context.Background()
		want := "| 6,false,30"
		if n > 0 {
			ctx, want = withSearchCount(ctx, n), fmt.Sprint("| ", n, ",false,30")
		}
		s.kimiSearch(ctx, plan, "latest go")
		kimi.mu.Lock()
		got := kimi.searched[len(kimi.searched)-1]
		kimi.mu.Unlock()
		if !strings.HasSuffix(got, want) {
			t.Errorf("Kimi Code's search asked %q, want limit %d", got, n)
		}
	}
}

// A search that names no count, as Claude Code's WebSearch and Codex's
// web_search never do, is answered with the count set in Settings › Web
// search (#5545, mdwsk88), and web_search's schema says it as its
// default; a count the call names still wins, and with none set it is 6.
func TestSearchCountSetting(t *testing.T) {
	fresh(t)
	const spec = "@acme/opencode-mojeek-search@1.0.0"
	plugged(t, spec, false)
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "mojeek-search:mojeek", Key: "good-key-0003"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var counts []int
	old := runPluginSearch
	t.Cleanup(func() { runPluginSearch = old })
	runPluginSearch = func(_ context.Context, _, _, _ string, count int, _, _ string) ([]plugin.SearchPage, error) {
		mu.Lock()
		counts = append(counts, count)
		mu.Unlock()
		var pages []plugin.SearchPage
		for i := range 30 {
			pages = append(pages, plugin.SearchPage{Title: fmt.Sprint("page ", i), URL: fmt.Sprintf("https://ex.test/%d", i), Text: "text"})
		}
		return pages, nil
	}
	h := lanGuard(New().Handler())
	const here = "127.0.0.1:50000"
	set := func(n int) {
		t.Helper()
		s := settings.Load()
		s.SearchCount = n
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	schemaDefault := func() int {
		t.Helper()
		var list struct {
			Result struct {
				Tools []struct {
					InputSchema struct {
						Properties map[string]struct{ Default int }
					}
				}
			}
		}
		if err := json.Unmarshal(mcpPost(h, here, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`).Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list.Result.Tools[0].InputSchema.Properties["count"].Default
	}
	call := func(args string) (said, asked int) {
		t.Helper()
		mu.Lock()
		counts = nil
		mu.Unlock()
		m := readMCP(t, mcpPost(h, here, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"web_search","arguments":`+args+`}}`))
		if m.Error != nil || m.Result.IsError || len(m.Result.Content) != 1 {
			t.Fatalf("%s: %+v", args, m)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(counts) != 1 {
			t.Fatalf("%s: the plugin was asked %v", args, counts)
		}
		return strings.Count(m.Result.Content[0].Text, " — https://ex.test/"), counts[0]
	}

	for _, tc := range []struct{ set, want int }{{11, 11}, {20, 20}, {1, 1}, {0, 6}} {
		set(tc.set)
		if got := searchCount(context.Background()); got != tc.want {
			t.Errorf("set %d: a search naming no count asks for %d, want %d", tc.set, got, tc.want)
		}
		if got := schemaDefault(); got != tc.want {
			t.Errorf("set %d: web_search's schema says %d by default, want %d", tc.set, got, tc.want)
		}
		if said, asked := call(`{"query":"latest go"}`); said != tc.want || asked != tc.want {
			t.Errorf("set %d: no count said %d results, asked %d, want %d", tc.set, said, asked, tc.want)
		}
		if said, asked := call(`{"query":"latest go","count":4}`); said != 4 || asked != 4 {
			t.Errorf("set %d: count 4 said %d results, asked %d", tc.set, said, asked)
		}
	}
}
