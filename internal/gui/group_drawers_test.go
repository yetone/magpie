package gui

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// jzw690 on X: the group editor's "Add a model" offers a provider's
// image models (its live list's gpt-image-2) beside its chat models, as
// drawers apart from them; a group holding one, from a provider whose
// picks leave it out, shows it ready, and Settings › Models › Images offers that
// group. One unticked in its provider's editor isn't offered, unless a
// group names it already.
func TestGroupEditorOffersImageModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	live := []catalog.Model{{ID: "gpt-5.5"}, {ID: "gpt-image-2", Draws: true}, {ID: "gpt-image-1", Draws: true}}
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1"},
		{ID: "pix", Name: "Pix", Key: "k", Chat: "http://127.0.0.1:2/v1", Models: []string{"gpt-5.5"}, HiddenDrawers: []string{"gpt-image-1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ms []modelRef) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	st := groupsState()
	for _, want := range []string{"relay/gpt-image-2", "relay/gpt-image-1", "pix/gpt-image-2"} {
		if !slices.Contains(ids(st.Drawers), want) {
			t.Fatalf("%s not offered: drawers %v", want, ids(st.Drawers))
		}
	}
	if slices.Contains(ids(st.Drawers), "pix/gpt-image-1") {
		t.Fatalf("an unticked image model is offered: %v", ids(st.Drawers))
	}
	for _, m := range st.Models {
		if slices.Contains(ids(st.Drawers), m.ID) || m.ID == "pix/gpt-image-2" {
			t.Fatalf("an image model among the chat models: %s", m.ID)
		}
	}

	if err := provider.SaveGroup(provider.Group{ID: "pix-images", Name: "Pix images", Members: []string{"pix/gpt-image-2", "pix/gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	st = groupsState()
	if !slices.Contains(ids(st.Drawers), "pix/gpt-image-1") {
		t.Fatalf("an unticked image model a group names isn't listed for its card: %v", ids(st.Drawers))
	}
	i := slices.IndexFunc(st.Groups, func(g groupJSON) bool { return g.ID == "pix-images" })
	if i < 0 || !st.Groups[i].Ready {
		t.Fatalf("group not ready: %+v", st.Groups)
	}
	for _, m := range st.Groups[i].Info {
		if !m.Ready || m.Provider != "pix" || m.Model == "" {
			t.Fatalf("member shown as missing: %+v", m)
		}
	}
	if !slices.ContainsFunc(settingsState().ImageGenModels, func(m modelRef) bool { return m.ID == "group/pix-images" }) {
		t.Fatal("Settings › Models › Images doesn't offer the group")
	}
}
