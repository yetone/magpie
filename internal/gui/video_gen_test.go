package gui

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings › Models › Images › Video generation lists a custom provider's model the
// user marked a video model (#1399), and none of its others; one unticked
// in its editor is left out unless it is the one picked; a pick no longer
// marked is said missing, with Automatic's named.
func TestVideoGenModelsListed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "studio", Name: "Studio", Chat: "https://studio.example/v1", Key: "sk-s", Models: []string{"chat-1", "seedance-x", "kling-y"}}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"studio/seedance-x", "studio/kling-y"} {
		if err := provider.SetModelKind(ref, provider.KindVideo); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() (ids []string) {
		for _, m := range settingsState().VideoGenModels {
			if m.Provider == "studio" {
				ids = append(ids, m.ID)
			}
		}
		slices.Sort(ids)
		return
	}
	if got := listed(); !slices.Equal(got, []string{"studio/kling-y", "studio/seedance-x"}) {
		t.Fatalf("listed %v", got)
	}
	if a := settingsState().VideoGenAuto; a == "" {
		t.Fatal("no automatic video model")
	}

	p, _ := provider.Find("studio")
	q := *p
	q.HiddenVideomakers = []string{"kling-y"}
	if err := provider.Save(q); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"studio/seedance-x"}) {
		t.Fatalf("an unticked one is listed: %v", got)
	}
	st := settings.Load()
	st.VideoGen = "studio/kling-y"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"studio/kling-y", "studio/seedance-x"}) {
		t.Fatalf("the unticked pick isn't listed: %v", got)
	}
	if m := settingsState().VideoGenMissing; m != "" {
		t.Fatalf("a marked pick said missing: %q", m)
	}

	if err := provider.SetModelKind("studio/kling-y", ""); err != nil {
		t.Fatal(err)
	}
	s := settingsState()
	if s.VideoGenMissing != "studio/kling-y" || s.VideoGenAuto != "studio/seedance-x" {
		t.Fatalf("unmarked pick: missing %q, automatic %q", s.VideoGenMissing, s.VideoGenAuto)
	}
}
