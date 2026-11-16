package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

func patternHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func routedIDs(t *testing.T, id string) []string {
	t.Helper()
	_, ms, ok := FindGroup(id)
	if !ok {
		t.Fatalf("no %s", id)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// A group's patterns (#766) find its members in the catalog each time it is
// read: the models they match follow the ones it names, in the catalog's
// order, a model named and matched both stays where it was named, and a
// model the vendor adds joins without the group being saved again.
func TestGroupPatterns(t *testing.T) {
	patternHome(t)
	if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a:free", "b", "c:free"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "zen", Name: "Zen", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x-free", "y"}}); err != nil {
		t.Fatal(err)
	}
	// typed among the members, as the CLI and the Routing view may send it
	if err := SaveGroup(Group{Name: "Free", Members: []string{"zen/y", "or/c:free", "or/*:free", "re:zen/.*-free"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("group/free")
	if !slices.Equal(g.Match, []string{"or/*:free", "re:zen/.*-free"}) {
		t.Fatalf("patterns: %v", g.Match)
	}
	want := []string{"zen/y", "or/c:free", "or/a:free", "zen/x-free"}
	if !slices.Equal(g.Members, want) || !slices.Equal(g.Matched, []string{"or/a:free", "zen/x-free"}) {
		t.Fatalf("members %v matched %v", g.Members, g.Matched)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, want) {
		t.Fatalf("routed: %v", got)
	}
	// what was matched is not written into the group
	b, _ := os.ReadFile(Path())
	var f struct{ Groups []Group }
	json.Unmarshal(b, &f)
	if len(f.Groups) != 1 || !slices.Equal(f.Groups[0].Members, []string{"zen/y", "or/c:free"}) || len(f.Groups[0].Matched) != 0 {
		t.Fatalf("stored: %+v", f.Groups)
	}
	// saved again as it was read, it stays so
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if s, _ := StoredGroups(); !slices.Equal(s[0].Members, []string{"zen/y", "or/c:free"}) {
		t.Fatalf("saved as read: %v", s[0].Members)
	}
	// renamed, too
	if err := RenameGroup("free", "pool"); err != nil {
		t.Fatal(err)
	}
	if s, _ := StoredGroups(); !slices.Equal(s[0].Members, []string{"zen/y", "or/c:free"}) || !slices.Equal(s[0].Match, g.Match) {
		t.Fatalf("renamed: %+v", s[0])
	}
	// the vendor lists another free model: the group has it
	if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a:free", "b", "c:free", "d:free"}}); err != nil {
		t.Fatal(err)
	}
	if got := routedIDs(t, "group/pool"); !slices.Equal(got, []string{"zen/y", "or/c:free", "or/a:free", "or/d:free", "zen/x-free"}) {
		t.Fatalf("after the catalog changed: %v", got)
	}
	if hits := PatternHits(g); len(hits) != 2 || hits[0].Models != 3 || hits[1].Models != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	// /v1/models has the group
	if !slices.ContainsFunc(Catalog(), func(e Entry) bool { return e.ID == "group/pool" }) {
		t.Fatal("not in the catalog")
	}
}

// The members a pattern matches are put in order by hand as a named one
// is (usrname1833 on Discord): the group is read, and routed, in the order
// the user put them in, a model matched since joins at the end in the
// catalog's order, one no longer matched drops out, and the order is kept
// through a save that sends the named members only, and a provider's
// rename.
func TestGroupPatternMembersKeepTheirPlace(t *testing.T) {
	patternHome(t)
	or := func(models ...string) {
		t.Helper()
		if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	or("a:free", "b", "c:free")
	if err := Save(Provider{ID: "zen", Name: "Zen", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "Free", Members: []string{"zen/y", "or/*:free"}}); err != nil {
		t.Fatal(err)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, []string{"zen/y", "or/a:free", "or/c:free"}) {
		t.Fatalf("unplaced: %v", got)
	}
	// dragged in the Routing page's editor, which sends the order:
	// a matched one first, the named one between
	g, _, _ := FindGroup("group/free")
	g.Placed = []string{"or/c:free", "zen/y", "or/a:free"}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	want := []string{"or/c:free", "zen/y", "or/a:free"}
	if g, _, _ := FindGroup("group/free"); !slices.Equal(g.Members, want) || !slices.Equal(g.Matched, []string{"or/a:free", "or/c:free"}) {
		t.Fatalf("read: %v matched %v", g.Members, g.Matched)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, want) {
		t.Fatalf("routed: %v", got)
	}
	if s, _ := StoredGroups(); !slices.Equal(s[0].Members, []string{"zen/y"}) {
		t.Fatalf("stored members: %v", s[0].Members)
	}
	// a save of the named ones only (the card's routing menu) keeps it
	if err := SaveGroup(Group{ID: "free", Name: "Free", Members: []string{"zen/y"}, Match: []string{"or/*:free"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, want) {
		t.Fatalf("after a named-only save: %v", got)
	}
	// the vendor drops one and adds another: gone, and joined at the end
	or("b", "c:free", "d:free")
	if got := routedIDs(t, "group/free"); !slices.Equal(got, []string{"or/c:free", "zen/y", "or/d:free"}) {
		t.Fatalf("after the catalog changed: %v", got)
	}
	// the provider renamed: its models keep their places
	if err := Rename("or", "op"); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "free", Name: "Free", Members: []string{"zen/y"}, Match: []string{"op/*:free"}}); err != nil {
		t.Fatal(err)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, []string{"op/c:free", "zen/y", "op/d:free"}) {
		t.Fatalf("after a rename: %v", got)
	}
	// a model added to the group as read (models+=, the TUI) goes after
	// those named, the pattern's staying where they were
	if err := Save(Provider{ID: "zen", Name: "Zen", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"y", "z"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup("group/free")
	g.Members = append(g.Members, "zen/z")
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := routedIDs(t, "group/free"); !slices.Equal(got, []string{"op/c:free", "zen/y", "zen/z", "op/d:free"}) {
		t.Fatalf("after an add: %v", got)
	}
	// put back in the order it has unasked, nothing is kept
	g, _, _ = FindGroup("group/free")
	g.Placed = []string{"zen/y", "zen/z", "op/c:free", "op/d:free"}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(Path()); strings.Contains(string(b), `"placed"`) {
		t.Fatalf("placed kept: %s", b)
	}
}

// A matched member may be switched off, picked, or ruled, like a named one;
// and named at an effort of its own it is not matched again.
func TestGroupPatternMembersOffAndRules(t *testing.T) {
	patternHome(t)
	if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a:free", "b:free", "c"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "P", Match: []string{"or/*:free"}, Off: []string{"or/a:free"}, Routing: Ordered,
		Rules: []Rule{{Use: "or/b:free", Tokens: 10}}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("group/p")
	if !slices.Equal(g.Off, []string{"or/a:free"}) || len(g.Rules) != 1 {
		t.Fatalf("saved: %+v", g)
	}
	if got := routedIDs(t, "group/p"); !slices.Equal(got, []string{"or/b:free"}) {
		t.Fatalf("routed: %v", got)
	}
	// every matched one off is every one off
	g.Off = g.Members
	if err := SaveGroup(g); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("all off: %v", err)
	}
	// a matched one given an effort of its own is named, and not matched again
	g, _, _ = FindGroup("group/p")
	g.RenameMember("or/b:free", "or/b:free:high")
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup("group/p")
	if !slices.Equal(g.Members, []string{"or/b:free:high", "or/a:free"}) || !slices.Equal(g.Matched, []string{"or/a:free"}) {
		t.Fatalf("named at an effort: %v matched %v", g.Members, g.Matched)
	}
}

// A pattern that matches nothing leaves the group there, empty but said so;
// a bad one is refused, and a group needs a model or a pattern.
func TestGroupPatternMatchingNothing(t *testing.T) {
	patternHome(t)
	if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "None", Match: []string{"or/*:free"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/none")
	if !ok || len(ms) != 0 || len(g.Members) != 0 {
		t.Fatalf("found %v: %+v %v", ok, g, ms)
	}
	if hits := PatternHits(g); len(hits) != 1 || hits[0].Models != 0 {
		t.Fatalf("hits: %+v", hits)
	}
	if err := SaveGroup(Group{Name: "Bad", Match: []string{"re:or/(a"}}); err == nil {
		t.Fatal("a broken regexp was taken")
	}
	if err := SaveGroup(Group{Name: "Empty"}); err == nil {
		t.Fatal("a group of nothing was taken")
	}
	// a pattern never matches a group, nor the group itself
	if err := SaveGroup(Group{Name: "All", Match: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("group/all"); !slices.Equal(g.Members, []string{"or/a"}) {
		t.Fatalf("all: %v", g.Members)
	}
}

func TestPatternSyntax(t *testing.T) {
	for _, c := range []struct {
		p, id string
		ok    bool
	}{
		{"openrouter/*:free", "openrouter/meta/llama-4:free", true},
		{"openrouter/*:free", "openrouter/meta/llama-4", false},
		{"*-free", "opencode-zen/grok-code-free", true},
		{"opencode-zen/*", "opencode-zen/x", true},
		{"opencode-zen/*", "opencode/x", false},
		{"OpenRouter/*", "openrouter/x", true},
		{"re:openrouter/.*:(free|beta)", "openrouter/x:beta", true},
		{"re:openrouter/x", "openrouter/xy", false},
		{"a.b*", "axb-1", false},
	} {
		if got := PatternMatches(c.p, c.id); got != c.ok {
			t.Errorf("%s ~ %s: %v", c.p, c.id, got)
		}
	}
	if IsPattern("openrouter/x:free") || !IsPattern("re:x") || !IsPattern("x*") {
		t.Error("IsPattern")
	}
}

// A re: pattern matches in any case as a glob does, for a group and for a
// provider's auto-include rules alike (both compile through
// compilePattern), and (?-i) inside it makes case matter again.
func TestRegexpPatternIgnoresCase(t *testing.T) {
	for _, c := range []struct {
		p, id string
		ok    bool
	}{
		{"re:openrouter/.*:(free|beta)", "OpenRouter/Meta/Llama-4:FREE", true},
		{"re:GPT-.*", "gpt-5.5", true},
		{"re:gpt-.*", "GPT-5.5", true},
		{"re:(?-i)gpt-.*", "GPT-5.5", false},
		{"re:(?-i)gpt-.*", "gpt-5.5", true},
		{"re:openrouter/(?-i)x", "OPENROUTER/x", true},
		{"re:openrouter/(?-i)x", "openrouter/X", false},
	} {
		if got := PatternMatches(c.p, c.id); got != c.ok {
			t.Errorf("%s ~ %s: %v, want %v", c.p, c.id, got, c.ok)
		}
	}
}

// A re: pattern Go's regexp can't read, a lookahead (maer8824 on Discord:
// glm but not flash), is read as the GUI's preview reads it, so the group
// saves and matches what the preview showed. One Go reads keeps Go's
// reading, and one neither reads is still refused.
func TestRegexpPatternLookahead(t *testing.T) {
	for _, c := range []struct {
		p, id string
		ok    bool
	}{
		{"re:(?!.*flash).*glm.*", "zhipu/glm-4.6", true},
		{"re:(?!.*flash).*glm.*", "zhipu/GLM-4.5-Flash", false},
		{"re:.*glm(?=-4)", "zhipu/glm-4", false},
		{"re:(?-i)(?!.*Flash).*glm.*", "z/glm-flash", true},
		{"re:.*(?<!-air)", "z/glm-4.5-air", false},
		{"re:.*(?<!-air)", "z/glm-4.5", true},
	} {
		if got := PatternMatches(c.p, c.id); got != c.ok {
			t.Errorf("%s ~ %s: %v, want %v", c.p, c.id, got, c.ok)
		}
	}
	if _, err := CleanPatterns([]string{"re:(?!.*flash).*glm.*"}); err != nil {
		t.Errorf("lookahead refused: %v", err)
	}
	if _, err := CleanPatterns([]string{"re:(unclosed"}); err == nil {
		t.Error("an unreadable pattern was kept")
	}
	if _, err := CleanAutoInclude([]string{"re:(?!.*flash).*glm.*"}); err != nil {
		t.Errorf("auto-include lookahead refused: %v", err)
	}
}

// freePlugins are WorkBuddy's and Cline's plugins signed in, their models
// as each lists them (#5903): WorkBuddy's hy4free, hy3 and hy4flash-exp4
// marked free (its "credits": "x0.00"), glm-5.1 at a rate, one with "free"
// in its id that isn't marked so, and one at a rate of 0 not marked free.
func freePlugins(t *testing.T, wb []plugin.Model) {
	t.Helper()
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wbSpec, clSpec := "@magpie-community/opencode-workbuddy-auth", "@magpie-community/opencode-cline-auth"
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": wbSpec}, {"spec": clSpec}}})
	write("plugin-auth.json", map[string]map[string]any{"workbuddy": {"type": "api", "key": "wb-1234"}, "cline": {"type": "api", "key": "sk-cline-1234"}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	listWorkBuddy(wb)
}

// listWorkBuddy is the plugins' catalog as read again, WorkBuddy listing wb.
func listWorkBuddy(wb []plugin.Model) {
	wbSpec, clSpec := "@magpie-community/opencode-workbuddy-auth", "@magpie-community/opencode-cline-auth"
	plugin.UseCached([]plugin.Provider{
		{ID: "workbuddy", Spec: wbSpec, Name: "WorkBuddy", Methods: []plugin.Method{{Type: "api", Label: "API key"}}, Accounts: []plugin.Account{{Key: "workbuddy", Type: "api"}}, Models: wb},
		{ID: "cline", Spec: clSpec, Name: "Cline", Methods: []plugin.Method{{Type: "api", Label: "API key"}}, Accounts: []plugin.Account{{Key: "cline", Type: "api"}},
			Models: []plugin.Model{{ID: "cline-free/mimo-v2.6-flash", Name: "MiMo V2.6 Flash (free)", Free: true}}},
	})
}

// A group's free: rule (#5903) takes, of the models its scope matches,
// only those their catalog marks free: within the provider it names, as
// the catalog was last read. A model at $0, at a rate of 0 or with free in
// its id is not taken, nor one of another provider; the catalog marking
// another free, or one free no longer, changes the group with no save, and
// the patterns that were there before match as they did.
func TestGroupFreeRule(t *testing.T) {
	wb := []plugin.Model{
		{ID: "hy4free", Name: "Hy4 Free", Free: true},
		{ID: "glm-5.1", Name: "GLM 5.1", Rate: 1},
		{ID: "hy3", Name: "Hy3", Free: true},
		{ID: "deepseek-v4-free", Name: "DeepSeek V4 Free"},
		{ID: "hy-rate0", Name: "Hy Rate 0", Rate: 0},
		{ID: "hy4flash-exp4", Name: "Hy4 Flash Exp4", Free: true},
	}
	freePlugins(t, wb)
	if err := Save(Provider{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x:free", "y"}}); err != nil {
		t.Fatal(err)
	}
	// a model the user priced at $0 is not one its catalog marks free
	if err := SetModelPrice("or/y", &catalog.Price{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("or"); p == nil || !p.PricedFree(settings.Load(), "y") {
		t.Fatal("or/y is not priced free: the fixture tests nothing")
	}
	for _, g := range []Group{
		{Name: "WB Free", Members: []string{"free:workbuddy/*"}},
		{Name: "All Free", Match: []string{"free:*"}},
		{Name: "WB", Match: []string{"workbuddy/*"}},
		{Name: "OR Free", Match: []string{"or/*:free"}},
	} {
		if err := SaveGroup(g); err != nil {
			t.Fatalf("%s: %v", g.Name, err)
		}
	}
	wbFree := []string{"workbuddy/hy4free", "workbuddy/hy3", "workbuddy/hy4flash-exp4"}
	g, _, _ := FindGroup("group/wb-free")
	if !slices.Equal(g.Match, []string{"free:workbuddy/*"}) || !slices.Equal(g.Members, wbFree) {
		t.Fatalf("free:workbuddy/*: match %v members %v (served %v)", g.Match, g.Members, IDs())
	}
	if got := routedIDs(t, "group/wb-free"); !slices.Equal(got, wbFree) {
		t.Fatalf("routed: %v", got)
	}
	if hits := PatternHits(g); len(hits) != 1 || hits[0].Models != 3 {
		t.Fatalf("hits: %+v", hits)
	}
	if got := routedIDs(t, "group/all-free"); !slices.Equal(got, append(slices.Clone(wbFree), "cline/cline-free/mimo-v2.6-flash")) {
		t.Fatalf("free:*: %v", got)
	}
	// the rules there before are as they were
	if got := routedIDs(t, "group/wb"); len(got) != len(wb) {
		t.Fatalf("workbuddy/*: %v", got)
	}
	if got := routedIDs(t, "group/or-free"); !slices.Equal(got, []string{"or/x:free"}) {
		t.Fatalf("or/*:free: %v", got)
	}
	// the next catalog: hy3 free no longer, hy5 new and free, hy4free gone
	listWorkBuddy([]plugin.Model{
		{ID: "hy3", Name: "Hy3", Rate: 0.5},
		{ID: "hy4flash-exp4", Name: "Hy4 Flash Exp4", Free: true},
		{ID: "hy5", Name: "Hy5", Free: true},
	})
	if got := routedIDs(t, "group/wb-free"); !slices.Equal(got, []string{"workbuddy/hy4flash-exp4", "workbuddy/hy5"}) {
		t.Fatalf("after the catalog changed: %v", got)
	}
}

// A free: rule is said as a pattern and kept as typed, magpie/ and spaces
// trimmed; one naming nothing, or free: twice, is refused. A provider's
// AutoInclude, over ids alone, reads free: as it did before.
func TestFreeRuleSyntax(t *testing.T) {
	if !IsPattern("free:workbuddy/hy3") || !IsPattern("free:re:workbuddy/hy.*") {
		t.Error("IsPattern")
	}
	got, err := CleanPatterns([]string{" free: magpie/workbuddy/* ", "free:re:workbuddy/hy.*", "free:workbuddy/*"})
	if err != nil || !slices.Equal(got, []string{"free:workbuddy/*", "free:re:workbuddy/hy.*"}) {
		t.Fatalf("clean: %v %v", got, err)
	}
	for _, p := range []string{"free:", "free: ", "free:free:x/*", "free:re:x/(a"} {
		if _, err := CleanPatterns([]string{p}); err == nil {
			t.Errorf("%q taken", p)
		}
	}
	if !PatternMatches("free:workbuddy/*", "workbuddy/hy3") || PatternMatches("free:workbuddy/*", "cline/hy3") {
		t.Error("PatternMatches over a free rule's scope")
	}
	if _, err := CleanAutoInclude([]string{"free:x"}); err == nil {
		t.Error("an auto-include rule over ids took free:x")
	}
}
