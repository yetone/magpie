package library

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A hook goes with the library to another computer, which writes it into
// its agents beside the user's own hooks there; what magpie wrote into the
// first computer's agents stays out of the bundle; taking it out on one
// takes magpie's entry out on the other and leaves the user's.
func TestCarryHooks(t *testing.T) {
	sandbox(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Description: "Ping my phone", Event: "turn-end", Command: "curl -s https://api.day.app/KEY/done",
		Timeout: 10, Agents: []string{"codex", "claude"}}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	carried := mustJSON(t, b)
	var raw struct {
		Hooks []map[string]any `json:"hooks"`
	}
	json.Unmarshal([]byte(carried), &raw)
	if len(raw.Hooks) != 1 {
		t.Fatalf("hooks carried: %s", carried)
	}
	if got := raw.Hooks[0]; got["name"] != "bark" || got["event"] != "turn-end" || got["description"] != "Ping my phone" ||
		got["command"] != "curl -s https://api.day.app/KEY/done" || got["timeout"] != float64(10) || mustJSON(t, got["agents"]) != `["claude","codex"]` {
		t.Errorf("the hook carried: %v", got)
	}
	if strings.Contains(carried, "applied") || strings.Contains(carried, `"entry"`) {
		t.Errorf("what magpie wrote here is carried:\n%s", carried)
	}

	h2 := sandbox(t)
	settings := filepath.Join(h2, ".claude/settings.json")
	write(t, settings, `{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say my own"}]}]}}`)
	r := putJSON(t, carried)
	for _, p := range r.Problems {
		if strings.HasPrefix(p.What, "hook") {
			t.Errorf("a problem: %+v", p)
		}
	}
	if got := hooksOf(t, settings, "Stop"); len(got) != 2 || !strings.Contains(got[0], "say my own") || !strings.Contains(got[1], "api.day.app") {
		t.Errorf("claude's Stop on the second computer: %v", got)
	}
	if got := hooksOf(t, filepath.Join(h2, ".codex/hooks.json"), "Stop"); len(got) != 1 || !strings.Contains(got[0], "api.day.app") {
		t.Errorf("codex's Stop on the second computer: %v", got)
	}

	// the first computer takes it out
	none := []*Hook{}
	b.Hooks = &none
	putJSON(t, mustJSON(t, b))
	if got := hooksOf(t, settings, "Stop"); len(got) != 1 || !strings.Contains(got[0], "say my own") {
		t.Errorf("after the hook was taken out, claude's Stop: %v", got)
	}
	if !strings.Contains(read(t, settings), `"model": "opus"`) {
		t.Errorf("the user's settings changed:\n%s", read(t, settings))
	}
}

// A bundle from a magpie before hooks were carried has none, and knew
// nothing of this computer's: they stay.
func TestCarryOldBundleKeepsHooks(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "github", Transport: "stdio", Command: "npx", Agents: []string{"claude"}}))
	b, err := Collect()
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	b.Hooks = nil
	old := mustJSON(t, b)
	if strings.Contains(old, `"hooks"`) {
		t.Fatalf("not as an older magpie wrote it: %s", old)
	}
	h := sandbox(t)
	ok(t)(SaveHook("", Hook{Name: "mine", Event: "turn-end", Command: "say mine", Agents: []string{"claude"}}))
	putJSON(t, old)
	l, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if l.Hooks == nil || len(*l.Hooks) != 1 || (*l.Hooks)[0].Name != "mine" {
		t.Fatalf("an older bundle changed the hooks: %s", mustJSON(t, l.Hooks))
	}
	if got := hooksOf(t, filepath.Join(h, ".claude/settings.json"), "Stop"); len(got) != 1 {
		t.Errorf("claude's Stop: %v", got)
	}
}

// A hook on an event a newer magpie knows and this one doesn't is carried
// as it comes: the agent it is given to has a problem, and the rest of the
// library comes in. A hook with no name or command, or one named twice,
// refuses the bundle.
func TestCarryNewerHooks(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "github", Transport: "stdio", Command: "npx", Agents: []string{"claude"}}))
	b, err := Collect()
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	hooks := []*Hook{
		{Name: "later", Event: "turn-later", Command: "say later", Agents: []string{"claude", "newagent"}},
		{Name: "done", Event: "turn-end", Command: "say done", Agents: []string{"claude"}},
	}
	b.Hooks = &hooks
	h := sandbox(t)
	r := putJSON(t, mustJSON(t, b))
	if got := hooksOf(t, filepath.Join(h, ".claude/settings.json"), "Stop"); len(got) != 1 || !strings.Contains(got[0], "say done") {
		t.Errorf("claude's Stop: %v", got)
	}
	found := false
	for _, p := range r.Problems {
		found = found || p.What == "hook:later"
	}
	if !found {
		t.Errorf("no problem for the event this magpie doesn't know: %+v", r.Problems)
	}
	l, _ := Collect()
	if len(*l.Hooks) != 2 || (*l.Hooks)[1].Name != "later" || mustJSON(t, (*l.Hooks)[1].Agents) != `["claude","newagent"]` {
		t.Errorf("not kept as it came: %s", mustJSON(t, l.Hooks))
	}

	for _, bad := range [][]*Hook{
		{{Name: "a b", Event: "turn-end", Command: "x"}},
		{{Name: "x", Event: "turn-end", Command: " "}},
		{{Name: "x", Event: "turn-end", Command: "x"}, {Name: "x", Event: "prompt", Command: "y"}},
		{nil},
	} {
		b.Hooks = &bad
		var in Bundle
		json.Unmarshal([]byte(mustJSON(t, b)), &in)
		if _, err := Put(&in); err == nil {
			t.Errorf("put %s", mustJSON(t, bad))
		}
	}
}
