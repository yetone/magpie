package provider

import (
	"path/filepath"
	"testing"
)

func TestContextSetByUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// set, not empty: a group saved lists what Devin's CLI reports, and
	// Devin given an empty one writes its config into the working directory
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	p := Provider{ID: "a", Contexts: map[string]int{"*": 272000, "big": 1000000}}
	if got := p.ContextOf("big"); got != 1000000 {
		t.Errorf("big: %d", got)
	}
	if got := p.ContextOf("other"); got != 272000 {
		t.Errorf("other: %d", got)
	}
	if got := (Provider{ID: "b"}).ContextOf("x"); got != 0 {
		t.Errorf("unset: %d", got)
	}

	e := func(p, m string, ctx int) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: Provider{ID: p}, Context: ctx}
	}
	entries := []Entry{e("a", "m1", 128000), e("b", "m2", 200000)}
	of := func() int {
		for _, g := range groupEntries(entries) {
			if g.ID == GroupPrefix+"g" {
				return g.Context
			}
		}
		t.Fatal("no group g")
		return 0
	}
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 200000 {
		t.Errorf("largest member's: %d", got)
	}
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, Context: 400000}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 400000 {
		t.Errorf("set: %d", got)
	}
	// one set below the members' still wins
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, Context: 100000}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 100000 {
		t.Errorf("set lower: %d", got)
	}
}

// #712: a group of DeepSeek V4.1 Flash (1M) at WorkBuddy and OpenCode Go,
// with OpenCode Zen's free DeepSeek V4 Flash (200k, models.dev) and a
// model whose window nothing says among them, is told the largest window:
// a conversation too long for the free one goes on to a member with room
// (#700), so Pi was made to compact at 200k for nothing.
func TestGroupToldItsLargestWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	e := func(p, m string, ctx int) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: Provider{ID: p}, Context: ctx}
	}
	entries := []Entry{
		e("workbuddy-ai", "deepseek-v4.1-flash", 1000000),
		e("opencode-go", "deepseek-v4.1-flash", 1000000),
		e("opencode-zen", "deepseek-v4-flash-free", 200000),
		e("opencode-go", "muse-spark-1.3-contributor", 1048576),
		e("zcode", "GLM-5.3-Flash", 0),
	}
	members := []string{"workbuddy-ai/deepseek-v4.1-flash:max", "opencode-go/deepseek-v4.1-flash:high",
		"opencode-zen/deepseek-v4-flash-free:high", "opencode-go/muse-spark-1.3-contributor:xhigh", "zcode/GLM-5.3-Flash"}
	if err := SaveGroup(Group{ID: "deepseek", Name: "腾讯DeepSeek", Members: members}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupEntries(entries) {
		if g.ID == GroupPrefix+"deepseek" {
			if g.Context != 1048576 {
				t.Errorf("context %d, want the largest member's 1048576", g.Context)
			}
			return
		}
	}
	t.Fatal("no group deepseek")
}

// #120: a subscription's models take the window the user sets on it, over
// the one its backend says (Codex: 272K) — kept through the account's save.
func TestContextSetOnAccount(t *testing.T) {
	signIn(t)
	of := func() int {
		for _, e := range Served() {
			if e.ID == "codex/gpt-5.5" {
				return e.Context
			}
		}
		t.Fatal("no codex/gpt-5.5")
		return 0
	}
	before := of()
	if err := Save(Provider{ID: "codex", Contexts: map[string]int{"*": 872000}}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 872000 {
		t.Errorf("set: %d, was %d", got, before)
	}
}
