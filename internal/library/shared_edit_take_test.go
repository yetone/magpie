package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An edit made in the shared copy — ~/.agents/skills where no link could
// be made (Windows without the right to) — is taken into the library as
// one in an agent's copy is, before the copy is replaced or set aside
// (#4958): syncShared finds the stale copy only after takeEdits has had
// its say.
func TestEditInSharedCopyTaken(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills")
	pdf := filepath.Join(shared, "pdf")
	lib := filepath.Join(src, "pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	ok(t)(SetSkillsWhere(SkillsInShared))
	if !linked(pdf) {
		t.Fatal("pdf isn't a link in ~/.agents/skills")
	}
	// where no link could be made, the entry is a marked copy instead:
	// what copyIn puts in a link's place
	if err := copyIn(pdf, "pdf"); err != nil {
		t.Fatal(err)
	}
	if linked(pdf) || !ours(pdf, "pdf") {
		t.Fatal("pdf isn't magpie's marked copy")
	}
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(pdf, "notes.md"), "my notes\n")

	ok(t)(Sync())
	if read(t, filepath.Join(lib, "notes.md")) != "my notes\n" {
		t.Error("the edit in the shared copy wasn't taken into the library")
	}
	// the entry is still magpie's, saying the library's skill: taken or
	// relinked, the edit is not only in the backups
	if !ours(pdf, "pdf") {
		t.Fatal("the shared entry is no longer magpie's")
	}
	if _, err := os.Stat(filepath.Join(pdf, "notes.md")); err != nil {
		t.Error("the shared entry lost the edit")
	}
}
