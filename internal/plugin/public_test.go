package plugin

import (
	"slices"
	"testing"
)

// What the day's report may say of the plugins installed: a package
// magpie's list names, a repository tagged magpie-plugin by its spec,
// and nothing else: not a folder, a private repository, a package no
// list names, nor one turned off.
func TestPublicPlugins(t *testing.T) {
	installed := []Entry{
		{Spec: "@magpie-community/opencode-cursor-auth@0.2.44"},
		{Spec: "opencode-third-party"},
		{Spec: "github:Alice/Tagged#main"},
		{Spec: "https://github.com/alice/tagged2.git"},
		{Spec: "bob/tagged3"},
		{Spec: "carol-npm-from-repo@1.0.0"},
		{Spec: "github:corp/private-plugin"},
		{Spec: "@corp/internal-plugin"},
		{Spec: "/Users/me/my-plugin"},
		{Spec: "./plugin.js"},
		{Spec: "opencode-turned-off", Off: true},
	}
	listed := map[string]bool{"@magpie-community/opencode-cursor-auth": true, "opencode-third-party": true, "opencode-turned-off": true}
	tagged := []Tagged{
		{Repo: "alice/tagged", Spec: "github:alice/tagged"},
		{Repo: "alice/tagged2", Spec: "github:alice/tagged2"},
		{Repo: "bob/tagged3", Spec: "github:bob/tagged3"},
		{Repo: "carol/npm-from-repo", Spec: "carol-npm-from-repo"},
	}
	got := public(installed, listed, tagged)
	want := []string{"@magpie-community/opencode-cursor-auth", "opencode-third-party", "github:alice/tagged", "github:alice/tagged2", "github:bob/tagged3", "carol-npm-from-repo"}
	if !slices.Equal(got, want) {
		t.Fatalf("public = %q\nwant      %q", got, want)
	}
}
