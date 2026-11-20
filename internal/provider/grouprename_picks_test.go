package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// A routing group picked for an agent shown only its picks stays picked
// when it is renamed (#2289, eachann1024: Pi's list lost its groups), and
// what other agents' lists say of it — taken out, put in an order, sent
// fast — moves to the new id too; nothing else of theirs changes.
func TestRenameGroupKeepsPicks(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"smart", "grok"} {
		if err := SaveGroup(Group{ID: id, Name: id, Members: []string{"relay/m1"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := settings.Load()
	s.PickedModels = map[string][]string{"pi": {"group/smart", "relay/m1"}, "omp": {"relay/m2"}}
	s.HiddenModels = map[string][]string{"codex": {"group/smart", "relay/m2"}}
	s.OrderedModels = map[string][]string{"claude": {"relay/m2", "group/smart", "relay/m1"}}
	s.FastPicks = map[string][]string{"codex": {"group/smart"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if shown, _ := CatalogFor("pi"); !slices.Contains(ids(shown), "group/smart") {
		t.Fatalf("not picked to begin with: %v", ids(shown))
	}
	if err := RenameGroup("smart", "zeta"); err != nil {
		t.Fatal(err)
	}
	shown, _ := CatalogFor("pi")
	if !slices.Equal(ids(shown), []string{"group/zeta", "relay/m1"}) {
		t.Fatalf("pi shown %v", ids(shown))
	}
	s = settings.Load()
	for name, got := range map[string][]string{
		"picked pi":  s.PickedModels["pi"],
		"picked omp": s.PickedModels["omp"],
		"hidden":     s.HiddenModels["codex"],
		"order":      s.OrderedModels["claude"],
		"fast":       s.FastPicks["codex"],
	} {
		want := map[string][]string{
			"picked pi":  {"group/zeta", "relay/m1"},
			"picked omp": {"relay/m2"},
			"hidden":     {"group/zeta", "relay/m2"},
			"order":      {"relay/m2", "group/zeta", "relay/m1"},
			"fast":       {"group/zeta"},
		}[name]
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	// the other group, not renamed, is still off in Pi's list
	if slices.Contains(ids(shown), "group/grok") {
		t.Fatal("an unpicked group came on")
	}
}

// A group magpie found, made the user's under an id of their own, stays
// in the lists that showed it (@CHENGJIAHEGOXIS on X: DeepSeek-API-Agents
// was gone from Codex's picker). Codex picked it by its auto/<model>,
// which the renamed group no longer answers to; Claude's list narrowed to
// it by that ref, and OpenCode's to a group by its bare id, rename with it.
func TestRenameFoundGroupKeepsItsPicks(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []Provider{
		{ID: "dss", Name: "dss", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "deepseek-agents", Name: "DeepSeek-Agents", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{ID: "open-chatgpt", Name: "Open-ChatGPT", Members: []string{"dss/deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	found := "auto-deepseek-v4-1-flash"
	refs := GroupRefs(found)
	if len(refs) != 2 || refs[1] != "auto/deepseek-v4.1-flash" {
		t.Fatalf("found group's refs %v", refs)
	}
	if err := SetOnlyPicked("codex", true); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.Visible = map[string][]string{
		"claude":   {"auto/deepseek-v4.1-flash", "dss"},
		"opencode": {"OPEN-CHATGPT"},
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"codex", "claude"} {
		if shown, _ := CatalogFor(a); !slices.Contains(ids(shown), refs[1]) {
			t.Fatalf("%s doesn't show the found group to begin with: %v", a, ids(shown))
		}
	}
	if err := RenameGroup(found, "deepseek-api-agents"); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("open-chatgpt", "open-gpt"); err != nil {
		t.Fatal(err)
	}
	for a, want := range map[string]string{"codex": "group/deepseek-api-agents", "claude": "group/deepseek-api-agents", "opencode": "group/open-gpt"} {
		if shown, _ := CatalogFor(a); !slices.Contains(ids(shown), want) {
			t.Errorf("%s lost %s: %v", a, want, ids(shown))
		}
	}
	s = settings.Load()
	if p := s.PickedModels["codex"]; slices.Contains(p, refs[1]) || !slices.Contains(p, "group/deepseek-api-agents") {
		t.Errorf("codex picks %v", p)
	}
	if v := s.Visible["claude"]; !slices.Equal(v, []string{"group/deepseek-api-agents", "dss"}) {
		t.Errorf("claude visible %v", v)
	}
	if v := s.Visible["opencode"]; !slices.Equal(v, []string{"open-gpt"}) {
		t.Errorf("opencode visible %v", v)
	}
}
