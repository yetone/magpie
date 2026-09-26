package provider

import (
	"strings"
	"testing"
)

// A decision provider (TypeSafe's Jev) is only ever a group's classifier:
// its models aren't in the catalog nor a group's members, and a group's
// effort is picked only by it.
func TestDecider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	ts, err := FromPreset("typesafe")
	if err != nil || !ts.Decides() {
		t.Fatalf("preset %+v %v", ts, err)
	}
	ts.Key = "kts"
	if err := Save(ts); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("typesafe"); err != nil || p.Host() != "api.typesafe.ai" {
		t.Fatalf("saved %+v %v", p, err)
	}
	for _, e := range Catalog() {
		if e.Provider.ID == "typesafe" {
			t.Fatalf("Jev in the catalog: %+v", e)
		}
	}
	if ds := Deciders(); len(ds) == 0 || ds[0].ID != "typesafe/jev-latest" || !IsDecider("typesafe/jev-latest") || IsDecider("a/m") {
		t.Fatalf("deciders %+v", ds)
	}
	if p, _, ok := Resolve("jev-latest"); ok && p.Decides() {
		t.Fatal("a bare jev-latest resolves to the decider")
	}
	g := Group{Name: "G", Members: []string{"a/m"}}
	for _, tc := range []struct {
		effort, classifier, err string
	}{
		{"auto", "", "needs the group's classifier"},
		{"auto", "a/m", "Jev"},
		{"high", "typesafe/jev-latest", "not \"high\""},
		{"auto", "typesafe/jev-latest", ""},
	} {
		g.Effort, g.Classifier = tc.effort, tc.classifier
		err := SaveGroup(g)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s by %q: %v, want %q", tc.effort, tc.classifier, err, tc.err)
		}
	}
	if g, _, _ := FindGroup("group/g"); g.Effort != EffortAuto || g.Classifier != "typesafe/jev-latest" || !g.Ruled() {
		t.Fatalf("saved %+v", g)
	}
	if err := SaveGroup(Group{Name: "H", Members: []string{"typesafe/jev-latest"}}); err == nil {
		t.Error("Jev saved as a group's member")
	}
}

// TypeSafe's errors are FastAPI's: {"detail":{"message":…}}.
func TestAPIErrorDetail(t *testing.T) {
	b := []byte(`{"detail":{"error_type":"authentication_error","message":"Must supply an API key! Check your request and try again."}}`)
	if got := APIError(b, "403 Forbidden"); got != "Must supply an API key! Check your request and try again." {
		t.Fatal(got)
	}
}
