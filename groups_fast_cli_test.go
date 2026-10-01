package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A member typed with :fast after it (and its effort) is sent in its
// vendor's fast mode; fast= says which members are, models-= takes one
// out typed either way, and the group reads them back typed as they were.
func TestGroupMembersFast(t *testing.T) {
	groupsHome(t)
	if err := provider.Save(provider.Provider{ID: "oa", Name: "OpenAI", Key: "k", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	g, err := addGroup("Sol", []string{"models=gpt-6.1-sol:high:fast,a/m"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Members, []string{"oa/gpt-6.1-sol:high", "a/m"}) || !slices.Equal(g.Fast, []string{"oa/gpt-6.1-sol:high"}) {
		t.Fatalf("members %v fast %v", g.Members, g.Fast)
	}
	out, err := said(t, func() error { return showGroup(g) })
	if err != nil || !strings.Contains(out, "oa/gpt-6.1-sol:high:fast") || !strings.Contains(out, "· fast") {
		t.Fatalf("shown %v:\n%s", err, out)
	}
	if out, _ = said(t, groups); !strings.Contains(out, "oa/gpt-6.1-sol:high:fast") {
		t.Fatalf("listed:\n%s", out)
	}
	if g, err = setGroup("sol", []string{"fast="}); err != nil || len(g.Fast) != 0 {
		t.Fatalf("fast= %v %v", g.Fast, err)
	}
	if g, err = setGroup("sol", []string{"fast=oa/gpt-6.1-sol:high"}); err != nil || !g.IsFast("oa/gpt-6.1-sol:high") {
		t.Fatalf("fast=… %v %v", g.Fast, err)
	}
	if _, err = setGroup("sol", []string{"fast=a/m"}); err == nil || !strings.Contains(err.Error(), "no fast mode") {
		t.Fatalf("a/m on a relay sent fast: %v", err)
	}
	if g, err = setGroup("sol", []string{"models-=gpt-6.1-sol:high:fast"}); err != nil || !slices.Equal(g.Members, []string{"a/m"}) || len(g.Fast) != 0 {
		t.Fatalf("models-= %v %v %v", g.Members, g.Fast, err)
	}
}
