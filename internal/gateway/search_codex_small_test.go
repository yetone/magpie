package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A Codex account searches for a model that can't with its cheap model,
// not the flagship its list starts with: lucy_85908 (Discord, v0.1.2682)
// saw every search for a third-party model in Codex go to gpt-6.1-sol,
// 10,532 tokens in, on the Codex account's allowance. The list carries no
// prices, so the first was picked; the list price models.dev gives the
// models says gpt-6-luna is the cheap one. Without a catalog at all, a
// luna is GPT-6's small tier, as a mini is. A model the user names in
// Settings is still the one asked.
func TestCodexSearchesWithItsCheapModel(t *testing.T) {
	codexSignedIn(t)
	list := []catalog.Model{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-astra"}, {ID: "gpt-6-sol"}, {ID: "gpt-6-luna"}, {ID: "gpt-5.6-sol"}, {ID: provider.CodexAutoReview}}
	if err := catalog.SaveLive("codex", provider.CodexBase, list); err != nil {
		t.Fatal(err)
	}
	pick := func() (string, string) {
		p, m, ok := searcher()
		if !ok {
			t.Fatal("no searcher with a Codex account signed in")
		}
		return p.ID, m
	}

	// no catalog: by the name
	catalog.Reset()
	if id, m := pick(); id != "codex" || m != "gpt-6-luna" {
		t.Fatalf("searcher = %s/%s with no catalog, want codex/gpt-6-luna", id, m)
	}

	// priced: the cheapest, whatever its name
	writeCatalog(t, `{"openai":{"id":"openai","models":{
  "gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":5,"output":30}},
  "gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":10,"output":50}},
  "gpt-6-sol":{"id":"gpt-6-sol","cost":{"input":2.5,"output":15}},
  "gpt-6-luna":{"id":"gpt-6-luna","cost":{"input":0.25,"output":2}},
  "gpt-5.6-sol":{"id":"gpt-5.6-sol","cost":{"input":1.25,"output":10}},
  "gpt-5.6-luna":{"id":"gpt-5.6-luna","cost":{"input":0.1,"output":0.4}}}}}`)
	if id, m := pick(); id != "codex" || m != "gpt-6-luna" {
		t.Fatalf("searcher = %s/%s priced, want codex/gpt-6-luna", id, m)
	}
	// a list with no small tier: the cheapest by its price, not the first
	if err := catalog.SaveLive("codex", provider.CodexBase, list[:3]); err != nil {
		t.Fatal(err)
	}
	if id, m := pick(); id != "codex" || m != "gpt-6-sol" {
		t.Fatalf("searcher = %s/%s priced with no luna, want the cheapest, codex/gpt-6-sol", id, m)
	}

	// the user's own pick wins
	st := settings.Load()
	st.Searcher = "codex/gpt-6.1-sol"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if id, m := pick(); id != "codex" || m != "gpt-6.1-sol" {
		t.Fatalf("searcher = %s/%s, want the one Settings names, codex/gpt-6.1-sol", id, m)
	}
}
