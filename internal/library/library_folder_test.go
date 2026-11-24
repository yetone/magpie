package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// #595: "Add from folder" pointed at a skill in the library's own folder
// emptied every file of it (it was copied onto itself), and one from
// elsewhere was copied into a folder by that name already there.
func TestAddFromLibraryFolderKeepsIt(t *testing.T) {
	sandbox(t)
	mine := skillDir("mine")
	skill(t, mine, "mine", "Mine")
	before := read(t, filepath.Join(mine, "SKILL.md"))
	same := func() {
		t.Helper()
		if got := read(t, filepath.Join(mine, "SKILL.md")); got != before {
			t.Errorf("SKILL.md is now %q", got)
		}
		if got := read(t, filepath.Join(mine, "scripts/run.sh")); got != "echo hi\n" {
			t.Errorf("run.sh is now %q", got)
		}
	}
	// the skill's folder itself, and the library's skills folder
	for _, in := range []struct{ src, path string }{{mine, ""}, {skillsDir(), "mine"}} {
		if _, err := InstallSkills(in.src, []string{in.path}, nil); err == nil || !strings.Contains(err.Error(), "already") {
			t.Errorf("%s: %v", in.src, err)
		}
		same()
	}
	// another folder by that name is never copied into it
	other := filepath.Join(t.TempDir(), "mine")
	skill(t, other, "mine", "Another")
	write(t, filepath.Join(other, "extra.md"), "x")
	if _, err := InstallSkills(other, []string{""}, nil); err == nil {
		t.Error("another mine was added over the library folder's")
	}
	same()
	if _, err := os.Stat(filepath.Join(mine, "extra.md")); !os.IsNotExist(err) {
		t.Error("the other folder's files were copied into it")
	}
	if l, _ := load(); l.skill("mine") != nil {
		t.Error("listed")
	}
	// copyDir never copies a folder onto or into itself
	if err := copyDir(mine, mine); err == nil {
		t.Error("copied onto itself")
	}
	if err := copyDir(mine, filepath.Join(mine, "sub")); err == nil {
		t.Error("copied into itself")
	}
	same()
}

// #595, jwaitq on Discord: a skill put in the library's folder by hand,
// not in library.json, was shown nowhere, then only under "In your
// agents", to be brought in by a click. The next read of the page brings
// it in where it is, as that click did, once; a dot-folder, a folder with
// no SKILL.md and a copy of magpie's stay out, and one the user removed
// isn't brought back.
func TestFoundInLibraryFolder(t *testing.T) {
	h := sandbox(t)
	hand := skillDir("hand")
	skill(t, hand, "hand", "By hand")
	ext := filepath.Join(h, "elsewhere/linky")
	skill(t, ext, "linky", "Linked")
	symlink(t, ext, skillDir("linky"))
	// magpie's link to it, left in Codex
	symlink(t, hand, filepath.Join(h, ".codex/skills/hand"))
	// a folder named in another script goes by its SKILL.md's name
	skill(t, filepath.Join(skillsDir(), "头脑风暴"), "brainstorm", "Ideas")
	// not a skill, magpie's own leftovers and a copy of magpie's
	write(t, filepath.Join(skillsDir(), "notes/README.md"), "")
	skill(t, filepath.Join(skillsDir(), oldPrefix("hand")+"-1"), "hand", "Old")
	skill(t, filepath.Join(skillsDir(), ".hand.next"), "hand", "Next")
	skill(t, filepath.Join(skillsDir(), "copied"), "copied", "A copy")
	write(t, filepath.Join(skillsDir(), "copied", marker), "copied from elsewhere\n")

	listed := func() map[string]SkillView {
		t.Helper()
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(v.FoundSkills) != 0 {
			t.Errorf("still found: %+v", v.FoundSkills)
		}
		out := map[string]SkillView{}
		for _, s := range v.Skills {
			out[s.Name] = s
		}
		return out
	}
	got := listed()
	if len(got) != 3 || got["hand"].Kind != "" || got["linky"].Kind != "folder" || got["brainstorm"].Name == "" {
		t.Fatalf("listed: %+v", got)
	}
	if fi, err := os.Lstat(hand); err != nil || !fi.IsDir() || read(t, filepath.Join(hand, "scripts/run.sh")) != "echo hi\n" {
		t.Errorf("hand isn't where it was: %v", err)
	}
	if !linked(skillDir("linky")) || realDir(skillDir("linky")) != realDir(ext) {
		t.Error("linky's link changed")
	}
	if !ours(filepath.Join(h, ".codex/skills/hand"), "hand") {
		t.Error("codex lost hand")
	}
	if _, err := os.Stat(filepath.Join(skillDir("brainstorm"), "SKILL.md")); err != nil {
		t.Errorf("brainstorm wasn't named: %v", err)
	}
	l, _ := load()
	if s := l.skill("hand"); s == nil || s.Source != nil || !slices.Equal(s.Agents, []string{"codex"}) {
		t.Errorf("hand: %+v", s)
	}
	if s := l.skill("linky"); s == nil || s.Source == nil || s.Source.Kind != "folder" || s.Source.Dir != realDir(ext) || len(s.Agents) != 0 {
		t.Errorf("linky: %+v", s)
	}
	if s := l.skill("brainstorm"); s == nil || len(s.Agents) != 0 {
		t.Errorf("brainstorm: %+v", s)
	}
	for _, n := range []string{"notes", "copied", oldPrefix("hand") + "-1", ".hand.next"} {
		if _, err := os.Stat(filepath.Join(skillsDir(), n)); err != nil {
			t.Errorf("%s was moved: %v", n, err)
		}
	}

	// once: another read lists the same, each once
	if again := listed(); len(again) != 3 {
		t.Errorf("listed again: %+v", again)
	}
	if l, _ := load(); len(l.Skills) != 3 {
		t.Errorf("library: %+v", l.Skills)
	}

	// removed, it stays out
	ok(t)(RemoveSkills([]string{"hand", "brainstorm"}))
	if got := listed(); len(got) != 1 || got["linky"].Name == "" {
		t.Errorf("after removing: %+v", got)
	}
}

// #595: a library skill kept in ~/.agents/skills is had by every agent
// that reads that folder: none of them gets a link of its own besides
// (the skill twice), one made before goes, and the page says they have it.
func TestSharedSkillNotLinkedTwice(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	ext := filepath.Join(h, "src/lint")
	skill(t, ext, "lint", "Lint")
	ok(t)(InstallSkills(ext, []string{""}, []string{"codex", "gemini", "pi"}))
	for _, d := range []string{".codex/skills/lint", ".gemini/skills/lint", ".pi/agent/skills/lint"} {
		if !ours(filepath.Join(h, d), "lint") {
			t.Fatalf("%s isn't the library's", d)
		}
	}
	v, _ := Read(nil)
	if len(v.Skills) != 1 || len(v.Skills[0].Always) != 0 {
		t.Fatalf("always before it's shared: %+v", v.Skills)
	}
	// it is put in the shared folder too (npx skills add, a link of the user's)
	symlink(t, ext, filepath.Join(h, ".agents/skills/lint"))
	ok(t)(Sync())
	for _, d := range []string{".codex/skills/lint", ".gemini/skills/lint"} {
		if _, err := os.Lstat(filepath.Join(h, d)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", d, err)
		}
	}
	if !ours(filepath.Join(h, ".pi/agent/skills/lint"), "lint") {
		t.Error("pi, which doesn't read the shared folder, lost it")
	}
	if !linked(filepath.Join(h, ".agents/skills/lint")) || read(t, filepath.Join(ext, "SKILL.md")) == "" {
		t.Error("the shared folder's entry changed")
	}
	v, _ = Read(nil)
	always := v.Skills[0].Always
	if !slices.Contains(always, "codex") || !slices.Contains(always, "gemini") || slices.Contains(always, "pi") || slices.Contains(always, "claude") {
		t.Errorf("always: %v", always)
	}
	// ticked again, it still gets no link of its own
	ok(t)(SkillAgents("lint", []string{"codex", "claude"}))
	if _, err := os.Lstat(filepath.Join(h, ".codex/skills/lint")); !os.IsNotExist(err) {
		t.Errorf("codex got a link again: %v", err)
	}
	if !ours(filepath.Join(h, ".claude/skills/lint"), "lint") {
		t.Error("claude didn't get it")
	}
}

// #595: a skill linked from a folder whose link was replaced by the folder
// itself is the library's own: the page doesn't say it's linked, as
// Remove moves it to the backups.
func TestSkillKindIsWhatTheFolderIs(t *testing.T) {
	h := sandbox(t)
	ext := filepath.Join(h, "src/lint")
	skill(t, ext, "lint", "Lint")
	ok(t)(InstallSkills(ext, []string{""}, nil))
	kind := func() (string, string) {
		v, _ := Read(nil)
		return v.Skills[0].Kind, v.Skills[0].Source
	}
	if k, src := kind(); k != "folder" || src != ext {
		t.Errorf("linked: %q %q", k, src)
	}
	// the folder moved in by hand in place of the link
	os.Remove(skillDir("lint"))
	if err := os.Rename(ext, skillDir("lint")); err != nil {
		t.Fatal(err)
	}
	if k, src := kind(); k != "" || src != "" {
		t.Errorf("moved in: %q %q", k, src)
	}
	ok(t)(RemoveSkill("lint"))
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "skills", "lint", "SKILL.md")); len(m) != 1 {
		t.Errorf("not kept with the backups: %v", m)
	}
}
