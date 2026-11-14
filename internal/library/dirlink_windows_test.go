//go:build windows

package library

import (
	"os"
	"path/filepath"
	"testing"
)

// #973: a junction, what Windows links with when symlinks aren't allowed,
// is a link to the library's skill: magpie's own, and taking it away
// leaves the skill where it is.
func TestJunctionIsALink(t *testing.T) {
	h := sandbox(t)
	skill(t, skillDir("pdf"), "pdf", "Read PDFs")
	p := filepath.Join(h, ".claude/skills/pdf")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := junction(skillDir("pdf"), p); err != nil {
		t.Fatal(err)
	}
	if to, ok := linkTarget(p); !ok || filepath.Clean(to) != filepath.Clean(skillDir("pdf")) {
		t.Errorf("link target %q %v", to, ok)
	}
	if !ours(p, "pdf") {
		t.Error("magpie's junction isn't its own")
	}
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		t.Errorf("the skill isn't read through the junction: %v", err)
	}
	if err := unlink(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("the junction is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir("pdf"), "SKILL.md")); err != nil {
		t.Errorf("taking the junction away took the skill: %v", err)
	}
	// a junction can't point at a share; nothing is left behind
	q := filepath.Join(h, "share")
	if junction(`\\server\share\pdf`, q) == nil {
		t.Error("a junction to a share")
	}
	if _, err := os.Lstat(q); !os.IsNotExist(err) {
		t.Errorf("a failed junction left %s: %v", q, err)
	}
}

// #6610: Antigravity skips a junction, so its junction from an older
// magpie is made a copy, a real folder (not ModeIrregular to a Go walk),
// in its own folder and in a project's .agents/skills.
func TestJunctionBlindJunctionBecomesCopy(t *testing.T) {
	h, cfg := antigravity(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"agy"}))
	proj := filepath.Join(t.TempDir(), "app")
	write(t, filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main\n")
	ok(t)(AddProject(proj))
	ok(t)(ProjectSkill(proj, "pdf", []string{"agy"}))
	ps := []string{filepath.Join(cfg, "skills/pdf"), filepath.Join(proj, ".agents/skills/pdf")}
	for _, p := range ps {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := junction(skillDir("pdf"), p); err != nil {
			t.Fatal(err)
		}
	}
	ok(t)(Sync())
	for _, p := range ps {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode()&(os.ModeIrregular|os.ModeSymlink) != 0 || !ours(p, "pdf") {
			t.Errorf("%s: %v %v", p, fi, err)
			continue
		}
		es, err := os.ReadDir(filepath.Dir(p))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.Name() == "pdf" && !e.IsDir() {
				t.Errorf("%s: a walk doesn't see a folder: %v", p, e.Type())
			}
		}
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Error(err)
		}
	}
}
