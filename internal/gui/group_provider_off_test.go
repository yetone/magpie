package gui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group's member whose provider is switched off is said as that on the
// Routing page, with its provider's name and model, rather than as a bare
// id nothing serves: the group skips it until the provider is on again.
// One whose provider is gone says nothing of a provider.
func TestGroupMemberOfASwitchedOffProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b", "c"} {
		if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id) + " Cloud", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"a/x", "b/x", "c/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetOff("b", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("c"); err != nil {
		t.Fatal(err)
	}
	var mine *groupJSON
	st := groupsState()
	for i := range st.Groups {
		if st.Groups[i].ID == "mine" {
			mine = &st.Groups[i]
		}
	}
	if mine == nil {
		t.Fatal("no group mine")
	}
	info := map[string]memberJSON{}
	for _, m := range mine.Info {
		info[m.ID] = m
	}
	if m := info["a/x"]; !m.Ready || m.ProviderOff {
		t.Fatalf("a/x: %+v", m)
	}
	if m := info["b/x"]; m.Ready || !m.ProviderOff || m.Name != "B Cloud" || m.Model != "x" || m.Provider != "b" {
		t.Fatalf("b/x, its provider off: %+v", m)
	}
	if m := info["c/x"]; m.Ready || m.ProviderOff || m.Name != "" {
		t.Fatalf("c/x, its provider gone: %+v", m)
	}
	b, _ := json.Marshal(info["b/x"])
	if !strings.Contains(string(b), `"providerOff":true`) {
		t.Fatalf("on the wire: %s", b)
	}
	// on again, it is a member like any other
	if err := provider.SetOff("b", false); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupsState().Groups {
		for _, m := range g.Info {
			if g.ID == "mine" && m.ID == "b/x" && (!m.Ready || m.ProviderOff) {
				t.Fatalf("b on again: %+v", m)
			}
		}
	}
}
