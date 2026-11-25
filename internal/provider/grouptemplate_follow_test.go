package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// deepseekHome is a sandbox HOME with models.dev's DeepSeek as the catalog
// magpie ships (2026-11-10), and the DeepSeek preset serving models.
func deepseekHome(t *testing.T, models ...string) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	b, err := os.ReadFile(filepath.Join("testdata", "models-dev-deepseek-2026-11-10.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	serveDeepseek(t, models...)
}

func serveDeepseek(t *testing.T, models ...string) {
	t.Helper()
	if err := Save(Provider{ID: "deepseek", Name: "DeepSeek", Key: "sk-test", Catalog: "deepseek",
		Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
		Models: models}); err != nil {
		t.Fatal(err)
	}
}

func groupByID(t *testing.T, id string) Group {
	t.Helper()
	for _, g := range Groups() {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no group %s", id)
	return Group{}
}

func storedGroup(t *testing.T, id string) Group {
	t.Helper()
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range f.Groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no stored group %s", id)
	return Group{}
}

// A group added from a template keeps its models picked from the user's
// own as they change (AlanWang1027713 on X: "让 AI 配，不用人工配"): a
// model retired and another served in its place is picked up without the
// editor. A save that changes a pick makes the picks the user's, never
// picked over again; a rename keeps it following.
func TestTemplateGroupFollowsTheModels(t *testing.T) {
	deepseekHome(t, "deepseek-v4-pro", "deepseek-v4-flash")
	g, err := AddTemplate(TemplateSmart, "")
	if err != nil {
		t.Fatal(err)
	}
	if g.Template != TemplateSmart || storedGroup(t, g.ID).Template != TemplateSmart {
		t.Fatalf("added %+v: want it following %s", g, TemplateSmart)
	}
	if want := []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash"}; !slices.Equal(g.Members, want) {
		t.Fatalf("members %v, want %v", g.Members, want)
	}

	// a second one the user changes: the easy turns to the strong model
	mine, err := AddTemplate(TemplateSmart, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	mine.Template = "" // as the Routing page's save of a group sends it
	for i, r := range mine.Rules {
		if r.Intent == IntentEasy {
			mine.Rules[i].Use = "deepseek/deepseek-v4-pro"
		}
	}
	if err := SaveGroup(mine); err != nil {
		t.Fatal(err)
	}
	if got := groupByID(t, mine.ID).Template; got != "" {
		t.Fatalf("a rule changed by the user: template %q, want the picks the user's", got)
	}

	// renamed, the picks as they were: still following
	g.Name, g.Note, g.Template = "Smart", "work", ""
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := groupByID(t, g.ID); got.Template != TemplateSmart || got.Name != "Smart" {
		t.Fatalf("renamed: %+v, want it following still", got)
	}

	// DeepSeek retires V4 Flash for deepseek-flash
	serveDeepseek(t, "deepseek-v4-pro", "deepseek-flash")
	got := groupByID(t, g.ID)
	if want := []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-flash"}; !slices.Equal(got.Members, want) {
		t.Errorf("following: members %v, want %v", got.Members, want)
	}
	for _, r := range got.Rules {
		if (r.Intent == IntentEasy || r.Compact) && r.Use != "deepseek/deepseek-flash" {
			t.Errorf("following: rule %+v, want it on the cheap model now", r)
		}
	}
	if got.Classifier != "deepseek/deepseek-flash" {
		t.Errorf("following: classifier %q, want the cheap model now", got.Classifier)
	}
	// the user's own picks stay as they set them
	if m := groupByID(t, mine.ID); !slices.Contains(m.Members, "deepseek/deepseek-v4-flash") {
		t.Errorf("the user's group: members %v, want theirs kept", m.Members)
	}
	// saved as read now, it still follows
	if err := SaveGroup(got); err != nil {
		t.Fatal(err)
	}
	if storedGroup(t, g.ID).Template != TemplateSmart {
		t.Error("saved as read: want it following still")
	}

	// nothing cheap left: the template can't be made, and the group keeps
	// the picks it had rather than go empty
	serveDeepseek(t, "deepseek-v4-pro")
	if m := groupByID(t, g.ID); len(m.Members) == 0 || m.Template != TemplateSmart {
		t.Errorf("unmakeable: %+v, want the picks it had", m)
	}
}

// The editor's Edit first saves a template's draft as a new group, its
// kind sent with it: unchanged it follows, a pick changed in the editor
// makes it the user's, and a kind magpie has no template of is none.
func TestTemplateDraftSavedFollows(t *testing.T) {
	deepseekHome(t, "deepseek-v4-pro", "deepseek-v4-flash")
	tp, err := MakeTemplate(TemplateThrifty)
	if err != nil {
		t.Fatal(err)
	}
	draft := tp.Group
	draft.ID, draft.Template = "cheap", TemplateThrifty
	if err := SaveGroup(draft); err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, "cheap").Template; got != TemplateThrifty {
		t.Errorf("draft saved as made: template %q, want %q", got, TemplateThrifty)
	}
	changed := tp.Group
	changed.ID, changed.Template = "cheap-mine", TemplateThrifty
	changed.Members = slices.Clone(changed.Members)
	slices.Reverse(changed.Members)
	if err := SaveGroup(changed); err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, "cheap-mine").Template; got != "" {
		t.Errorf("draft with its order changed: template %q, want none", got)
	}
	odd := tp.Group
	odd.ID, odd.Template = "odd", "no-such"
	if err := SaveGroup(odd); err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, "odd").Template; got != "" {
		t.Errorf("unknown kind: template %q, want none", got)
	}
}

// A group shared as text carries its picks as they are, not the template:
// the picks are this magpie's models.
func TestSharedTemplateGroupCarriesNoTemplate(t *testing.T) {
	deepseekHome(t, "deepseek-v4-pro", "deepseek-v4-flash")
	g, err := AddTemplate(TemplateSmart, "")
	if err != nil {
		t.Fatal(err)
	}
	text, err := ShareGroup(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	res, err := ImportGroup(text)
	if err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, res.ID).Template; got != "" {
		t.Errorf("imported: template %q, want none", got)
	}
}
