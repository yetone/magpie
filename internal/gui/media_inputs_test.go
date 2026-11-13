package gui

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The Gateway list shows video and audio beside images when a model's
// source listed them (pingqipai3 on X): only those two, in that order,
// whatever form its list wrote them in.
func TestMediaInputs(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"text", "image", "pdf"}, nil},
		{[]string{"audio", "text", "video"}, []string{"video", "audio"}},
		{[]string{"Video/MP4", "audio/wav"}, []string{"video", "audio"}},
		{[]string{"image", "audio"}, []string{"audio"}},
	} {
		if got := mediaInputs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("mediaInputs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// What the provider's model list says it takes reaches the Gateway list:
// each model's video and audio, and a group's from any one of its models.
func TestGatewayListSaysVideoAndAudio(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "p", Name: "P", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"omni", "ear", "text"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("p", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "omni", InputModalities: []string{"text", "image", "video", "audio"}},
		{ID: "ear", InputModalities: []string{"text", "audio"}},
		{ID: "text", InputModalities: []string{"text"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"p/ear", "p/text"}}); err != nil {
		t.Fatal(err)
	}
	s := providersState()
	got := map[string][]string{}
	for _, p := range s.Providers {
		if p.ID == "p" {
			for _, m := range p.Models {
				got[m.ID] = m.Inputs
			}
		}
	}
	for id, want := range map[string][]string{"omni": {"video", "audio"}, "ear": {"audio"}, "text": nil} {
		if !slices.Equal(got[id], want) {
			t.Errorf("%s says %q, want %q", id, got[id], want)
		}
	}
	i := slices.IndexFunc(s.Gateway.Groups, func(g gwGroupJSON) bool { return g.ID == "group/g" })
	if i < 0 {
		t.Fatal("group/g isn't listed")
	}
	if g := s.Gateway.Groups[i]; !slices.Equal(g.Inputs, []string{"audio"}) {
		t.Errorf("group/g says %q, want audio (one of its models takes it)", g.Inputs)
	}
}
