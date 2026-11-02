package gui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A routing group's editor reads each member off the served catalog entry:
// whether agents are told it takes images, whether anything said so either
// way, the levels it reasons at and the window it holds. The first is the
// same expression the gateway decides by when it describes an image for a
// model that can't see one (blindTo reads Entry.Images), so the editor cannot
// say a member sees images that the gateway would describe them for.
func TestGroupModelsCarryWhatTheyTake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	yes, no := true, false
	if err := provider.Save(provider.Provider{ID: "p", Name: "P", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"eye", "text", "mystery", "mine"}}); err != nil {
		t.Fatal(err)
	}
	// what each model's own list says: one sees, one takes none, one says
	// nothing at all
	if err := catalog.SaveLive("p", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "eye", Context: 1048576, Efforts: []string{"low", "medium", "high"}, Images: true, ImageInput: &yes},
		{ID: "text", Context: 200000, ImageInput: &no},
		{ID: "mystery", Context: 128000},
		{ID: "mine", Context: 64000, ImageInput: &no},
	}); err != nil {
		t.Fatal(err)
	}
	// the user's own answer for one its list said took none
	st := settings.Load()
	st.ModelImages = map[string]bool{"p/mine": true}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"p/eye", "p/text", "p/mystery", "p/mine"}}); err != nil {
		t.Fatal(err)
	}

	byID := map[string]modelRef{}
	for _, m := range groupsState().Models {
		byID[m.ID] = m
	}
	for _, tc := range []struct {
		id      string
		images  bool
		unknown bool
		context int
		efforts int
	}{
		{"p/eye", true, false, 1048576, 3},    // its list says it sees
		{"p/text", false, false, 200000, 0},   // its list says it takes none
		{"p/mystery", false, true, 128000, 0}, // nothing was read of it either way
		{"p/mine", true, false, 64000, 0},     // the user's answer, over its list
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s is not in the group's models", tc.id)
		}
		if m.Images != tc.images || m.ImagesUnknown != tc.unknown {
			t.Errorf("%s: images %v unknown %v, want %v %v", tc.id, m.Images, m.ImagesUnknown, tc.images, tc.unknown)
		}
		if m.Context != tc.context {
			t.Errorf("%s: context %d, want %d", tc.id, m.Context, tc.context)
		}
		if len(m.Efforts) != tc.efforts {
			t.Errorf("%s: efforts %v, want %d of them", tc.id, m.Efforts, tc.efforts)
		}
	}

	// and the group's own members say the same of them
	var g groupJSON
	for _, x := range groupsState().Groups {
		if x.ID == "g" {
			g = x
		}
	}
	if len(g.Info) != 4 {
		t.Fatalf("the group lists %d members", len(g.Info))
	}
	for i, want := range []struct {
		id      string
		images  bool
		unknown bool
	}{{"p/eye", true, false}, {"p/text", false, false}, {"p/mystery", false, true}, {"p/mine", true, false}} {
		if g.Info[i].ID != want.id || g.Info[i].Images != want.images || g.Info[i].ImagesUnknown != want.unknown {
			t.Errorf("member %d is %s images %v unknown %v, want %s %v %v", i, g.Info[i].ID, g.Info[i].Images, g.Info[i].ImagesUnknown, want.id, want.images, want.unknown)
		}
	}
}

// A member that is itself a routing group is in none of groups.models
// (groupsState leaves groups out of that list), so the editor read nothing
// for it and its row carried no chips at all. It now says what agents are
// told of it — the window the largest of its models has, whether any of them
// sees images, and the levels every one of them has — which is what
// provider.groupEntries gives the group itself.
func TestGroupMemberThatIsAGroupCarriesWhatAgentsSee(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	yes, no := true, false
	if err := provider.Save(provider.Provider{ID: "p", Name: "P", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"a1", "a2", "b1", "b2", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("p", "http://127.0.0.1:1/v1", []catalog.Model{
		// one sees, the other doesn't; both reason low/high/max
		{ID: "a1", Images: true, ImageInput: &yes, Context: 1000000, Efforts: []string{"low", "high", "max"}},
		{ID: "a2", ImageInput: &no, Context: 400000, Efforts: []string{"low", "high", "max"}},
		// nothing in common between these two
		{ID: "b1", ImageInput: &no, Context: 200000, Efforts: []string{"low"}},
		{ID: "b2", ImageInput: &no, Context: 128000, Efforts: []string{"high"}},
		{ID: "plain", ImageInput: &no, Context: 64000},
	}); err != nil {
		t.Fatal(err)
	}
	// inner1: a member sees images, so the group does; levels shared
	if err := provider.SaveGroup(provider.Group{ID: "inner1", Name: "Same", Routing: provider.Ordered,
		Members: []string{"p/a1", "p/a2"}}); err != nil {
		t.Fatal(err)
	}
	// inner2: no member sees; no level in common
	if err := provider.SaveGroup(provider.Group{ID: "inner2", Name: "Diff", Routing: provider.Ordered,
		Members: []string{"p/b1", "p/b2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "outer", Name: "Outer", Routing: provider.Ordered,
		Members: []string{"group/inner1", "group/inner2", "p/plain"}}); err != nil {
		t.Fatal(err)
	}

	// a group is not among the models the page looks a member up in: this is
	// the gap, and the memberInfo below is what makes up for it
	for _, m := range groupsState().Models {
		if strings.HasPrefix(m.ID, provider.GroupPrefix) {
			t.Errorf("groups.models holds the group %s after all", m.ID)
		}
	}

	var outer groupJSON
	for _, x := range groupsState().Groups {
		if x.ID == "outer" {
			outer = x
		}
	}
	if len(outer.Info) != 3 {
		t.Fatalf("outer lists %d members", len(outer.Info))
	}
	byMember := map[string]memberJSON{}
	for _, mi := range outer.Info {
		byMember[mi.ID] = mi
	}

	inner := byMember["group/inner1"]
	if !inner.Group {
		t.Fatal("a group in a group is not marked as one")
	}
	if inner.Context != 1000000 {
		t.Errorf("the group's window is %d, want the largest of its models' (1000000)", inner.Context)
	}
	if !inner.Images {
		t.Error("the group is not said to take images, though a model in it sees")
	}
	if inner.ImagesUnknown {
		t.Error("the group is called unknown, though its members were read")
	}
	if !slices.Equal(inner.Efforts, []string{"low", "high", "max"}) {
		t.Errorf("the group offers %v, want the levels every model in it has", inner.Efforts)
	}

	// no level in common: the group offers none, and is not called unknown
	diff := byMember["group/inner2"]
	if diff.Images {
		t.Error("a group none of whose models sees is said to take images")
	}
	if diff.ImagesUnknown {
		t.Error("a group whose models were all read is called unknown")
	}
	if len(diff.Efforts) != 0 {
		t.Errorf("a group whose models share no level offers %v", diff.Efforts)
	}

	// a plain member is unchanged by any of this
	plain := byMember["p/plain"]
	if plain.Group || len(plain.Efforts) != 0 || plain.Context != 64000 {
		t.Errorf("the plain member is %+v", plain)
	}

	// and each member's row is readable: a member that carries any of these
	// draws chips (routing/client.js reads memberInfo when the member is a group)
	for _, mi := range outer.Info {
		if !mi.Group && mi.Context == 0 {
			t.Errorf("member %s carries nothing to read", mi.ID)
		}
	}
}

// for carries no imagesUnknown key at all (and `images` is omitempty, so a
// model that takes none carries no images key either), while one nothing was
// read of carries imagesUnknown: true. What the browser test's fixture writes
// is this shape, not one of its own.
func TestGroupModelImagesOnTheWire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	yes, no := true, false
	if err := provider.Save(provider.Provider{ID: "p", Name: "P", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"eye", "text", "mystery"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("p", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "eye", Images: true, ImageInput: &yes},
		{ID: "text", ImageInput: &no},
		{ID: "mystery"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"p/eye", "p/text", "p/mystery"}}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(groupsState())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []map[string]any `json:"models"`
		Groups []struct {
			Info []map[string]any `json:"memberInfo"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) (images, unknown any) { return m["images"], m["imagesUnknown"] }
	byID := map[string]map[string]any{}
	for _, m := range doc.Models {
		byID[m["id"].(string)] = m
	}
	for _, tc := range []struct {
		id      string
		images  bool
		unknown bool
	}{
		{"p/eye", true, false},
		{"p/text", false, false},
		{"p/mystery", false, true},
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s is not in the models", tc.id)
		}
		img, unk := keys(m)
		if img != nil && img != tc.images || (img == nil) == tc.images {
			t.Errorf("%s: images on the wire is %v, want %v (a false is left out)", tc.id, img, tc.images)
		}
		if unk != nil && unk != tc.unknown || (unk == nil) == tc.unknown {
			t.Errorf("%s: imagesUnknown on the wire is %v, want %v (only true is sent)", tc.id, unk, tc.unknown)
		}
	}
	// the same for a group's own members
	for _, m := range doc.Groups[0].Info {
		want := byID[m["id"].(string)]
		img, unk := keys(m)
		wantImg, wantUnk := keys(want)
		if (img == nil) != (wantImg == nil) || img != wantImg || (unk == nil) != (wantUnk == nil) || unk != wantUnk {
			t.Errorf("member %v carries images %v unknown %v, the model carries %v %v", m["id"], img, unk, wantImg, wantUnk)
		}
	}
}

// A group's member says it is free when Free first would count it so
// (provider.FreeModel): every model of a provider the user marked Free for
// me, and none of one they didn't (jiabubu on Discord)
func TestGroupMemberSaysFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "scope", Name: "Scope", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"qwen"}, CountFree: true},
		{ID: "paid", Name: "Paid", Key: "k", Chat: "http://127.0.0.1:2/v1", Models: []string{"qwen"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"scope/qwen", "paid/qwen"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupsState().Groups {
		if g.ID != "g" {
			continue
		}
		free := map[string]bool{}
		for _, m := range g.Info {
			if !m.Ready {
				t.Fatalf("%s is not served", m.ID)
			}
			free[m.ID] = m.Free
		}
		if !free["scope/qwen"] || free["paid/qwen"] {
			t.Fatalf("free: %v, want scope/qwen only", free)
		}
		return
	}
	t.Fatal("group g is not in the state")
}
