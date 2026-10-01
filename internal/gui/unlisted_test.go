package gui

import (
	"net/url"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider set to "Only through routing groups" keeps its models out of
// the pickers; one in no group was gone from every picker without a word
// (悠悠哥 on Discord: hy4 vanished). The page is told which models are kept
// so and the groups each is used through, none for hy4, for the picker and
// the provider's editor to say so; the tray panel can open the window on a
// new group of it.
func TestUnlistedModelsSaid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "hunyuan", Name: "Hunyuan", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"hy3", "hy4"}, Unlisted: true}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"hunyuan/hy3", "other/m1"}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range provider.Catalog() {
		if e.Group == "" && e.Provider.ID == "hunyuan" {
			t.Fatalf("%s is offered to agents", e.ID)
		}
	}
	got := map[string][]string{}
	for _, m := range state().Unlisted {
		got[m.ID] = m.Groups
		if m.Provider != "Hunyuan" {
			t.Errorf("%s: provider %q", m.ID, m.Provider)
		}
	}
	if len(got) != 2 || !slices.Equal(got["hunyuan/hy3"], []string{"group/mine"}) || got["hunyuan/hy4"] == nil || len(got["hunyuan/hy4"]) != 0 {
		t.Fatalf("unlisted %v, want hy3 in group/mine and hy4 in none", got)
	}
	p, err := provider.Find("hunyuan")
	if err != nil {
		t.Fatal(err)
	}
	j := providerInfo(*p, nil)
	if !slices.Equal(j.Groups["hy3"], []string{"group/mine"}) || len(j.Groups["hy4"]) != 0 {
		t.Fatalf("provider's groups %v", j.Groups)
	}
	if v := mainView(url.Values{"view": {"routing"}, "newgroup": {"hunyuan/hy4"}}); v != "routing&newgroup=hunyuan%2Fhy4" {
		t.Fatalf("window view %q", v)
	}
	if v := mainView(url.Values{"view": {"settings"}, "newgroup": {"x/y"}}); v != "settings" {
		t.Fatalf("window view %q", v)
	}
}
