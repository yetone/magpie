package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// refeccd on Discord: skills kept in ~/.agents/skills, handed over to
// magpie, could no longer be edited where they had been. A file saved
// from the page goes into the library's folder, reaches the agent linked
// to it and the one given a copy, and stays through the syncs after.
func TestSavedSkillFileReachesEveryAgent(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "writer"), "writer", "Write well")
	write(t, filepath.Join(shared, "writer/refs/style.md"), "# Style\n")
	write(t, filepath.Join(shared, "writer/logo.png"), "\x89PNG\r\n\x1a\n\x00\x00")
	ok(t)(ImportSkill("writer"))
	ok(t)(HandOverSkills([]string{"writer"}))
	ok(t)(SetSkillHow("codex", HowCopy))
	ok(t)(SetSkillHow("gemini", HowLink))
	cx, gm := filepath.Join(h, ".codex/skills/writer"), filepath.Join(h, ".gemini/skills/writer")
	if !ours(cx, "writer") || isLink(t, cx) || !isLink(t, gm) {
		t.Fatal("codex hasn't a copy, or gemini a link")
	}

	f, err := ReadSkillFile("writer", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.File != "SKILL.md" || !strings.Contains(f.Text, "description: Write well") {
		t.Fatalf("read: %+v", f)
	}
	if want := []string{"SKILL.md", "refs/style.md", "scripts/run.sh"}; !slices.Equal(f.Files, want) {
		t.Fatalf("files %v, want %v (the picture left out)", f.Files, want)
	}

	time.Sleep(20 * time.Millisecond)
	edited := strings.Replace(f.Text, "Write well", "Write well, briefly", 1)
	ok(t)(SaveSkillFile("writer", "SKILL.md", edited, f.Text))
	for range 2 { // the save's sync, and the one of the next start
		for _, p := range []string{skillDir("writer"), cx, gm} {
			if s := read(t, filepath.Join(p, "SKILL.md")); s != edited {
				t.Fatalf("%s:\n%s", p, s)
			}
		}
		if !ours(cx, "writer") {
			t.Fatal("codex's copy isn't magpie's any more")
		}
		ok(t)(Sync())
	}

	// another file of it
	g, err := ReadSkillFile("writer", "refs/style.md")
	if err != nil || g.Text != "# Style\n" {
		t.Fatalf("refs/style.md: %+v %v", g, err)
	}
	ok(t)(SaveSkillFile("writer", "refs/style.md", "# Style\n\nShort.\n", g.Text))
	if s := read(t, filepath.Join(cx, "refs/style.md")); s != "# Style\n\nShort.\n" {
		t.Fatalf("codex's refs/style.md: %q", s)
	}
}

// What the page opened has changed since (an agent's edit taken in, an
// update): the save writes nothing over it.
func TestSkillFileSaveRefusesAChangedFile(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	f, err := ReadSkillFile("pdf", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	now := strings.Replace(f.Text, "Read PDFs", "Changed meanwhile", 1)
	write(t, filepath.Join(skillDir("pdf"), "SKILL.md"), now)
	if _, err := SaveSkillFile("pdf", "SKILL.md", "mine", f.Text); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("saved over a changed file: %v", err)
	}
	if s := read(t, filepath.Join(skillDir("pdf"), "SKILL.md")); s != now {
		t.Fatalf("written over: %q", s)
	}
	if _, err := SaveSkillFile("nope", "SKILL.md", "x", ""); err == nil {
		t.Fatal("saved a skill the library hasn't")
	}
}

// Only the skill's own files: no way out of its folder, through .. or a
// link, and none of what magpie leaves aside.
func TestSkillFileStaysInTheSkill(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	write(t, filepath.Join(h, "secret.txt"), "key=1\n")
	write(t, filepath.Join(h, "outside/notes.md"), "out\n")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	dir := realDir(skillDir("pdf"))
	symlink(t, filepath.Join(h, "secret.txt"), filepath.Join(dir, "link.txt"))
	symlink(t, filepath.Join(h, "outside"), filepath.Join(dir, "out"))
	write(t, filepath.Join(dir, "node_modules/x/README.md"), "dep\n")
	for _, bad := range []string{"../../secret.txt", "/etc/hosts", "link.txt", "out/notes.md", "node_modules/x/README.md", ".magpie", "scripts", ""} {
		if _, err := SaveSkillFile("pdf", bad, "x", ""); err == nil {
			t.Errorf("saved %q", bad)
		}
		if bad != "" {
			if _, err := ReadSkillFile("pdf", bad); err == nil {
				t.Errorf("read %q", bad)
			}
		}
	}
	if s := read(t, filepath.Join(h, "secret.txt")); s != "key=1\n" {
		t.Fatalf("written outside: %q", s)
	}
	f, err := ReadSkillFile("pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"SKILL.md", "scripts/run.sh"}; !slices.Equal(f.Files, want) {
		t.Fatalf("files %v, want %v", f.Files, want)
	}
}

// A file written with Windows line ends keeps them: the page's text has
// \n alone.
func TestSkillFileKeepsLineEnds(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	write(t, filepath.Join(src, "crlf/SKILL.md"), "---\r\nname: crlf\r\ndescription: CRLF\r\n---\r\n")
	ok(t)(InstallSkills(src, []string{"crlf"}, []string{"claude"}))
	f, err := ReadSkillFile("crlf", "")
	if err != nil {
		t.Fatal(err)
	}
	page := strings.ReplaceAll(f.Text, "\r\n", "\n") + "More.\n"
	ok(t)(SaveSkillFile("crlf", "SKILL.md", page, f.Text))
	if s := read(t, filepath.Join(skillDir("crlf"), "SKILL.md")); s != f.Text+"More.\r\n" {
		t.Fatalf("%q", s)
	}
	if _, err := os.Stat(filepath.Join(skillDir("crlf"), "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
