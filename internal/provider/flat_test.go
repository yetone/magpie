package provider

import "testing"

// #1387: a flat id is taken back only to the one id that flattens to it;
// two that flatten alike are neither, and an id with a slash, or with no
// tilde, is never flat.
func TestUnflat(t *testing.T) {
	es := []Entry{{ID: "bb-codex/gpt-6.1-sol"}, {ID: "openrouter/~anthropic/claude-opus-latest"}, {ID: "group/auto-gpt-6-1-sol"},
		{ID: "p/a/b"}, {ID: "p/a~b"}}
	for in, want := range map[string]string{
		"bb-codex~gpt-6.1-sol":                     "bb-codex/gpt-6.1-sol",
		"openrouter~~anthropic~claude-opus-latest": "openrouter/~anthropic/claude-opus-latest",
		"group~auto-gpt-6-1-sol":                   "group/auto-gpt-6-1-sol",
		"p~a~b":                                    "",
		"gpt-6.1-sol":                              "",
	} {
		if got, ok := unflatIn(es, in); got != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
	if FlatID("bb-codex/gpt-6.1-sol") != "bb-codex~gpt-6.1-sol" {
		t.Fatal(FlatID("bb-codex/gpt-6.1-sol"))
	}
	if _, ok := Unflat("bb-codex/gpt~6"); ok {
		t.Fatal("an id with a slash was taken as flat")
	}
}
