package provider

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
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
