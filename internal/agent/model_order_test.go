package agent

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// An order dragged on the Agents page reaches every agent, not only Codex
// (#1052): Claude Code's /model picker (viaMagpie, which writePicker turns
// into settings.json's modelPicker) and the model files magpie writes for
// the others (magpieModels) list the models in it, and one agent's order
// leaves another's alone.
func TestModelOrderReachesEveryAgent(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	refs := func(agent string) (out []string) {
		for _, o := range viaMagpie(agent, "") {
			out = append(out, o.Ref)
		}
		return out
	}
	ids := func(agent string) (out []string) {
		for _, m := range magpieModels(agent) {
			out = append(out, m.ID)
		}
		return out
	}
	was := ids("opencode")
	if want := []string{"relay/glm-4.6", "other/m1", "other/m2"}; !slices.Equal(was, want) {
		t.Fatalf("default %v, want %v", was, want)
	}
	if err := provider.SetModelOrder("claude", []string{"other/m2", "relay/glm-4.6"}); err != nil {
		t.Fatal(err)
	}
	if got, want := refs("claude"), []string{"other/m2", "relay/glm-4.6", "other/m1"}; !slices.Equal(got, want) {
		t.Errorf("Claude Code's picker %v, want %v", got, want)
	}
	if got := ids("opencode"); !slices.Equal(got, was) {
		t.Errorf("claude's order moved opencode's: %v", got)
	}
	if err := provider.SetModelOrder("opencode", []string{"other/m1"}); err != nil {
		t.Fatal(err)
	}
	if got, want := ids("opencode"), []string{"other/m1", "relay/glm-4.6", "other/m2"}; !slices.Equal(got, want) {
		t.Errorf("opencode's models %v, want %v", got, want)
	}
}
