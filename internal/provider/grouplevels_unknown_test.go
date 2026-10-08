package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #597: a routing group of DeepSeek V4 Pro on Tencent's TokenHub and on its
// Token Plan (deepseek-v4-pro-202606, which nothing magpie reads knows)
// offered no reasoning levels, so Pi listed off alone for it and ran it
// without reasoning while magpie showed max. A model a provider serves that
// its vendor's list leaves out takes the levels the gateway fits an effort
// to (Provider.Efforts), and a member whose levels nothing knows takes
// whatever the agent asks rather than taking the others' away. A member
// known to have none still does.
func TestGroupLevelsWithUnknownMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "deepseek": {"models": {"deepseek-v4-pro": {"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","reasoning":true,
	    "reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"limit":{"context":1000000}}}},
	  "tencent-tokenhub": {"models": {"hy3": {"id":"hy3","reasoning":true,"reasoning_options":[{"type":"effort","values":["none","high"]}]}}},
	  "plaincat": {"models": {"plain-chat": {"id":"plain-chat","limit":{"context":128000}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []Provider{
		// TokenHub's models.dev list has its Hy models alone
		{ID: "th", Name: "TokenHub", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "tencent-tokenhub", Models: []string{"deepseek-v4-pro"}},
		{ID: "tp", Name: "Token Plan", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4-pro-202606"}},
		{ID: "pl", Name: "Plain", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "plaincat", Models: []string{"plain-chat"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"none", "low", "high", "max"}
	entry := func(id string) Entry {
		t.Helper()
		e, ok := EntryOf(id)
		if !ok {
			t.Fatalf("%s not listed", id)
		}
		return e
	}
	// the catalog and the gateway agree on the model's levels
	p, _ := Find("th")
	if got := p.Efforts("deepseek-v4-pro"); !slices.Equal(got, want) {
		t.Fatalf("gateway's levels: %v", got)
	}
	if e := entry("th/deepseek-v4-pro"); !slices.Equal(e.Efforts, want) {
		t.Fatalf("catalog's levels: %v", e.Efforts)
	}
	for name, members := range map[string][]string{
		"ds":    {"tp/deepseek-v4-pro-202606", "th/deepseek-v4-pro"},
		"plain": {"th/deepseek-v4-pro", "pl/plain-chat"},
		"tp":    {"tp/deepseek-v4-pro-202606"},
	} {
		if err := SaveGroup(Group{Name: name, Members: members}); err != nil {
			t.Fatal(err)
		}
	}
	if e := entry("group/ds"); !slices.Equal(e.Efforts, want) || !e.Reasoning {
		t.Fatalf("ds: levels %v, reasoning %v", e.Efforts, e.Reasoning)
	}
	// a member listed without levels still takes them away, and a group
	// of models nothing knows offers none
	if e := entry("group/plain"); len(e.Efforts) != 0 {
		t.Fatalf("plain: levels %v", e.Efforts)
	}
	if e := entry("group/tp"); len(e.Efforts) != 0 {
		t.Fatalf("tp: levels %v", e.Efforts)
	}
}
