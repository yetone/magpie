package gui

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The editor receives an authoritative list, even empty, so a Jev Router
// chat model on a remote isn't guessed to be a System One model.
func TestRemoteMagpieDecisionPicker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: "http://127.0.0.1:1", Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	for _, decisions := range [][]catalog.Model{
		{{ID: "judge/custom", Decides: true}},
		nil,
	} {
		ms := append([]catalog.Model{{ID: "relay/typesafe/jev-router"}}, decisions...)
		if err := catalog.SaveLive(p.ID, p.Chat, ms); err != nil {
			t.Fatal(err)
		}
		out := providerInfo(*p, nil)
		want := []string{"relay/typesafe/jev-router"}
		for _, m := range decisions {
			want = append(want, m.ID)
		}
		var got []string
		for _, m := range out.Models {
			got = append(got, m.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("editor models after refresh: %v; want %v", got, want)
		}
		if out.Deciders == nil || len(*out.Deciders) != len(decisions) {
			t.Fatalf("editor decision list: %v", out.Deciders)
		}
		if len(decisions) > 0 && !slices.Equal(*out.Deciders, []string{"judge/custom"}) {
			t.Fatalf("editor decisions: %v", *out.Deciders)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		json.Unmarshal(b, &fields)
		if len(decisions) == 0 && string(fields["deciders"]) != "[]" {
			t.Fatalf("empty decisions omitted: %s", b)
		}
		if out.Decide != "http://127.0.0.1:1/v1" || out.Chat != out.Decide {
			t.Fatalf("remote API bases: %+v", out)
		}
	}
}
