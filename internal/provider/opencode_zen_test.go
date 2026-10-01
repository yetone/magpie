package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// ARNO on Discord: OpenCode Zen's free models answer 403 "OpenCode's free
// tier can only be used from within OpenCode", so they aren't listed.
func TestOpenCodeZenDropsFreeModels(t *testing.T) {
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	got := p.planModels([]catalog.Model{{ID: "mimo-v2.6-flash-free"}, {ID: "claude-opus-5-5"}, {ID: "gpt-5.5"}})
	if len(got) != 2 || got[0].ID != "claude-opus-5-5" || got[1].ID != "gpt-5.5" {
		t.Fatalf("got %v", got)
	}
	// any other provider's list is as it came
	o := Provider{Preset: "together"}
	if got := o.planModels([]catalog.Model{{ID: "a-free"}}); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}
