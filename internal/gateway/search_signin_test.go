package gateway

import (
	"context"
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
