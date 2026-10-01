package gui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
)

// The state the page draws carries what each profile holds, by agent, for a
// profile's details to show before it is applied (#467), and nothing that
// reads as a key.
func TestStateProfileDetails(t *testing.T) {
	sandboxHome(t)
	if err := profile.Save("work", profile.Profile{Fields: map[string]string{
		"codex.model": "gpt-5", "codex.effort": "high", "codex.token": "abc",
	}}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(state())
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Profiles []struct {
			Name   string `json:"name"`
			Agents []struct {
				ID, Name string
				Fields   []struct {
					Key, Label, Value string
					Hidden            bool
				}
			} `json:"agents"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Profiles) != 1 || len(s.Profiles[0].Agents) != 1 {
		t.Fatalf("profiles %s", b)
	}
	a := s.Profiles[0].Agents[0]
	got := map[string]string{}
	for _, f := range a.Fields {
		got[f.Key] = f.Label + "=" + f.Value
		if f.Key == "token" && !f.Hidden {
			t.Errorf("token must be hidden: %+v", f)
		}
	}
	if a.ID != "codex" || a.Name != "Codex" || got["model"] != "model=gpt-5" || got["effort"] != "effort=high" || got["token"] != "token=" {
		t.Errorf("codex %+v", a)
	}
	if strings.Contains(string(b), `"abc"`) {
		t.Errorf("state carries the token: %s", b)
	}
}
