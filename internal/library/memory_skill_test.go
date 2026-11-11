package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/memory"
)

// A procedure made a skill: a SKILL.md of its own in the library, named
// from its words, given to the agents ticked for instructions, and the
// memory set aside as that skill's. A second by the same words gets
// another name, and one in Chinese one from its id.
func TestMemorySkill(t *testing.T) {
	sandbox(t)
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"codex"}}))
	mk := func(text string) *memory.Memory {
		t.Helper()
		k, s, why := memory.Procedure, memory.Global, "assets come last"
		m, _, err := memory.Save(memory.Input{Text: &text, Kind: &k, Scope: &s, Why: &why}, memory.FromAgent, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := mk("To release: tag it, wait for the 16 assets, then post.\nNever before.")
	_, name, err := MemorySkill(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name != "to-release-tag-it-wait" {
		t.Fatalf("named %q", name)
	}
	md := read(t, filepath.Join(skillDir(name), "SKILL.md"))
	for _, w := range []string{"name: to-release-tag-it-wait\n", `description: "To release: tag it, wait for the 16 assets, then post."`, "Never before.", "Why: assets come last"} {
		if !strings.Contains(md, w) {
			t.Fatalf("SKILL.md lacks %q:\n%s", w, md)
		}
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.skill(name)
	if s == nil || s.Source != nil || !slices.Equal(s.Agents, []string{"codex"}) {
		t.Fatalf("in the library: %+v", s)
	}
	if got, _ := memory.Get(m.ID); got.Status != memory.Archived || got.Skill != name {
		t.Fatalf("the memory after: %+v", got)
	}
	if _, _, err := MemorySkill(m.ID); err == nil {
		t.Fatal("made a skill twice")
	}
	_, name2, err := MemorySkill(mk("To release: tag it, wait for CI first.").ID)
	if err != nil || name2 != "to-release-tag-it-wait-2" {
		t.Fatalf("second: %q, %v", name2, err)
	}
	_, name3, err := MemorySkill(mk("发布：先打 tag，等资产齐了再发帖。").ID)
	if err != nil || !strings.HasPrefix(name3, "procedure-") {
		t.Fatalf("Chinese: %q, %v", name3, err)
	}
	// only a procedure
	k, sc, text := memory.Rule, memory.Global, "Never force-push."
	r, _, _ := memory.Save(memory.Input{Text: &text, Kind: &k, Scope: &sc}, memory.FromAgent, nil, nil)
	if _, _, err := MemorySkill(r.ID); err == nil {
		t.Fatal("a rule was made a skill")
	}
}
