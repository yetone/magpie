package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// searchRoute is the newest web_search route the Routing page lists.
func searchRoute(t *testing.T, s *Server) Route {
	t.Helper()
	routes := s.Trace(context.Background(), 0, 0).Routes
	for i := len(routes) - 1; i >= 0; i-- {
		if routes[i].Kind == "web_search" {
			return routes[i]
		}
	}
	t.Fatalf("no web_search route in %d", len(routes))
	return Route{}
}

// noSecret fails when any archived object holds one of the secrets.
func noSecret(t *testing.T, b *memBucket, secrets ...string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, data := range b.objs {
		for _, k := range secrets {
			if strings.Contains(string(data), k) {
				t.Errorf("%q in %s:\n%s", k, name, data)
			}
		}
	}
}

// axelburks on Discord: 搜索API代搜时 路由页面不显示是请求了哪个API … 用量-请求
// 页面 能看到请求记录 但是无请求存档. A search magpie asks a search API for
// is a route of the Routing page naming the API and the key (masked), a
// Recent call with its request and answer, and, with the request archive
// on, an archive its usage row names, the key nowhere in it, even where
// the API says it back. Off, nothing is archived, and the route is still listed.
func TestSearchAPIAskIsRoutedAndArchived(t *testing.T) {
	fresh(t)
	const key = "tvly-dev-secret0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo-Key", key) // an API that says the key back
		if !strings.Contains(string(b)+r.Header.Get("Authorization"), key) {
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":{"error":"no key"}}`)
			return
		}
		io.WriteString(w, `{"query":"x","results":[{"title":"Go downloads","url":"https://go.dev/dl/","content":"go1.27.1, key `+key+`","score":0.9}]}`)
	}))
	defer srv.Close()
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: key, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	s := New()
	ctx := context.WithValue(context.Background(), searchForKey{}, &CallFor{Agent: "claude"})
	search := func() Route {
		t.Helper()
		if _, hits, err := s.apiSearch(ctx, "latest go"); err != nil || len(hits) != 1 {
			t.Fatalf("%+v %v", hits, err)
		}
		archivePending.Wait()
		return searchRoute(t, s)
	}

	r := search()
	if len(b.objs) != 0 || r.Archive != "" {
		t.Fatalf("archived while off: %v %q", keys(b.objs), r.Archive)
	}
	// the API by name, and where it was asked, as the usage row says them
	host := provider.HostOf(srv.URL)
	if r.Model != "Tavily" || r.Provider != host || r.Agent != "magpie" || r.For == nil || r.For.Agent != "claude" || !r.Done || r.Status != 200 ||
		len(r.Order) != 1 || r.Order[0].Who != provider.MaskSearchKey(key) || r.Order[0].Kind != "key" {
		t.Fatalf("route %+v", r)
	}
	c := s.Recent()[0]
	if c.Route != r.ID || c.Kind != "web_search" || c.Provider != host || !strings.Contains(c.RequestBody, "latest go") ||
		!strings.Contains(c.ResponseBody, "go.dev/dl") || strings.Contains(c.RequestBody+c.ResponseBody, key) {
		t.Fatalf("recent %+v", c)
	}

	settings.Save(settings.Settings{RequestArchive: true})
	r = search()
	got := archivedOf(t, b)[host]
	if len(got) != 1 || len(b.objs) != 1 {
		t.Fatalf("uploaded %v", keys(b.objs))
	}
	noSecret(t, b, key)
	a := got[0]
	if r.Archive != a.ID || s.Recent()[0].Archive != a.ID || a.Request.Method != "POST" || a.Request.Path != "/search" ||
		!strings.Contains(a.Request.Body, "latest go") ||
		a.Response.Status != 200 || !strings.Contains(a.Response.Body, "go.dev/dl") || a.Call.Kind != "web_search" {
		t.Errorf("archived %+v", a)
	}
	// the usage row names the route, and the archive once it is on
	rows, _ := searchRows(t)
	if len(rows) != 2 || rows[0].RouteID == 0 || rows[0].Archive != "" || rows[1].Archive != a.ID || rows[1].RouteID != r.ID {
		t.Errorf("rows %+v", rows)
	}
}

// An API asked by GET with its key in the address (and its answer cut
// at the archive's size) keeps neither the key nor more than the size.
func TestSearchAPIKeyInQueryNotArchived(t *testing.T) {
	fresh(t)
	const key = "BSAsecretkey0123456789"
	big := strings.Repeat("x", 2<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"web":{"results":[{"title":"Go","url":"https://go.dev/dl/","description":"`+big+`"}]}}`)
	}))
	defer srv.Close()
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	st := settings.Settings{RequestArchive: true, RequestArchiveMaxMB: 1}
	settings.Save(st)
	ctx, w := withSearchWire(context.Background(), key)
	r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/res/v1/web/search?q=go&key="+key, nil)
	r.Header.Set("X-Subscription-Token", key)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res = tapSearch(ctx, r, nil, res)
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	sent, _, _, _, answer, cut, ok := w.taken()
	if !ok || strings.Contains(sent.URL.RawQuery, key) || strings.Contains(sent.Header.Get("X-Subscription-Token"), key) ||
		!cut || len(answer) > 1<<20+1 {
		t.Fatalf("taken %v %q %q cut %v %d", ok, sent.URL.RawQuery, sent.Header, cut, len(answer))
	}
	s := New()
	s.logSearch(ctx, usage.Record{Requested: "Brave", Host: "api.search.brave.com"}, "go", time.Now(), 1, nil)
	archivePending.Wait()
	got := archivedOf(t, b)["api.search.brave.com"]
	if len(got) != 1 || !got[0].Response.Truncated || !strings.HasPrefix(got[0].Request.Path, "/res/v1/web/search?") {
		t.Fatalf("archived %v %+v", keys(b.objs), got)
	}
	noSecret(t, b, key)
}

// A plugin's search API is a route and an archive too: what magpie
// handed the plugin, its key masked, and the pages it gave back, or the
// refusal it threw.
func TestPluginSearchAPIRoutedAndArchived(t *testing.T) {
	fresh(t)
	const spec = "@acme/opencode-mojeek-search@1.0.0"
	const id = "mojeek-search:mojeek"
	plugged(t, spec, false)
	t.Cleanup(forgetSearchKeys) // the refused key rests past the test otherwise
	old := runPluginSearch
	t.Cleanup(func() { runPluginSearch = old })
	runPluginSearch = func(_ context.Context, _, _, _ string, _ int, key, _ string) ([]plugin.SearchPage, error) {
		if key == "spent-key-0001" {
			return nil, plugin.SearchRefused{Status: 429, Msg: "slow down"}
		}
		return []plugin.SearchPage{{Title: "Go downloads", URL: "https://go.dev/dl/", Text: "go1.27.1"}}, nil
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: id, Key: "spent-key-0001,good-key-0003"}); err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	settings.Save(settings.Settings{RequestArchive: true})
	s := New()
	if _, _, err := s.apiSearch(context.Background(), "latest go"); err != nil {
		t.Fatal(err)
	}
	archivePending.Wait()
	got := archivedOf(t, b)["Mojeek"]
	if len(got) != 2 {
		t.Fatalf("uploaded %v", keys(b.objs))
	}
	noSecret(t, b, "spent-key-0001", "good-key-0003")
	var refused, found bool
	for _, a := range got {
		var asked map[string]any
		json.Unmarshal([]byte(a.Request.Body), &asked)
		if a.Request.Method != "PLUGIN" || a.Request.Path != spec || asked["query"] != "latest go" || asked["provider"] != "mojeek" {
			t.Errorf("asked %+v", a.Request)
		}
		refused = refused || a.Response.Status == 429 && strings.Contains(a.Response.Body, "slow down")
		found = found || a.Response.Status == 200 && strings.Contains(a.Response.Body, "go.dev/dl")
	}
	if !refused || !found {
		t.Errorf("archived %+v", got)
	}
	if r := searchRoute(t, s); r.Model != "Mojeek" || r.Status != 200 || r.Archive == "" {
		t.Errorf("route %+v", r)
	}
}

// A Kimi Code plan named to search searches with its own search service
// (searchBy): the ask is a usage row, a route and an archive, the plan's
// key in none of them. Before, it wrote no row at all.
func TestKimiPlanSearchRoutedAndArchived(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir())
	kimi := newKimiPlan(t)
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-kimi",
		Chat: kimi.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	settings.Save(settings.Settings{Searcher: "kimi", RequestArchive: true})
	s := New()
	if _, hits, err := s.webSearch(context.Background(), "latest go"); err != nil || len(hits) != 1 {
		t.Fatalf("%+v %v", hits, err)
	}
	archivePending.Wait()
	got := archivedOf(t, b)["kimi"]
	if len(got) != 1 || got[0].Request.Path != "/coding/v1/search" || !strings.Contains(got[0].Request.Body, "latest go") ||
		got[0].Request.Headers["Authorization"][0] != "[REDACTED]" || !strings.Contains(got[0].Response.Body, "go.dev/dl") {
		t.Fatalf("archived %v %+v", keys(b.objs), got)
	}
	noSecret(t, b, "sk-kimi")
	r := searchRoute(t, s)
	if r.Provider != "kimi" || r.Model != "Kimi Code" || r.Archive != got[0].ID || r.Status != 200 {
		t.Errorf("route %+v", r)
	}
	rows, _ := searchRows(t)
	if len(rows) != 1 || rows[0].Provider != "kimi" || rows[0].Archive != got[0].ID || rows[0].RouteID != r.ID {
		t.Errorf("rows %+v", rows)
	}
}

// GLM Coding Plan's search goes as an MCP session; the archive keeps its
// tools/call, the search itself.
func TestGLMPlanSearchArchivesItsToolsCall(t *testing.T) {
	fresh(t)
	g := newGLMSearchPlan(t)
	glm := provider.Provider{ID: "glm", Name: "GLM Coding Plan", Preset: "zhipu", Key: "sk-glm", Chat: g.URL + "/api/coding/paas/v4", Models: []string{"glm-4.6"}}
	if err := provider.Save(glm); err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	settings.Save(settings.Settings{RequestArchive: true})
	s := New()
	if _, hits, err := s.glmSearch(context.Background(), glm, "latest go"); err != nil || len(hits) != 1 {
		t.Fatalf("%+v %v", hits, err)
	}
	archivePending.Wait()
	got := archivedOf(t, b)["glm"]
	if len(got) != 1 || !strings.Contains(got[0].Request.Body, "tools/call") || !strings.Contains(got[0].Response.Body, "go.dev/dl") {
		t.Fatalf("archived %v %+v", keys(b.objs), got)
	}
	noSecret(t, b, "sk-glm")
	if r := searchRoute(t, s); r.Provider != "glm" || r.Archive != got[0].ID {
		t.Errorf("route %+v", r)
	}
}
