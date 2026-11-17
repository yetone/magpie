package provider

import (
	"slices"
	"testing"
)

// A provider can be kept out of the groups magpie finds (chenxiang0010 on
// Discord: auto/kimi-k3.1-pro took in a provider they didn't want there):
// its models are in no found group, a model one other provider serves is
// no group, a request still naming that group goes to the model from a
// provider not kept out, the groups the user made or changed keep it, and
// it follows the provider's rename. Let back in, the groups are as before.
func TestAutoGroupSkip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b", "c"} {
		models := []string{"kimi-k3", "x"}
		if id == "c" {
			models = []string{"kimi-k3", "glm-5.3"}
		}
		if id == "b" {
			models = []string{"kimi-k3", "x", "glm-5.3", "y"}
		}
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	// a found group the user changed is theirs, and keeps b
	if err := SaveGroup(Group{ID: "auto-x", Name: "X", Members: []string{"b/x", "a/x"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	members := func(id string) []string {
		for _, g := range Groups() {
			if g.ID == id && !g.Hidden {
				return g.Members
			}
		}
		return nil
	}
	if got := members("auto-kimi-k3"); !slices.Equal(got, []string{"a/kimi-k3", "b/kimi-k3", "c/kimi-k3"}) {
		t.Fatalf("before: %v", got)
	}
	if err := SetAutoGroupSkip([]string{" B ", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := AutoGroupSkip(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("skip: %v", got)
	}
	if got := members("auto-kimi-k3"); !slices.Equal(got, []string{"a/kimi-k3", "c/kimi-k3"}) {
		t.Fatalf("b kept out: %v", got)
	}
	if got := members("auto-glm-5-3"); got != nil {
		t.Fatalf("a model only c serves besides b is still a group: %v", got)
	}
	if got := members("auto-x"); !slices.Equal(got, []string{"b/x", "a/x"}) {
		t.Fatalf("the user's own found group lost b: %v", got)
	}
	// a request for the group skipping took away goes to c's, not b's
	for in, want := range map[string]string{"group/auto-glm-5-3": "c/glm-5.3", "auto/glm-5.3": "c/glm-5.3"} {
		if got, ok := AutoStandIn(in); !ok || got != want {
			t.Fatalf("%s: %q %v, want %s", in, got, ok, want)
		}
	}
	// none for a group there is, or one gone for another reason
	if _, ok := AutoStandIn("group/auto-kimi-k3"); ok {
		t.Fatal("a stand-in for a group there is")
	}
	// y, b's alone, is no group: its group's id still reaches it there, as
	// a group gone for want of a second provider does (canlee886 on X)
	if got, ok := AutoStandIn("group/auto-y"); !ok || got != "b/y" {
		t.Fatalf("group/auto-y: %q %v, want b/y", got, ok)
	}
	if _, ok := AutoStandIn("group/auto-nothing"); ok {
		t.Fatal("a stand-in for a model no provider serves")
	}
	// kept out under its new id
	if err := Rename("b", "bee"); err != nil {
		t.Fatal(err)
	}
	if got := AutoGroupSkip(); !slices.Equal(got, []string{"bee"}) {
		t.Fatalf("after rename: %v", got)
	}
	if got := members("auto-kimi-k3"); !slices.Equal(got, []string{"a/kimi-k3", "c/kimi-k3"}) {
		t.Fatalf("renamed, back in: %v", got)
	}
	if err := SetAutoGroupSkip(nil); err != nil {
		t.Fatal(err)
	}
	if got := members("auto-glm-5-3"); !slices.Equal(got, []string{"bee/glm-5.3", "c/glm-5.3"}) {
		t.Fatalf("let back in: %v", got)
	}
	if _, ok := AutoStandIn("group/auto-glm-5-3"); ok {
		t.Fatal("a stand-in once nothing is kept out")
	}
}
