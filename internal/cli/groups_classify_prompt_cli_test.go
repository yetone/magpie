package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// classify-prompt= sets a group's own classifier instructions (sloceanxc on
// X), an "=" in them kept, and empty gives magpie's back.
func TestGroupClassifyPromptPairs(t *testing.T) {
	none := func(string) (string, error) { return "", nil }
	g := provider.Group{}
	const own = "Kinds are about tone: a = b is maths, not code."
	if err := applyGroupPairs(&g, []string{"classify-prompt= " + own + " "}, none, false); err != nil || g.ClassifyPrompt != own {
		t.Fatalf("set: %v %q", err, g.ClassifyPrompt)
	}
	if err := applyGroupPairs(&g, []string{"classify-prompt="}, none, false); err != nil || g.ClassifyPrompt != "" {
		t.Fatalf("empty: %v %q", err, g.ClassifyPrompt)
	}
}
