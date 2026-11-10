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

// An issuer's pattern is never tried on a glob's text: [^-] or '.' takes
// its '*', so "re:^openai/[^-]*$" covered "openai/*" and an issued key
// got openai/gpt-4o, which its issuer may not use.
func TestPatternCoversAGlobOnlyForAll(t *testing.T) {
	for _, c := range []struct {
		issuer, child string
		want          bool
	}{
		{"re:^openai/[^-]*$", "openai/*", false},
		{"re:^openai/gpt-.$", "openai/gpt-*", false},
		{"re:^openai/gpt-5$", "openai/gpt-5*", false},
		{"re:^openai/gpt\\b", "openai/gpt*", false},
		{"re:^gr", "gr*", false}, // the glob names group/… too
		{"re:^openai/", "openai/gpt-*", true},
		{"re:^gpt-", "openai/gpt-*", true},
		{"re:gpt", "*gpt*", false}, // only the text before the '*' is judged
		{"re:^openai/[^-]*$", "openai/o3", true},
		{"re:^openai/[^-]*$", "openai/gpt-4o", false},
	} {
		if got := covered([]string{c.issuer}, c.child); got != c.want {
			t.Errorf("covered(%q, %q) = %v, want %v", c.issuer, c.child, got, c.want)
		}
	}
	// at the gateway, the issued key is held to what both allow (an empty
	// fold is refused there)
	may := func(who []string, id string) bool { return len(who) > 0 && ModelAllowed(who, id) }
	who := foldModels([]string{"openai/*"}, []string{"re:^openai/[^-]*$"})
	if may(who, "openai/gpt-4o") {
		t.Errorf("folded %q: an issued key may use openai/gpt-4o, which its issuer may not", who)
	}
	// a glob in the child never stands in for the issuer's pattern
	if who := foldModels([]string{"r*"}, []string{"re:^openai/"}); may(who, "openai/gpt-4o") {
		t.Errorf("folded %q: the child's r* took the issuer's whole pattern", who)
	}
	if who := foldModels([]string{"*"}, []string{"re:^openai/"}); !may(who, "openai/gpt-4o") || may(who, "anthropic/x") {
		t.Errorf("folded %q: * with the issuer's pattern is the pattern", who)
	}
}
