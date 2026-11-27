package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin's search API that signs in as the plugin's own provider
// (../plugin/testdata/searchauth, axelburks on Discord), end to end
// through the plugin host: saved with no key; while the provider isn't
// signed in, a search says so and the next search API answers; signed
// in, each search is one account that is on, in the provider's order, the
// hook seeing that account alone; an account the API rate limits rests
// for search only, the next account answering, and the provider's own
// rests (which its models' requests read) are untouched; and the plugin's
// entry without auth still takes its key as before.
func TestPluginSearchSignsIn(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/searchauth")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	vendor := func(id string) provider.SearchVendor {
		t.Helper()
		for _, v := range provider.AllSearchVendors() {
			if v.Plugin == abs && v.PluginID == id {
				return v
			}
		}
		t.Fatalf("no vendor %s in %+v", id, provider.AllSearchVendors())
		return provider.SearchVendor{}
	}
	seek, keyed := vendor("seek"), vendor("keyed")
	if seek.Auth != "seekco" || keyed.Auth != "" {
		t.Fatalf("seek %+v keyed %+v", seek, keyed)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: seek.ID}); err != nil {
		t.Fatalf("a search API that signs in, saved with no key: %v", err)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: keyed.ID}); err == nil || !strings.Contains(err.Error(), "needs its API key") {
		t.Errorf("a key's search API with no key: %v", err)
	}
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: keyed.ID, Key: "k9"}); err != nil {
		t.Fatal(err)
	}
	if as := provider.SearchAPIs(); len(as) != 2 || as[0].Vendor != seek.ID {
		t.Fatalf("search APIs %+v", as)
	}

	s := New()
	// not signed in: said, and the next API answers
	if _, p := provider.SearchSignIn(seek); p != nil {
		t.Fatalf("signed in with no account: %+v", p)
	}
	said, hits, err := s.apiSearch(ctx, "go")
	if err != nil || len(hits) != 1 || hits[0].Title != "k9 undefined" {
		t.Fatalf("signed out: %q %+v %v", said, hits, err)
	}
	if _, err := s.askSearchAPIKeys(ctx, provider.SearchAPI{Vendor: seek.ID}, "go"); err == nil || !strings.Contains(err.Error(), "not signed in to seekco") {
		t.Errorf("signed out, the error: %v", err)
	}

	later := time.Now().Add(time.Hour).UnixMilli()
	for _, who := range []string{"a@seek", "b@seek", "c@seek"} {
		access := "acc-" + who[:1]
		if who == "a@seek" {
			access = "spent" // the search API rate limits it
		}
		if _, err := plugin.Import(ctx, "seekco", map[string]any{"type": "oauth", "access": access, "refresh": "r-" + who, "expires": later, "accountId": who}); err != nil {
			t.Fatal(err)
		}
	}
	// c signed in but switched off
	if err := provider.SetLoginOn("plugin:seekco", "c@seek", false); err != nil {
		t.Fatal(err)
	}
	name, p := provider.SearchSignIn(seek)
	if p == nil || name == "" {
		t.Fatalf("signed in, no provider: %q", name)
	}
	cs := accountsOn(*p, "")
	var users []string
	for _, c := range cs {
		users = append(users, c.p.Account.User)
	}
	if strings.Join(users, " ") != "a@seek b@seek" {
		t.Fatalf("accounts on: %v", users)
	}

	said, hits, err = s.apiSearch(ctx, "go")
	if err != nil || len(hits) != 1 || hits[0].Title != "b@seek acc-b" {
		t.Fatalf("signed in: %q %+v %v", said, hits, err)
	}
	// the rate limit rested a for search alone
	if r, ok := searchKeyRest(seek.ID, searchAccountID(cs[0].p), time.Now()); !ok || r.Why != "rate limited" {
		t.Errorf("a's search rest: %+v %v", r, ok)
	}
	if _, ok := searchKeyRest(seek.ID, searchAccountID(cs[1].p), time.Now()); ok {
		t.Error("b rests, having answered")
	}
	for _, c := range accountsOn(*p, "") {
		if r, resting := restingOf(c); resting {
			t.Errorf("a search's rate limit rested %s for its models: %+v", c.p.Account.User, r)
		}
	}
	// the next search starts from b, a resting
	if _, hits, err := s.apiSearch(ctx, "go"); err != nil || len(hits) != 1 || hits[0].Title != "b@seek acc-b" {
		t.Fatalf("after a's rest: %+v %v", hits, err)
	}
	rows, _ := searchRows(t)
	n429 := 0
	for _, r := range rows {
		if r.Status == 429 {
			n429++
			if r.ProviderKeyName != "a@seek" {
				t.Errorf("the refusal's row names %q", r.ProviderKeyName)
			}
		}
		if strings.Contains(r.ProviderKeyName, "spent") || strings.Contains(r.ProviderKeyID, "seekco") {
			t.Errorf("a row shows the account's token or key: %+v", r)
		}
	}
	if n429 != 1 {
		t.Errorf("%d refusals logged: %+v", n429, rows)
	}

	// every account rate limited: the error says so, and the next API answers
	if err := provider.SetLoginOn("plugin:seekco", "b@seek", false); err != nil {
		t.Fatal(err)
	}
	forgetSearchKeys()
	if _, hits, err := s.apiSearch(ctx, "go"); err != nil || len(hits) != 1 || hits[0].Title != "k9 undefined" {
		t.Fatalf("a alone, rate limited: %+v %v", hits, err)
	}
}

// A plugin's search API that signs in is magpie's web search once its
// provider is signed in and the search list never held one (Kayphoon,
// #7632): not before the sign-in, not a second time, and never after the
// user removed the last API they had, a list from before the marker
// included (a removal leaves the list empty, which is theirs).
func TestSignedInSearchAdopted(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/searchauth")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	var seek provider.SearchVendor
	for _, v := range provider.AllSearchVendors() {
		if v.Plugin == abs && v.PluginID == "seek" {
			seek = v
		}
	}
	adopt := func(want string) {
		t.Helper()
		id, err := provider.AdoptSignedInSearch()
		if err != nil || id != want {
			t.Fatalf("adopted %q %v, want %q", id, err, want)
		}
	}
	vendors := func() (out []string) {
		for _, a := range provider.StoredSearchAPIs() {
			out = append(out, a.Vendor)
		}
		return out
	}

	adopt("") // not signed in
	later := time.Now().Add(time.Hour).UnixMilli()
	if _, err := plugin.Import(ctx, "seekco", map[string]any{"type": "oauth", "access": "acc-a", "refresh": "r-a", "expires": later, "accountId": "a@seek"}); err != nil {
		t.Fatal(err)
	}
	adopt(seek.ID) // the keyed entry, which has no sign-in, is not
	if got := vendors(); len(got) != 1 || got[0] != seek.ID {
		t.Fatalf("search list %v", got)
	}
	if as := provider.SearchAPIs(); len(as) != 1 || as[0].Vendor != seek.ID {
		t.Fatalf("asked %+v", as)
	}
	adopt("") // on the list already
	if err := provider.RemoveSearchAPI(seek.ID); err != nil {
		t.Fatal(err)
	}
	adopt("") // removed by the user: the empty list is theirs
	if got := vendors(); len(got) != 0 {
		t.Fatalf("search list after removing it %v", got)
	}

	// a list written before the marker, its last API then removed
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "searchesSet")
	m["searches"] = []map[string]any{{"vendor": "searxng", "url": "https://searx.example", "pick": ""}}
	b, _ = json.Marshal(m)
	if err := os.WriteFile(provider.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.RemoveSearchAPI("searxng"); err != nil {
		t.Fatal(err)
	}
	adopt("")

	// one never set up at all is added
	delete(m, "searches")
	b, _ = json.Marshal(m)
	if err := os.WriteFile(provider.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	adopt(seek.ID)
}

// A search list holding the withdrawn Antigravity plugin's search API,
// which Kayphoon (#7632) set up by hand while agy-cli had none, gets
// agy-cli's in front of it once agy-cli is signed in, and a search then
// asks agy: the list was never empty, so the adoption above left it as it
// was and agy was never used. The old entry stays after it, switched off
// and mixed as it was, and it is carried once: agy removed by the user
// stays removed, and one the user put on the list already is not added
// again.
func TestWithdrawnSearchCarriedToAgy(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// a plugin of agy-cli's id and search hook: agy-cli:agy, as the real
	// one's, signing in as its provider agy-cli
	dir := filepath.Join(t.TempDir(), "agy-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	js := `export default async () => ({
  auth: { provider: "agy-cli", methods: [{ type: "api", label: "agy" }] },
  search: {
    agy: { name: "Antigravity (agy)", auth: "agy-cli", async execute({ query }, ctx) {
      const a = await ctx.getAuth()
      return [{ title: "agy " + a.accountId, url: "https://agy.invalid/" + query, text: "found" }]
    } },
  },
})
`
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	const agy, old = "agy-cli:agy", "antigravity:antigravity"
	if _, ok := provider.SearchVendorOf(agy); !ok {
		t.Fatalf("no %s in %+v", agy, provider.AllSearchVendors())
	}
	// Kayphoon's list: a SearXNG, then the old plugin's API, whose plugin
	// is gone; written as magpie wrote it, vendors and all
	writeList := func(list []map[string]any, carried []string) {
		t.Helper()
		b, err := os.ReadFile(provider.Path())
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		m := map[string]any{}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
		}
		m["searches"], m["searchesSet"] = list, true
		if carried == nil {
			delete(m, "searchesCarried")
		} else {
			m["searchesCarried"] = carried
		}
		b, _ = json.Marshal(m)
		if err := os.WriteFile(provider.Path(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeList([]map[string]any{{"vendor": "searxng", "url": "https://searx.example"}, {"vendor": old, "mix": true}}, nil)
	adopt := func(want string) {
		t.Helper()
		id, err := provider.AdoptSignedInSearch()
		if err != nil || id != want {
			t.Fatalf("adopted %q %v, want %q", id, err, want)
		}
	}
	list := func() string {
		var out []string
		for _, a := range provider.StoredSearchAPIs() {
			s := a.Vendor
			if a.Off {
				s += "(off)"
			}
			if a.Mix {
				s += "(mix)"
			}
			out = append(out, s)
		}
		return strings.Join(out, " ")
	}

	adopt("") // agy-cli not signed in: nothing yet
	if got := list(); got != "searxng "+old+"(mix)" {
		t.Fatalf("before the sign-in: %s", got)
	}
	later := time.Now().Add(time.Hour).UnixMilli()
	if _, err := plugin.Import(ctx, "agy-cli", map[string]any{"type": "oauth", "access": "acc", "refresh": "r", "expires": later, "accountId": "k@agy"}); err != nil {
		t.Fatal(err)
	}
	adopt(agy)
	if got := list(); got != "searxng "+agy+"(mix) "+old+"(mix)" {
		t.Fatalf("carried: %s", got)
	}
	// a search asks agy: the old plugin is gone, so agy is the one API
	// besides the SearXNG, which is taken out here
	if err := provider.SwitchSearchAPI("searxng", false); err != nil {
		t.Fatal(err)
	}
	if _, hits, err := New().apiSearch(ctx, "go"); err != nil || len(hits) != 1 || hits[0].Title != "agy k@agy" {
		t.Fatalf("the search after the carry: %+v %v", hits, err)
	}
	adopt("") // once
	if err := provider.RemoveSearchAPI(agy); err != nil {
		t.Fatal(err)
	}
	adopt("") // removed by the user: stays removed
	if got := list(); got != "searxng(off) "+old+"(mix)" {
		t.Fatalf("after removing agy: %s", got)
	}

	// switched off, the old one gives agy switched off; one the user had
	// put on the list already stays where they put it
	writeList([]map[string]any{{"vendor": old, "off": true}}, nil)
	adopt(agy)
	if got := list(); got != agy+"(off) "+old+"(off)" {
		t.Fatalf("an old one switched off: %s", got)
	}
	writeList([]map[string]any{{"vendor": old}, {"vendor": agy}}, nil)
	adopt("")
	if got := list(); got != old+" "+agy {
		t.Fatalf("agy set up already: %s", got)
	}
}
