package gui

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Gateway view's routing groups say what agents are told of them, as
// its models do — their reasoning levels, images, context and output — and
// name the models they send to, for the model list's tooltip and search
// (ARNO on Discord).
func TestGatewayGroupsSayWhatTheyTake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "ra", Name: "Relay A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}},
		{ID: "rb", Name: "Relay B", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5", "glm-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mix", Name: "Mix", Members: []string{"ra/gpt-5.5:high", "rb/glm-5"}}); err != nil {
		t.Fatal(err)
	}
	var entry provider.Entry
	for _, e := range provider.Catalog() {
		if e.ID == "group/mix" {
			entry = e
		}
	}
	if entry.ID == "" {
		t.Fatal("group/mix isn't in the catalog")
	}
	var got *gwGroupJSON
	for _, g := range providersState().Gateway.Groups {
		if g.ID == "group/mix" {
			got = &g
		}
	}
	if got == nil {
		t.Fatal("group/mix isn't listed")
	}
	if !slices.Equal(got.Members, []string{"ra/gpt-5.5", "rb/glm-5"}) {
		t.Errorf("members %v", got.Members)
	}
	if !slices.Equal(got.Efforts, entry.Efforts) || got.Images != entry.Images || got.Context != entry.Context || got.Output != entry.Output {
		t.Errorf("listed %v %v %d %d, the catalog says %v %v %d %d", got.Efforts, got.Images, got.Context, got.Output, entry.Efforts, entry.Images, entry.Context, entry.Output)
	}
}
