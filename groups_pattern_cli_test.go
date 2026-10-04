package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// models= takes patterns among the ids (#766): the group keeps the
// patterns, not what they match, and models+= / models-= add and drop them.
func TestGroupPatternsCLI(t *testing.T) {
	groupsHome(t)
	g, err := addGroup("Pool", []string{"models=b/gpt-5.5,a/*"})
	if err != nil {
		t.Fatal(err)
	}
	// named first, then matched in the catalog's order
	if !slices.Equal(g.Members, []string{"b/gpt-5.5", "a/m", "a/only-a", "a/claude-opus-5-5"}) || !slices.Equal(g.Match, []string{"a/*"}) {
		t.Fatalf("added: %v %v", g.Members, g.Match)
	}
	gs := storedGroups(t)
	if fmt.Sprint(gs[0]["members"]) != "[b/gpt-5.5]" || fmt.Sprint(gs[0]["match"]) != "[a/*]" {
		t.Fatalf("stored: %v", gs)
	}
	// a matched model can't be dropped by id: the pattern would bring it back
	if _, err := setGroup("pool", []string{"models-=a/m"}); err == nil || !strings.Contains(err.Error(), "a/*") {
		t.Fatalf("dropped a matched model: %v", err)
	}
	// named, it moves to where it is named
	if g, err = setGroup("pool", []string{"models+=a/only-a,re:b/vendor/.*"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Members, []string{"b/gpt-5.5", "a/only-a", "a/m", "a/claude-opus-5-5", "b/vendor/m"}) || len(g.Match) != 2 {
		t.Fatalf("models+=: %v %v", g.Members, g.Match)
	}
	if g, err = setGroup("pool", []string{"models-=a/*"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Members, []string{"b/gpt-5.5", "a/only-a", "b/vendor/m"}) || !slices.Equal(g.Match, []string{"re:b/vendor/.*"}) {
		t.Fatalf("models-= a pattern: %v %v", g.Members, g.Match)
	}
	// a pattern alone, matching nothing: the group stays, said so
	if g, err = setGroup("pool", []string{"models=zzz/*"}); err != nil {
		t.Fatal(err)
	}
	if len(g.Members) != 0 || !slices.Equal(g.Match, []string{"zzz/*"}) {
		t.Fatalf("models=zzz/*: %+v", g)
	}
	if _, ms, ok := provider.FindGroup("group/pool"); !ok || len(ms) != 0 {
		t.Fatalf("routed: %v %+v", ok, ms)
	}
	if err := showGroup(g); err != nil {
		t.Fatal(err)
	}
	if _, err := addGroup("Bad", []string{"models=re:a/(m"}); err == nil {
		t.Fatal("a broken regexp was taken")
	}
	if !strings.Contains(groupUsage, "re:<regexp>") {
		t.Fatal("help says nothing of patterns")
	}
}
