package gui

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Routing page tests a member from its row, so groupsState says of each
// member what its provider says of a model's test (provider.ModelTest): a
// decision API whose models are tested as one endpoint says why the row's
// Test is off, rather than the page asking and showing the provider's
// refusal as the model's. A member that can be tested carries none.
func TestGroupMemberSaysWhenItsModelCantBeTested(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// Vercel's evaluation models: a decision API asked as one endpoint, not
	// model by model (AsksDecideModels is false for /v4/ai)
	if err := provider.Save(provider.Provider{ID: "judge", Name: "Vercel", Key: "k",
		Decide: "https://ai-gateway.vercel.sh/v4/ai", Models: []string{"typesafe/jev-eval"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "chat", Name: "Chat", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "judge", Name: "Judge", Members: []string{"judge/typesafe/jev-eval"}},
		{ID: "talk", Name: "Talk", Members: []string{"chat/m"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}

	st := groupsState()
	byID := map[string]groupJSON{}
	for _, g := range st.Groups {
		byID[g.ID] = g
	}
	for id, want := range map[string]string{"judge": "decide", "talk": ""} {
		g, ok := byID[id]
		if !ok {
			t.Fatalf("group %s is gone", id)
		}
		if len(g.Info) != 1 {
			t.Fatalf("%s lists %d members", id, len(g.Info))
		}
		if got := g.Info[0].ModelTest; got != want {
			t.Errorf("%s's member says modelTest %q, want %q", id, got, want)
		}
		if g.Info[0].Provider == "" || g.Info[0].Model == "" {
			t.Errorf("%s's member is not resolved: %+v", id, g.Info[0])
		}
	}

	// a model a group not saved yet may name is told the same, from the
	// lists the editor's picker reads
	for _, m := range st.Models {
		if m.ID == "chat/m" && m.ModelTest != "" {
			t.Errorf("chat/m is said untestable: %q", m.ModelTest)
		}
	}
	for _, m := range st.Deciders {
		if m.ID == "judge/typesafe/jev-eval" && m.ModelTest != "decide" {
			t.Errorf("the decision model says modelTest %q, want decide", m.ModelTest)
		}
	}

	// and the page reads it off the wire: a testable member has no such key,
	// which is what it takes for testable
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			ID   string           `json:"id"`
			Info []map[string]any `json:"memberInfo"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, g := range doc.Groups {
		if g.ID != "judge" && g.ID != "talk" {
			continue
		}
		got, has := g.Info[0]["modelTest"]
		if g.ID == "judge" && (!has || got != "decide") {
			t.Errorf("the decision member is on the wire as %v (present %v), want decide", got, has)
		}
		if g.ID == "talk" && has {
			t.Errorf("the chat member carries modelTest %v on the wire", got)
		}
	}
}
