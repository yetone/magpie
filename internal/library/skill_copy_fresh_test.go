package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// blockSkillSubtree makes dir unreadable, and restores it wherever a broken
// implementation moved it within root so TempDir can clean it up.
func blockSkillSubtree(t *testing.T, root, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix read permissions without root")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if d != nil && d.IsDir() && d.Name() == filepath.Base(dir) {
				return os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	if _, err := os.ReadDir(dir); !os.IsPermission(err) {
		t.Fatalf("subtree is not unreadable: %v", err)
	}
}

// An unreadable subtree hides edits from the mark's timestamp check. Neither
// relinking, refreshing nor removing a duplicate copy may take those edits away.
func TestSkillCopyScanErrorPreservesEdits(t *testing.T) {
	for _, how := range []string{HowLink, HowCopy, "shared"} {
		t.Run(how, func(t *testing.T) {
			h := sandbox(t)
			id := "claude"
			other := "codex"
			if how == "shared" {
				id = "codex"
				other = "claude"
				write(t, filepath.Join(h, ".kimi/config.toml"), "")
			}
			src := filepath.Join(h, "src/skills")
			skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
			ok(t)(SetSkillHow("", HowCopy))
			ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex"}))
			p := filepath.Join(h, "."+id, "skills/pdf")
			original := read(t, filepath.Join(p, "SKILL.md"))
			edited := filepath.Join(p, "scripts/run.sh")
			write(t, edited, "echo mine\n")
			made, marked := copiedAt(p)
			if !marked {
				t.Fatal("copy has no mark")
			}
			later := made.Add(time.Minute)
			if err := os.Chtimes(edited, later, later); err != nil {
				t.Fatal(err)
			}
			if how == HowCopy {
				write(t, filepath.Join(src, "pdf/scripts/run.sh"), "echo library update\n")
			}
			blocked := filepath.Join(p, "scripts")
			blockSkillSubtree(t, h, blocked)
			var res *Result
			var err error
			if how == "shared" {
				// Kimi gives it to ~/.agents/skills, so Codex no longer needs
				// its own copy. That copy still has edits to take first.
				res, err = SkillAgents("pdf", []string{"claude", "codex", "kimi"})
			} else {
				res, err = SetSkillHow(id, how)
			}
			if err != nil {
				t.Fatal(err)
			}
			if linked(p) {
				t.Fatal("unreadable copy was replaced by a link")
			}
			if got := read(t, filepath.Join(p, "SKILL.md")); got != original {
				t.Fatalf("unreadable copy was partly removed: %q", got)
			}
			if len(res.Problems) != 1 || res.Problems[0].Agent != id || res.Problems[0].What != "skill:pdf" || !strings.Contains(res.Problems[0].Error, blocked) {
				t.Fatalf("unreadable copy wasn't reported: %+v", res.Problems)
			}
			if how == HowCopy && read(t, filepath.Join(h, ".codex/skills/pdf/scripts/run.sh")) != "echo library update\n" {
				t.Error("one unreadable copy stopped another agent's update")
			}
			if err := os.Chmod(blocked, 0o755); err != nil {
				t.Fatal(err)
			}
			if got := read(t, edited); got != "echo mine\n" {
				t.Fatalf("unreadable copy's edit was replaced: %q", got)
			}
			ok(t)(Sync())
			if how == "shared" {
				edited = filepath.Join(sharedSkillsDir(), "pdf/scripts/run.sh")
			}
			for _, f := range []string{edited, filepath.Join(src, "pdf/scripts/run.sh"), filepath.Join(h, "."+other, "skills/pdf/scripts/run.sh")} {
				if got := read(t, f); got != "echo mine\n" {
					t.Errorf("edit wasn't kept after permissions returned at %s: %q", f, got)
				}
			}
		})
	}
}

// #1031: switching a group of skills on for one agent made the sync read
// every file of every copy every agent has, which over a WSL distro's
// share took 10 seconds and more. A copy unchanged since magpie made it is
// known fresh by its mark, without its files being read: here one of them
// can't be read at all, and the sync leaves it as it is.
func TestSyncDoesNotReadUnchangedCopies(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a file that can't be read needs Unix permissions and no root")
	}
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	for _, n := range []string{"pdf", "xlsx", "docx"} {
		skill(t, filepath.Join(src, n), n, n)
	}
	ok(t)(SetSkillHow("", HowCopy))
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx", "docx"}, []string{"claude"}))
	ok(t)(SkillAgents("docx", []string{}))
	f := filepath.Join(h, ".codex/skills/pdf/scripts/run.sh")
	ok(t)(SkillAgents("pdf", []string{"claude", "codex"}))
	// unreadable, with its time as it was: the copy is as magpie made it
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f, 0o644) })

	res := ok(t)(SomeSkillsAgents([]string{"xlsx", "docx"}, []string{"codex"}, true))
	if !slices.Contains(res.Changed, "codex") {
		t.Errorf("codex wasn't given the group: %v", res.Changed)
	}
	if fi, err := os.Stat(f); err != nil {
		t.Errorf("codex's copy of pdf is gone: %v", err)
	} else if fi.Mode().Perm() != 0 {
		t.Errorf("codex's copy of pdf was read and made again (mode %v)", fi.Mode().Perm())
	}
	for _, n := range []string{"xlsx", "docx"} {
		if !ours(filepath.Join(h, ".codex/skills", n), n) {
			t.Errorf("codex has no copy of %s", n)
		}
	}
}

// A copy is still made again when the library's skill changes, and an
// edit in an agent's copy is still taken, whether its mark says what was
// copied or is one from before marks did (no hash in it).
func TestCopyFreshByMark(t *testing.T) {
	h := sandbox(t)
	fresh := func(p string) bool {
		t.Helper()
		current, err := copyIsFresh(p, hashDir(realDir(skillDir("pdf"))), false)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(SetSkillHow("", HowCopy))
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex"}))
	cl, cx := filepath.Join(h, ".claude/skills/pdf"), filepath.Join(h, ".codex/skills/pdf")
	if _, ok := markedHash(cl); !ok {
		t.Fatal("the copy's mark says no hash")
	}
	if !fresh(cl) || !fresh(cx) {
		t.Fatal("a copy just made isn't fresh")
	}
	// a mark from before: no hash
	write(t, filepath.Join(cx, marker), "copied from "+skillDir("pdf")+" by magpie, and copied again when it changes\n")
	if !fresh(cx) {
		t.Error("an old mark's copy, the same as the library's, isn't fresh")
	}

	// the library's changes: both copies are behind and made again
	lib := filepath.Join(skillDir("pdf"), "scripts/run.sh")
	write(t, lib, "echo new\n")
	later := time.Now().Add(time.Minute)
	os.Chtimes(lib, later, later)
	if fresh(cl) || fresh(cx) {
		t.Fatal("copies of an older skill are fresh")
	}
	ok(t)(Sync())
	for _, p := range []string{cl, cx} {
		if got := read(t, filepath.Join(p, "scripts/run.sh")); got != "echo new\n" {
			t.Errorf("%s wasn't made again: %q", p, got)
		}
	}

	// an edit in claude's copy is taken into the library, and codex gets it
	edited := filepath.Join(cl, "scripts/run.sh")
	write(t, edited, "echo mine\n")
	later = time.Now().Add(2 * time.Minute)
	os.Chtimes(edited, later, later)
	if fresh(cl) {
		t.Error("an edited copy is fresh")
	}
	ok(t)(Sync())
	if got := read(t, lib); got != "echo mine\n" {
		t.Errorf("the library didn't take the edit: %q", got)
	}
	if got := read(t, filepath.Join(cx, "scripts/run.sh")); got != "echo mine\n" {
		t.Errorf("codex didn't get the edit: %q", got)
	}
}
