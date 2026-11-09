package gui

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// 7up6666 on X: Settings › Models › Images offers each routing group with a member
// that draws, under the group's name with no provider of its own (the
// picker puts it with Routing groups); a group of chat models only isn't
// offered, and a picked group none of whose members draws is said missing.
func TestImageGenOffersDrawingGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "pics", Name: "Pics", Chat: "https://pics.example/v1", Key: "sk-p", Models: []string{"text", "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "painters", Name: "Painters", Members: []string{"pics/gpt-image-1"}},
		{ID: "talkers", Name: "Talkers", Members: []string{"pics/text"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	find := func(id string) (modelRef, bool) {
		for _, m := range settingsState().ImageGenModels {
			if m.ID == id {
				return m, true
			}
		}
		return modelRef{}, false
	}
	m, ok := find("group/painters")
	if !ok || m.Provider != "" || m.Name != "Painters" {
		t.Fatalf("drawing group offered as %+v %v", m, ok)
	}
	if _, ok := find("group/talkers"); ok {
		t.Fatal("a chat-only group is offered to draw with")
	}
	if _, ok := find("pics/gpt-image-1"); !ok {
		t.Fatal("the provider's own image model left the list")
	}
	st := settings.Load()
	st.ImageGen = "group/talkers"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if s := settingsState(); s.ImageGenMissing != "group/talkers" {
		t.Fatalf("a picked group that draws nothing isn't said missing: %q", s.ImageGenMissing)
	}
	st.ImageGen = "group/painters"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if s := settingsState(); s.ImageGenMissing != "" || s.ImageGenHidden {
		t.Fatalf("a drawing group pick: missing %q hidden %v", s.ImageGenMissing, s.ImageGenHidden)
	}
}
