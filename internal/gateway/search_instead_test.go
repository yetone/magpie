package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// pickedSearchSetup is a search for glm/glm-flash, which can't search, with
// ds/deepseek-flash, on DeepSeek's Responses API, the first searcher: one
// that answers without searching (blind), or one whose every search is
// refused for what it was sent, not for its allowance (failing). next is
// another searcher that would search.
func pickedSearchSetup(t *testing.T, failing bool, nextOn provider.Protocol) (ds, next *fakeDeepSeek, result func() string) {
	t.Helper()
	searchSandbox(t)
	forget := func() {
		lastNamedFail.Lock()
		lastNamedFail.f = SearchFailure{}
		lastNamedFail.Unlock()
	}
	forget()
	t.Cleanup(forget)
	ds = newFakeDeepSeek(t)
	ds.blind = true
	first := ds.URL
	if failing {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/models") {
				w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"invalid tools: web_search"}}`))
		}))
		t.Cleanup(broken.Close)
		first = broken.URL
	}
	glm, result := newBlindModel(t)
	next = newFakeDeepSeek(t)
	searchesOn(t, provider.Responses, first)
	searchesOn(t, nextOn, next.URL)
	ds2 := provider.Provider{ID: "ds2", Name: "DeepSeek 2", Key: "k", Models: []string{"deepseek-flash"}}
	if nextOn == provider.Anthropic {
		ds2.Anthropic = next.URL + "/anthropic"
	} else {
		ds2.Responses = next.URL + "/v1"
	}
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Responses: first + "/v1", Models: []string{"deepseek-flash"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}}, ds2)
	return ds, next, result
}

func searchForGLM(t *testing.T, s *Server) {
	t.Helper()
	body := `{"model":"glm/glm-flash","max_tokens":3000,"stream":true,"messages":[{"role":"user","content":"用 web_search 工具搜索：今天上海天气"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// The searcher picked in Settings that fails, or answers without
// searching, gives way to no other provider: Gearclickist on Discord
// picked Grok, whose grok-4.7 answered without searching, and every search
// went on to Codex's gpt-6-astra, a pricier model never picked. The model
// is told the search failed, Settings says why (SearcherFailed), and only
// the search APIs, when set up, search in its place.
func TestPickedSearcherIsNotHandedOn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failing bool
	}{{"answered without searching", false}, {"failed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ds, next, result := pickedSearchSetup(t, tc.failing, provider.Anthropic)
			st := settings.Load()
			st.Searcher = "ds/deepseek-flash"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}

			s := New()
			searchForGLM(t, s)
			if got := next.searches(); len(got) != 0 {
				t.Fatalf("another provider searched in place of the picked one: %q", got)
			}
			if r := result(); strings.Contains(r, "22/17℃") || !strings.Contains(r, "no other provider searches in its place") {
				t.Fatalf("the model was told %q", r)
			}
			for _, r := range s.Trace(context.Background(), 0, 0).Routes {
				if r.Kind == "web_search" && strings.HasPrefix(r.Model, "ds2/") {
					t.Fatalf("a web search row for ds2: %+v", r)
				}
			}
			f, ok := SearcherFailed()
			if !ok || f.Searcher != "ds/deepseek-flash" || f.At.IsZero() {
				t.Fatalf("SearcherFailed = %+v %v", f, ok)
			}
			if tc.failing && (f.NotSearched || f.NothingFound || !strings.Contains(f.Why, "invalid tools: web_search")) {
				t.Fatalf("SearcherFailed = %+v, want the picked searcher's error", f)
			}
			if !tc.failing && (!f.NotSearched || f.NothingFound || f.Why != "") {
				t.Fatalf("SearcherFailed = %+v, want answered without searching", f)
			}

			// a search API set up searches in its place, still not ds2
			apis := newSearchAPIs(t)
			if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
				t.Fatal(err)
			}
			searchForGLM(t, New())
			if strings.Join(apis.asked, "|") != "tavily: 今天上海天气" || len(next.searches()) != 0 {
				t.Fatalf("search APIs asked %q, ds2 %q", apis.asked, next.searches())
			}
			if r := result(); !strings.Contains(r, "go1.27.1") {
				t.Fatalf("the model was told %q", r)
			}

			// another pick says nothing of it, and a search by it clears it
			st.Searcher = "ds2"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			if f, ok := SearcherFailed(); ok {
				t.Fatalf("another searcher picked, SearcherFailed = %+v", f)
			}
			if !tc.failing {
				st.Searcher = "ds/deepseek-flash"
				if err := settings.Save(st); err != nil {
					t.Fatal(err)
				}
				ds.mu.Lock()
				ds.blind = false
				ds.mu.Unlock()
				searchForGLM(t, New())
				if f, ok := SearcherFailed(); ok {
					t.Fatalf("the picked searcher searched, SearcherFailed = %+v", f)
				}
			}
		})
	}
}

// When the searcher magpie picked by itself fails or finds nothing and the
// next one searches in its place, the row of the search that answered
// names the first and why: one that answered without searching is said so,
// apart from one that searched and named no page.
func TestSearchInPlaceOfAutoSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failing bool
	}{{"answered without searching", false}, {"failed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			_, next, _ := pickedSearchSetup(t, tc.failing, provider.Responses)
			if p, m, ok := searcher(); !ok || p.ID != "ds" || m != "deepseek-flash" {
				t.Fatalf("magpie's searcher is %s/%s", p.ID, m)
			}
			s := New()
			searchForGLM(t, s)
			if len(next.searches()) != 1 {
				t.Fatalf("the next searcher asked %q", next.searches())
			}
			var instead *Route
			for _, r := range s.Trace(context.Background(), 0, 0).Routes {
				if r.Kind == "web_search" && strings.HasPrefix(r.Model, "ds2/") {
					r := r
					instead = &r
				}
			}
			if instead == nil || instead.For == nil {
				t.Fatalf("no row for the search that answered: %+v", s.Trace(context.Background(), 0, 0).Routes)
			}
			f := instead.For
			if f.Instead != "ds/deepseek-flash" || f.Picked || f.Model != "glm/glm-flash" {
				t.Fatalf("the answering search's row is for %+v", *f)
			}
			if tc.failing && (f.NothingFound || f.NotSearched || !strings.Contains(f.Why, "invalid tools: web_search")) {
				t.Fatalf("why %q nothing %v not searched %v, want the first searcher's error", f.Why, f.NothingFound, f.NotSearched)
			}
			if !tc.failing && (f.NothingFound || !f.NotSearched || f.Why != "") {
				t.Fatalf("why %q nothing %v not searched %v, want answered without searching", f.Why, f.NothingFound, f.NotSearched)
			}
			if _, ok := SearcherFailed(); ok {
				t.Fatal("SearcherFailed with no searcher picked")
			}
		})
	}
}
