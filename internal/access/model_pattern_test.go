package access

import (
	"slices"
	"testing"
)

func TestModelPatternsAreRegularExpressions(t *testing.T) {
	for _, c := range []struct {
		pat, id string
		want    bool
	}{
		{"re:^gpt-", "openai/gpt-5", true},
		{"re:^openai/gpt-", "openai/gpt-5", true},
		{"re:^GPT-5$", "openai/gpt-5", true},
		{"re:^gpt-", "openai/o3", false},
		{"re:gpt", "group/gpt-fans", false}, // never a routing group
		{"RE:^gpt-", "openai/gpt-5", true},
		{"re:(", "openai/gpt-5", false}, // a bad one names nothing, never all
	} {
		if got := ModelAllowed([]string{c.pat}, c.id); got != c.want {
			t.Errorf("%s on %s: %v", c.pat, c.id, got)
		}
	}
}

func TestCleanModelsRefusesABadPattern(t *testing.T) {
	for _, bad := range []string{"re:(", "re:", "re:[a-", "re:   "} {
		if _, err := CleanModels([]string{"openai/gpt-5", bad}); err == nil {
			t.Errorf("kept %q", bad)
		}
	}
	got, err := CleanModels([]string{" re:^gpt-.* ", "openai/*", "RE:^GPT-.*"})
	if err != nil || !slices.Equal(got, []string{"re:^gpt-.*", "openai/*"}) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestCleanAliases(t *testing.T) {
	got, err := CleanAliases(map[string]string{" fast ": " openai/gpt-5 ", "": ""})
	if err != nil || len(got) != 1 || got["fast"] != "openai/gpt-5" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []map[string]string{
		{"a b": "openai/gpt-5"}, {"re:x": "openai/gpt-5"}, {"x*": "openai/gpt-5"},
		{"fast": "gpt-5"}, {"fast": "openai/*"}, {"fast": "re:^gpt"}, {"openai/gpt-5": "openai/GPT-5"},
		{"fast": ""}, {"": "openai/gpt-5"}, {"Fast": "openai/gpt-5", "fast": "openai/gpt-4"},
	} {
		if _, err := CleanAliases(bad); err == nil {
			t.Errorf("kept %v", bad)
		}
	}
}

func TestIssuedKeyTakesNoPattern(t *testing.T) {
	_, err := IssueKey(Issue{Name: "x", TTL: 3600e9, Models: []string{"re:^gpt-"}})
	if err == nil {
		t.Fatal("issued a key with a pattern")
	}
}
