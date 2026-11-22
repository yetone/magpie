package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// A Codex subagent's effort is held to the levels of the model it runs
// on, its own or the Agents page's for every subagent: Codex refuses to
// start a subagent at one its model lacks. The page is given each
// model's levels to say so before saving; an agent other than Codex, or
// a model the lead picks, takes any.
func TestCodexSubagentEffortHeldToItsModel(t *testing.T) {
	sandboxHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("relay/m1", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(subagentModels(), func(m subagentModel) bool { return m.ID == "relay/m1" })
	if i < 0 || !slices.Equal(subagentModels()[i].Efforts, []string{"low", "high"}) {
		t.Fatalf("the page's subagent models: %+v", subagentModels())
	}

	s := library.Subagent{Name: "reviewer", Model: "relay/m1", Effort: "max", Agents: []string{"claude", "codex"}}
	if err := codexSubagentEffort(s); err == nil || !strings.Contains(err.Error(), "low, high") {
		t.Errorf("max on a model at low and high: %v", err)
	}
	for _, x := range []library.Subagent{
		{Model: "relay/m1", Effort: "high", Agents: []string{"codex"}},
		{Model: "relay/m1", Effort: "max", Agents: []string{"claude"}},
		{Effort: "max", Agents: []string{"codex"}},
		{Model: "relay/m1", Agents: []string{"codex"}},
	} {
		if err := codexSubagentEffort(x); err != nil {
			t.Errorf("%+v: %v", x, err)
		}
	}
	// no model of its own: the one the Agents page puts every Codex
	// subagent on
	if err := provider.SetCodexSubagentModel("relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := codexSubagentEffort(library.Subagent{Effort: "xhigh", Agents: []string{"codex"}}); err == nil {
		t.Error("xhigh on the Agents page's model at low and high saved")
	}
}
