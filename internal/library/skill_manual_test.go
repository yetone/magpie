package library

import (
	"path/filepath"
	"testing"
)

// sunset3279 on Discord: a skill whose SKILL.md sets
// disable-model-invocation is one Claude Code runs only when the user types
// /name. The page marks it, in the library and among the agents' own; one
// whose front matter doesn't parse is not known to be, so it isn't marked,
// and its name and description are read as before.
func TestSkillManualFromFrontMatter(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	front := map[string]string{
		"deploy":  "---\nname: deploy\ndescription: Ship it\ndisable-model-invocation: true\n---\n",
		"lint":    "---\nname: lint\ndescription: Lint\ndisable-model-invocation: false\n---\n",
		"plain":   "---\nname: plain\ndescription: Plain\n---\n",
		"crlf":    "---\r\nname: crlf\r\ndescription: Windows\r\ndisable-model-invocation: true\r\n---\r\n",
		"broken":  "---\nname: broken\ndescription: Broken\ndisable-model-invocation: true\nallowed-tools: [a\n---\n",
		"notbool": "---\nname: notbool\ndescription: Odd\ndisable-model-invocation: sometimes\n---\n",
	}
	for name, md := range front {
		write(t, filepath.Join(src, name, "SKILL.md"), md)
	}
	// one Claude Code has of its own, not in the library
	write(t, filepath.Join(h, ".claude/skills/release/SKILL.md"), "---\nname: release\ndescription: Cut a release\ndisable-model-invocation: true\n---\n")
	write(t, filepath.Join(h, ".claude/skills/notes/SKILL.md"), "---\nname: notes\ndescription: Notes\n---\n")
	ok(t)(InstallSkills(src, []string{"deploy", "lint", "plain", "crlf", "broken", "notbool"}, []string{"codex"}))

	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"deploy": true, "lint": false, "plain": false, "crlf": true, "broken": false, "notbool": false}
	got := map[string]bool{}
	for _, s := range v.Skills {
		got[s.Name] = s.Manual
		if w, ok := want[s.Name]; ok && s.Manual != w {
			t.Errorf("%s: manual %v, want %v", s.Name, s.Manual, w)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s isn't in the library: %+v", name, v.Skills)
		}
	}
	found := map[string]FoundSkill{}
	for _, f := range v.FoundSkills {
		found[f.Name] = f
	}
	if f, ok := found["release"]; !ok || !f.Manual {
		t.Errorf("release: %+v", f)
	}
	if f, ok := found["notes"]; !ok || f.Manual {
		t.Errorf("notes: %+v", f)
	}
	// a front matter that does not parse is not known to be manual
	if m := parseMeta([]byte(front["broken"])); m.Manual {
		t.Errorf("broken: %+v", m)
	}
}
