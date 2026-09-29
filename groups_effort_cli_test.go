package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A member typed with an effort (#189) is the model resolved as any other,
// the effort after it; the same model at two efforts is two members, and
// a rule's use= names one of them.
func TestGroupMembersWithEfforts(t *testing.T) {
	groupsHome(t)
	g, err := addGroup("Fast", []string{"models=a/m:low,gpt-5.5:HIGH,a/only-a"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/m:low", "b/gpt-5.5:high", "a/only-a"}; !slices.Equal(g.Members, want) {
		t.Fatalf("members %v, want %v", g.Members, want)
	}
	if g, err = setGroup("fast", []string{"models+=a/m:xhigh"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/m:low", "b/gpt-5.5:high", "a/only-a", "a/m:xhigh"}; !slices.Equal(g.Members, want) {
		t.Fatalf("members %v, want %v", g.Members, want)
	}
	if err := ruleCmd([]string{"add", "fast", "use=a/m:xhigh", "tokens=100k"}); err != nil {
		t.Fatal(err)
	}
	if g, err = findGroup("fast"); err != nil || len(g.Rules) != 1 || g.Rules[0].Use != "a/m:xhigh" {
		t.Fatalf("rule: %+v %v", g.Rules, err)
	}
	// a/m alone is two members: name one
	if err := ruleCmd([]string{"add", "fast", "use=a/m", "tokens=100k"}); err == nil || !strings.Contains(err.Error(), "name one") {
		t.Fatalf("ambiguous use: %v", err)
	}
	if g, err = setGroup("fast", []string{"models-=a/m:low"}); err != nil || slices.Contains(g.Members, "a/m:low") || !slices.Contains(g.Members, "a/m:xhigh") {
		t.Fatalf("drop one effort: %v %v", g.Members, err)
	}
	for _, bad := range []string{"a/nope:low", "group/fast:low"} {
		if _, err := addGroup("Bad", []string{"models=a/m," + bad}); err == nil {
			t.Errorf("%s: added", bad)
		}
	}
	names := catalogByID()
	if s, ok := memberLabel("a/m:low", names); !ok || !strings.Contains(s, "low reasoning") {
		t.Errorf("label %q %v", s, ok)
	}
	if _, _, ok := provider.FindGroup("group/fast"); !ok {
		t.Fatal("group gone")
	}
}
