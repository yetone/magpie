package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A skill made in the Library (josephBenchap on X): SKILL.md with valid
// front matter in the library's folder, given to the agents picked, and a
// name that clashes or isn't folder-safe is refused.
func TestCreateSkill(t *testing.T) {
	h := sandbox(t)
	ok(t)(CreateSkill("my-skill", "Does: a thing\nacross lines", []string{"claude", "codex"}))
	b, err := os.ReadFile(filepath.Join(skillDir("my-skill"), "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := parseMeta(b)
	if m.Name != "my-skill" || m.Description != "Does: a thing across lines" {
		t.Errorf("front matter: %+v\\n%s", m, b)
	}
	for _, d := range []string{".claude/skills/my-skill", ".codex/skills/my-skill"} {
		if _, err := os.Stat(filepath.Join(h, d, "SKILL.md")); err != nil {
			t.Errorf("not given to %s: %v", d, err)
		}
	}
	v, _ := Read(nil)
	if len(v.Skills) != 1 || v.Skills[0].Name != "my-skill" {
		t.Errorf("listed: %+v", v.Skills)
	}
	skill(t, filepath.Join(h, ".claude/skills/theirs"), "theirs", "An agent's own")
	for _, c := range [][2]string{
		{"My-Skill", "d"}, {"theirs", "d"}, {"../x", "d"}, {"a/b", "d"}, {"", "d"}, {"fine", "  "}, {"fine", strings.Repeat("x", 1025)},
	} {
		if _, err := CreateSkill(c[0], c[1], nil); err == nil {
			t.Errorf("%q / %.10q was taken", c[0], c[1])
		}
	}
	if _, err := os.Lstat(skillDir("fine")); err == nil {
		t.Error("a refused skill left a folder")
	}
}
