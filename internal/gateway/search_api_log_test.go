package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func searchRows(t *testing.T) ([]usage.Record, string) {
	t.Helper()
	all, text := loggedRows(t)
	var out []usage.Record
	for _, r := range all {
		if r.Source == usage.SourceSearch {
			out = append(out, r)
		}
	}
	return out, text
}

// cybercaius on Discord: which search API a search used couldn't be seen.
// Each ask of a search API with one key is a row of the request log: the
// agent it was for, the API by name, its host, the key masked, and how it
// ended (answered, refused with the API's status, failed, found nothing).
// The query is in no row, not even where the API said it back in an error.
func TestSearchAPIAsksAreLogged(t *testing.T) {
	fresh(t)
	// letters only: a number could be in the server's random port
	// (127.0.0.1:37817 held "7781" on the Linux box)
	const query = "kangaroo orbit quokkavex"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Query string `json:"query"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") {
		case "tvly-spent-0001":
			w.WriteHeader(432)
			io.WriteString(w, `{"detail":{"error":"This request exceeds your plan's set usage limit"}}`)
		case "tvly-good-0002":
			io.WriteString(w, `{"query":"x","results":[{"title":"Go downloads","url":"https://go.dev/dl/","content":"go1.27.1","score":0.9}]}`)
		case "tvly-echo-0003":
			w.WriteHeader(500)
			io.WriteString(w, `{"detail":{"error":"could not search for `+q.Query+`"}}`)
		default:
			io.WriteString(w, `{"query":"x","results":[]}`)
		}
	}))
	defer srv.Close()
	s := New()
	ctx := context.WithValue(context.Background(), searchForKey{}, &CallFor{Agent: "claude"})
	search := func(keys string) {
		t.Helper()
		forgetSearchKeys()
		if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: keys, URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
		s.apiSearch(ctx, query)
	}
	search("tvly-spent-0001\ntvly-good-0002")
	search("tvly-echo-0003")
	search("tvly-empty-0004")
	if _, _, err := s.apiSearch(context.Background(), query); err != nil { // no CallFor: magpie's own
		t.Log(err)
	}

	rows, text := searchRows(t)
	if len(rows) != 5 {
		t.Fatalf("%d search rows: %+v", len(rows), rows)
	}
	host := provider.HostOf(srv.URL)
	for i, r := range rows {
		if r.Requested != "Tavily" || r.Host != host || r.Kind != "web_search" || r.Provider != "" || r.Model != "" ||
			r.ProviderKeyID == "" || r.ProviderKeyName == "" || strings.Contains(r.ProviderKeyName, "spent") || strings.Contains(r.ProviderKeyName, "good") {
			t.Errorf("row %d: %+v", i, r)
		}
		if want := "claude"; i == 4 {
			if r.Agent != "magpie" {
				t.Errorf("row %d agent %q", i, r.Agent)
			}
		} else if r.Agent != want {
			t.Errorf("row %d agent %q", i, r.Agent)
		}
	}
	if r := rows[0]; r.Status != 432 || !r.Failed() {
		t.Errorf("refused key: %+v", r)
	}
	if r := rows[1]; r.Status != 200 || r.Failed() {
		t.Errorf("answered: %+v", r)
	}
	if r := rows[2]; !r.Failed() || r.Error == "" {
		t.Errorf("failed: %+v", r)
	}
	if r := rows[3]; r.Error != "found nothing" {
		t.Errorf("found nothing: %+v", r)
	}
	if rows[0].ProviderKeyID == rows[1].ProviderKeyID {
		t.Errorf("two keys, one id: %q", rows[0].ProviderKeyID)
	}
	for _, w := range strings.Fields(query) {
		if strings.Contains(text, w) {
			t.Fatalf("the query's %q is in usage.jsonl:\n%s", w, text)
		}
	}
	// counted in nothing: Load (totals, budgets) leaves them out
	for _, r := range usage.Load(rows[0].Time.Add(-1)) {
		if r.Source == usage.SourceSearch {
			t.Fatalf("Load gave a search row: %+v", r)
		}
	}
}

// An API asked by GET carries the query in its URL, which Go's transport
// errors say whole: the row says the host and what went wrong, not the URL.
func TestSearchAPIFailureKeepsNoURL(t *testing.T) {
	fresh(t)
	// letters only: a number could be in the server's random port
	// (127.0.0.1:44105 held "4410" on ubuntu CI, #5616) or in the row's
	// route_id (1794294410888 did on 11-10)
	const query = "wombat lattice numbatzor"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			c.Close() // no answer at all
		}
	}))
	defer srv.Close()
	forgetSearchKeys()
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "brave", Key: "brave-k-0001", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New().apiSearch(context.Background(), query); err == nil {
		t.Fatal("a closed connection answered")
	}
	rows, text := searchRows(t)
	if len(rows) != 1 || rows[0].Requested != "Brave Search" || rows[0].Error == "" || !strings.Contains(rows[0].Error, provider.HostOf(srv.URL)) || strings.Contains(rows[0].Error, "?") {
		t.Fatalf("rows %+v", rows)
	}
	for _, w := range strings.Fields(query) {
		if strings.Contains(text, w) {
			t.Fatalf("the query's %q is in usage.jsonl:\n%s", w, text)
		}
	}
}

// A plan's own search service (Kimi Code's here, WorkBuddy's in
// TestWorkBuddyPluginSearches) writes the same row, under the plan's
// provider (#3528).
func TestOwnSearchIsLogged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir())
	kimi := newKimiPlan(t)
	p := provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-kimi", Chat: kimi.URL + "/coding/v1", Models: []string{"kimi-for-coding"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(searchingOn(context.Background(), p), searchForKey{}, &CallFor{Agent: "claude"})
	const query = "latest go"
	if _, _, err := New().ownSearch(ctx, p, query); err != nil {
		t.Fatal(err)
	}
	kimi.mu.Lock()
	kimi.broken = true
	kimi.mu.Unlock()
	New().ownSearch(ctx, p, query)
	rows, _ := searchRows(t)
	if len(rows) != 2 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Provider != "kimi" || r.Requested != "Kimi Code" || r.Host != provider.HostOf(kimi.URL) || r.Agent != "claude" || r.Kind != "web_search" {
			t.Errorf("row %+v", r)
		}
	}
	if rows[0].Status != 200 || rows[0].Failed() || !rows[1].Failed() {
		t.Errorf("outcomes %+v", rows)
	}
}
