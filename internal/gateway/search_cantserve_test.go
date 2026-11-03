package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// codexUsedUp is ChatGPT's refusal of an account whose plan's window is
// used up, the shape Codex accounts answer when they run out.
const codexUsedUp = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":20000}}`

// usedUpSearcher is a searcher on DeepSeek's Responses API whose every
// search is refused with codexUsedUp; asked counts the searches.
func usedUpSearcher(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"deepseek-flash"}]}`)
			return
		}
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, codexUsedUp)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// The searcher picked in Settings that can't serve — its plan used up —
// gives way to the searchers magpie would pick, though none is named after
// it: pedestrianz (plugins#635) named Codex to search for Cursor's models,
// and when Codex ran out and Cursor took the turns over, every search
// failed with it. Once it is known to be out its vendor isn't asked again,
// and with no other searcher that can, the turn still ends with the
// model's answer, told the search failed.
func TestPickedSearcherThatCantServeGivesWay(t *testing.T) {
	fresh(t)
	// the rest its refusal leaves goes with the test
	t.Cleanup(func() {
		restingUntil.Lock()
		restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
		restingUntil.Unlock()
	})
	searchSandbox(t)
	forget := func() {
		lastNamedFail.Lock()
		lastNamedFail.f = SearchFailure{}
		lastNamedFail.Unlock()
	}
	forget()
	t.Cleanup(forget)
	out, asked := usedUpSearcher(t)
	glm, result := newBlindModel(t)
	next := newFakeDeepSeek(t)
	searchesOn(t, provider.Responses, out.URL)
	searchesOn(t, provider.Anthropic, next.URL)
	save(t, provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Responses: out.URL + "/v1", Models: []string{"deepseek-flash"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}},
		provider.Provider{ID: "ds2", Name: "DeepSeek 2", Key: "k", Anthropic: next.URL + "/anthropic", Models: []string{"deepseek-flash"}})
	st := settings.Load()
	st.Searcher = "ds/deepseek-flash"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}

	s := New()
	searchForGLM(t, s)
	if asked.Load() != 1 || len(next.searches()) != 1 {
		t.Fatalf("the picked searcher asked %d times, ds2 %q", asked.Load(), next.searches())
	}
	if r := result(); !strings.Contains(r, "22/17℃") {
		t.Fatalf("the model was told %q", r)
	}
	f, ok := SearcherFailed()
	if !ok || f.Searcher != "ds/deepseek-flash" || f.Instead != "ds2/deepseek-flash" || !strings.Contains(f.Why, "usage limit") {
		t.Fatalf("SearcherFailed = %+v %v", f, ok)
	}
	var row *Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Kind == "web_search" && strings.HasPrefix(r.Model, "ds2/") {
			r := r
			row = &r
		}
	}
	if row == nil || row.For == nil || row.For.Instead != "ds/deepseek-flash" || !row.For.Picked {
		t.Fatalf("the row of the search that answered: %+v", row)
	}

	// known to be out now: the next search goes to ds2, and ds's vendor
	// isn't asked
	searchForGLM(t, New())
	if asked.Load() != 1 || len(next.searches()) != 2 {
		t.Fatalf("out of its allowance, the picked searcher asked %d times, ds2 %q", asked.Load(), next.searches())
	}

	// no other searcher that can: the turn goes on, the model told why
	if err := provider.Delete("ds2"); err != nil {
		t.Fatal(err)
	}
	searchForGLM(t, New())
	if r := result(); strings.Contains(r, "22/17℃") || !strings.Contains(r, "search failed") || !strings.Contains(r, "usage limit") {
		t.Fatalf("the model was told %q", r)
	}
}
