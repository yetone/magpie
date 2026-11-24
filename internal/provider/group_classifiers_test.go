package provider

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A group asks its classifiers in order (qinzhidaoa on X). One classifier
// is saved as it always was, with no classifierMore; several keep their
// order, each checked as the one is, and follow a rename or a removal.
func TestSaveGroupClassifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	intent := []Rule{{Use: "b/big", Intent: "planning"}}
	if err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: intent, Classifier: "a/m"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "classifierMore") || !strings.Contains(string(raw), `"classifier": "a/m"`) {
		t.Fatalf("one classifier saved as %s", raw)
	}
	g, _, _ := FindGroup(GroupPrefix + "r")
	if got := g.Classifiers(); !slices.Equal(got, []string{"a/m"}) {
		t.Fatalf("one: %q", got)
	}
	// several, a repeat and a blank among them
	g.SetClassifiers([]string{" magpie/b/m", "a/m", "b/m", ""})
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup(GroupPrefix + "r")
	if got := g.Classifiers(); !slices.Equal(got, []string{"b/m", "a/m"}) {
		t.Fatalf("several: %q", got)
	}
	// each is checked: a second that magpie knows nothing of is refused
	bad := g
	bad.SetClassifiers([]string{"b/m", "z/nothing"})
	if err := SaveGroup(bad); err == nil || !strings.Contains(err.Error(), "knows no model") {
		t.Fatalf("an unknown second: %v", err)
	}
	// a second that is a group: its rename is followed, and it can't be
	// removed while it classifies
	if err := SaveGroup(Group{Name: "Fast", Members: []string{"a/m", "b/m"}}); err != nil {
		t.Fatal(err)
	}
	g.SetClassifiers([]string{"b/m", "group/fast"})
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("fast", "quick"); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup(GroupPrefix + "r")
	if got := g.Classifiers(); !slices.Equal(got, []string{"b/m", "group/quick"}) {
		t.Fatalf("after the rename: %q", got)
	}
	if err := DeleteGroup("quick"); err == nil || !strings.Contains(err.Error(), "classifier") {
		t.Fatalf("removing a second classifier: %v", err)
	}
	// its own id among them is refused
	g.SetClassifiers([]string{"b/m", "group/r"})
	if err := SaveGroup(g); err == nil || !strings.Contains(err.Error(), "its own classifier") {
		t.Fatalf("itself second: %v", err)
	}
	// no intents left: no classifiers kept
	g, _, _ = FindGroup(GroupPrefix + "r")
	g.Rules = nil
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup(GroupPrefix + "r"); g.Classifier != "" || g.ClassifierMore != nil {
		t.Fatalf("kept %q %q", g.Classifier, g.ClassifierMore)
	}
}
