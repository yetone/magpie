package provider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// groupMembers are the members of the group id as Groups reads it, nil
// when there is none (or it is hidden).
func groupMembers(t *testing.T, id string) []string {
	t.Helper()
	for _, g := range Groups() {
		if g.ID == id && !g.Hidden {
			return g.Members
		}
	}
	return nil
}

func saveKeyProvider(t *testing.T, id string, models ...string) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
}

// A found group the user changed follows its model as one magpie finds
// does (Kxsg79bEYFP4 on X: auto/deepseek-v4.1-flash, changed, stayed at
// the two providers it was found with when a third with the model was
// added): a provider that serves the model since joins it, after the
// members the user named. A model the user took out stays out, the one
// taken out before the new provider came and the new one taken out
// after it joined alike; a provider kept out of found groups doesn't
// join; and a provider's rename carries what was seen, so the renamed
// provider's model the user took out doesn't come back by its new id.
func TestChangedFoundGroupTakesInANewProvider(t *testing.T) {
	claudeHome(t)
	saveKeyProvider(t, "a", "deepseek-v4.1-flash")
	saveKeyProvider(t, "b", "deepseek-v4-1-flash")
	saveKeyProvider(t, "c", "deepseek-v4.1-flash")
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, []string{"a/deepseek-v4.1-flash", "b/deepseek-v4-1-flash", "c/deepseek-v4.1-flash"}) {
		t.Fatalf("found: %v", got)
	}
	// the user changes it: routing by turns, without c
	if err := SaveGroup(Group{ID: "auto-deepseek-v4-1-flash", Name: "DS Flash", Members: []string{"b/deepseek-v4-1-flash", "a/deepseek-v4.1-flash:high"}, Routing: Rotate}); err != nil {
		t.Fatal(err)
	}
	want := []string{"b/deepseek-v4-1-flash", "a/deepseek-v4.1-flash:high"}
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, want) {
		t.Fatalf("changed: %v", got)
	}
	// a fourth provider with the model joins it, last; c stays out
	saveKeyProvider(t, "d", "DeepSeek-V4.1-Flash")
	want = append(want, "d/DeepSeek-V4.1-Flash")
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, want) {
		t.Fatalf("d added: %v, want %v", got, want)
	}
	// the group the gateway routes has it too
	if _, ms, ok := FindGroup("group/auto-deepseek-v4-1-flash"); !ok || !slices.ContainsFunc(ms, func(m Member) bool { return m.Provider.ID == "d" }) {
		t.Fatalf("routed without d: %v %v", ms, ok)
	}
	// saved as read, it is named; the user's name and routing stay
	gs := Groups()
	g := gs[slices.IndexFunc(gs, func(g Group) bool { return g.ID == "auto-deepseek-v4-1-flash" })]
	if g.Name != "DS Flash" || g.Routing != Rotate {
		t.Fatalf("the user's settings: %q %q", g.Name, g.Routing)
	}
	// the user takes d out: it stays out, as c does
	g.Members = slices.DeleteFunc(slices.Clone(g.Members), func(m string) bool { return strings.HasPrefix(m, "d/") })
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	want = []string{"b/deepseek-v4-1-flash", "a/deepseek-v4.1-flash:high"}
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, want) {
		t.Fatalf("d taken out: %v", got)
	}
	// renamed, c is still one the user took out
	if err := Rename("c", "cee"); err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, want) {
		t.Fatalf("c renamed came back: %v", got)
	}
	// one kept out of found groups doesn't join; let back in, it does
	saveKeyProvider(t, "e", "deepseek-v4.1-flash")
	if err := SetAutoGroupSkip([]string{"e"}); err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, want) {
		t.Fatalf("e kept out joined: %v", got)
	}
	if err := SetAutoGroupSkip(nil); err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, "auto-deepseek-v4-1-flash"); !slices.Equal(got, append(slices.Clone(want), "e/deepseek-v4.1-flash")) {
		t.Fatalf("e let back in: %v", got)
	}
	// what this magpie saw is its own: a shared group doesn't carry it
	text, err := ShareGroup("auto-deepseek-v4-1-flash")
	if err != nil {
		t.Fatal(err)
	}
	var shared SharedGroup
	if err := json.Unmarshal([]byte(text), &shared); err != nil || len(shared.Group.Seen) != 0 {
		t.Fatalf("shared with what was seen: %v %v", shared.Group.Seen, err)
	}
}

// A found group changed before magpie kept what it had seen has no
// record of which of its models the user took out: none joins it, as
// before, so one taken out by hand never comes back. Its next save
// sees what is found then, and from that on a new provider joins.
func TestChangedFoundGroupFromBeforeKeepsItsMembers(t *testing.T) {
	claudeHome(t)
	saveKeyProvider(t, "a", "glm-5.3")
	saveKeyProvider(t, "b", "glm-5.3")
	saveKeyProvider(t, "c", "glm-5.3")
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	// as an older magpie stored it: c taken out, nothing seen
	f.Groups = append(f.Groups, Group{ID: "auto-glm-5-3", Name: "GLM", Members: []string{"a/glm-5.3", "b/glm-5.3"}, Routing: Ordered})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	want := []string{"a/glm-5.3", "b/glm-5.3"}
	if got := groupMembers(t, "auto-glm-5-3"); !slices.Equal(got, want) {
		t.Fatalf("an older change: %v", got)
	}
	saveKeyProvider(t, "d", "glm-5.3")
	if got := groupMembers(t, "auto-glm-5-3"); !slices.Equal(got, want) {
		t.Fatalf("an older change took a model in: %v", got)
	}
	gs := Groups()
	if err := SaveGroup(gs[slices.IndexFunc(gs, func(g Group) bool { return g.ID == "auto-glm-5-3" })]); err != nil {
		t.Fatal(err)
	}
	if got := groupMembers(t, "auto-glm-5-3"); !slices.Equal(got, want) {
		t.Fatalf("saved again: %v", got)
	}
	saveKeyProvider(t, "e", "glm-5.3")
	if got := groupMembers(t, "auto-glm-5-3"); !slices.Equal(got, append(want, "e/glm-5.3")) {
		t.Fatalf("a provider after the save: %v", got)
	}
}

// The reporter's case on its own surface: a provider a plugin serves,
// added after the user changed a found group of two key providers,
// joins it.
func TestChangedFoundGroupTakesInAPluginProvider(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_DEEPSEEK", "1")
	t.Cleanup(plugin.Settle)
	saveKeyProvider(t, "a", "deepseek-v4-pro")
	saveKeyProvider(t, "b", "deepseek-v4-pro")
	if err := SaveGroup(Group{ID: "auto-deepseek-v4-pro", Name: "DS Pro", Members: []string{"b/deepseek-v4-pro", "a/deepseek-v4-pro"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	got := groupMembers(t, "auto-deepseek-v4-pro")
	if len(got) != 3 || !slices.Equal(got[:2], []string{"b/deepseek-v4-pro", "a/deepseek-v4-pro"}) || !strings.HasSuffix(got[2], "/deepseek-v4-pro") || strings.HasPrefix(got[2], "a/") || strings.HasPrefix(got[2], "b/") {
		t.Fatalf("the plugin's provider didn't join: %v", got)
	}
}
