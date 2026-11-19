package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A cheaper model whose id says it is the small one is the cheap one
// beside a flagship that costs less than four times as much
// (@qinzhidaoa on X): DeepSeek lists deepseek-v4-pro at $1.98 out and
// V4.1 Flash at $0.60, 3.3 times less, so "Hard to strong, easy to
// cheap" and "Cheap first" read "Needs a model cheap beside
// DeepSeek-V4-Pro" with both models on the provider.
func TestTemplatesCheapBesideACloseFlagship(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	// models.dev's DeepSeek as the catalog magpie ships (2026-11-10) has it
	b, err := os.ReadFile(filepath.Join("testdata", "models-dev-deepseek-2026-11-10.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	// the reporter's provider: the DeepSeek preset, its two models
	if err := Save(Provider{ID: "deepseek", Name: "DeepSeek", Key: "sk-test", Catalog: "deepseek",
		Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
		Models: []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}}); err != nil {
		t.Fatal(err)
	}
	ms := templateModels()
	for _, m := range ms {
		t.Logf("%s out=%v maker=%v small=%v", m.e.ID, m.out, m.maker, m.small)
	}
	want := map[string][]string{
		TemplateSmart:   {"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4.1-flash"},
		TemplateThrifty: {"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4-pro"},
	}
	for _, tp := range Templates() {
		if w, ok := want[tp.Kind]; ok && (tp.Need != "" || !slices.Equal(tp.Group.Members, w)) {
			t.Errorf("%s = need %q of %q, members %v; want %v", tp.Kind, tp.Need, tp.Of, tp.Group.Members, w)
		}
	}

	// the class: Sonnet and Haiku are three times apart, Pro and Flash four
	for _, c := range [][2]rated{
		{tm("anthropic", "claude-sonnet-5-5", 15, true, "2026-09-01"), tm("anthropic", "claude-haiku-5-5", 5, true, "2026-09-01")},
		{tm("google", "gemini-3-pro", 12, true, "2026-08-01"), tm("google", "gemini-3-flash", 3.5, true, "2026-08-01")},
	} {
		if tp := templateOf(TemplateSmart, c[:], nil); !slices.Equal(tp.Group.Members, []string{c[0].e.ID, c[1].e.ID}) {
			t.Errorf("%s beside %s: %+v", c[1].e.Model, c[0].e.Model, tp)
		}
	}
	// a model no cheaper is never the cheap one, however its id reads,
	// nor one that costs as much without a small id
	for _, c := range [][2]rated{
		{tm("x", "big-model", 2, true, ""), tm("x", "big-model-flash", 2, true, "")},
		{tm("x", "big-model", 2, true, ""), tm("x", "other-model", 1, true, "")},
	} {
		if tp := templateOf(TemplateSmart, c[:], nil); tp.Need != NeedCheap {
			t.Errorf("%s beside %s: %+v, want no cheap one", c[1].e.Model, c[0].e.Model, tp)
		}
	}
}
