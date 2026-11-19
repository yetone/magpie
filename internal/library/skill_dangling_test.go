package library

import (
	"os"
	"path/filepath"
	"testing"
)

// #7226: a skill brought in from ~/.agents/skills is a link into it. After
// `npx skills remove` the folder is gone: the library shows the skill as
// gone, no agent is given a link to it (and magpie's own link there is
// taken away), and the user's own files are untouched.
func TestLinkedInSkillWhoseSourceIsGone(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "grilling"), "grilling", "Grill a plan")
	skill(t, filepath.Join(shared, "keeper"), "keeper", "Stays")
	os.MkdirAll(filepath.Join(h, ".claude"), 0o755)
	if _, err := ImportSkills([]string{"grilling", "keeper"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SkillAgents("grilling", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	cl := filepath.Join(h, ".claude/skills/grilling")
	if _, err := os.Stat(filepath.Join(cl, "SKILL.md")); err != nil {
		t.Fatalf("the agent isn't given the skill: %v", err)
	}
	// npx skills remove grilling
	if err := os.RemoveAll(filepath.Join(shared, "grilling")); err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Skills {
		switch s.Name {
		case "grilling":
			if s.Gone == "" {
				t.Errorf("a skill whose source is gone isn't said to be: %+v", s)
			}
		case "keeper":
			if s.Gone != "" {
				t.Errorf("a skill whose source is there is gone: %+v", s)
			}
		}
	}
	// a change syncs every agent: none gets a link to it
	if _, err := SkillAgents("grilling", []string{"claude", "codex"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cl, filepath.Join(h, ".codex/skills/grilling")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s: an agent still has a link to the gone skill", p)
		}
	}
	if _, err := os.Stat(filepath.Join(shared, "keeper/SKILL.md")); err != nil {
		t.Errorf("another skill of the user's was touched: %v", err)
	}
	// the source comes back (an unmounted drive, a reinstall): given again
	skill(t, filepath.Join(shared, "grilling"), "grilling", "Grill a plan")
	if _, err := SkillAgents("grilling", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cl, "SKILL.md")); err != nil {
		t.Errorf("not given again once its source is back: %v", err)
	}
	v, _ = Read(nil)
	for _, s := range v.Skills {
		if s.Name == "grilling" && s.Gone != "" {
			t.Errorf("still gone with its source back: %+v", s)
		}
	}
}

// A folder with its SKILL.md taken out, what a skills CLI that empties the
// entry leaves, is as gone. The library's own folder never is.
func TestSourceGoneKinds(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "a"), "a", "A")
	skill(t, skillDir("own"), "own", "Own")
	if err := dirLink(filepath.Join(shared, "a"), skillDir("a")); err != nil {
		t.Fatal(err)
	}
	if sourceGone("a") || sourceGone("own") {
		t.Fatal("a skill that is there is gone")
	}
	os.Remove(filepath.Join(shared, "a/SKILL.md"))
	if !sourceGone("a") {
		t.Error("an emptied entry isn't gone")
	}
	if sourceGone("own") || sourceGone("nothing") {
		t.Error("the library's own folder, or no entry, is gone")
	}
	os.RemoveAll(filepath.Join(shared, "a"))
	if !sourceGone("a") {
		t.Error("a removed folder isn't gone")
	}
	if _, err := os.Lstat(skillDir("a")); err != nil {
		t.Errorf("the library's link was taken away: %v", err)
	}
}
