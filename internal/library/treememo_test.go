package library

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// age sets every entry under dir to have been written an hour ago, as
// skills installed some other day are.
func age(t *testing.T, dir string) {
	t.Helper()
	then := time.Now().Add(-time.Hour)
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			if d.IsDir() {
				dirs = append(dirs, p)
			} else if err := os.Chtimes(p, then, then); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	// a folder's time is its own, set last, deepest first
	for _, p := range slices.Backward(dirs) {
		if err := os.Chtimes(p, then, then); err != nil {
			t.Fatal(err)
		}
	}
}

// hide makes every file under dir but its SKILL.md (which says what the
// skill is) unreadable: a page read that opened one again couldn't tell
// what it holds.
func hide(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() != "SKILL.md" {
			fi, _ := d.Info()
			os.Chmod(p, 0)
			os.Chtimes(p, fi.ModTime(), fi.ModTime())
		}
		return nil
	})
	t.Cleanup(func() {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				os.Chmod(p, 0o644)
			}
			return nil
		})
	})
}

func foundIn(t *testing.T, name string) FoundSkill {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range v.FoundSkills {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("%s not found: %+v", name, v.FoundSkills)
	return FoundSkill{}
}

// The Library page compared an agent's own skill with the same name in
// another agent byte by byte on every read, and with big skills in many
// agents that took minutes each time the page opened (0day404, #541). A
// read of folders unchanged since the last one doesn't open their files
// again, and says what it said then; a folder written since is read again.
func TestPageReadDoesntReadUnchangedSkillsAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file can't be made unreadable with a mode on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	h := sandbox(t)
	big := strings.Repeat("template ", 4096)
	for _, d := range []string{".claude/skills/deck", ".codex/skills/deck"} {
		skill(t, filepath.Join(h, d), "deck", "Slides")
		write(t, filepath.Join(h, d, "assets/template.txt"), big)
		age(t, filepath.Join(h, d))
	}
	codex := filepath.Join(h, ".codex/skills/deck")
	if f := foundIn(t, "deck"); !slices.Contains(f.Copies, "codex") {
		t.Fatalf("codex's deck holds the same files as Claude Code's: %+v", f)
	}
	hide(t, codex)
	if f := foundIn(t, "deck"); !slices.Contains(f.Copies, "codex") {
		t.Fatalf("a second read opened codex's files again: %+v", f)
	}

	// written since: read again, and found to differ
	os.Chmod(filepath.Join(codex, "SKILL.md"), 0o644)
	os.Chmod(filepath.Join(codex, "assets/template.txt"), 0o644)
	write(t, filepath.Join(codex, "assets/template.txt"), strings.Replace(big, "template", "Template", 1))
	if f := foundIn(t, "deck"); slices.Contains(f.Copies, "codex") || !slices.Contains(f.Others, "codex") {
		t.Fatalf("codex's deck was edited: %+v", f)
	}
}

// What was found of a folder written a moment ago isn't kept: a file
// written again within the time its file system tells apart could keep
// its size and time.
func TestJustWrittenSkillIsReadAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file can't be made unreadable with a mode on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	h := sandbox(t)
	for _, d := range []string{".claude/skills/deck", ".codex/skills/deck"} {
		skill(t, filepath.Join(h, d), "deck", "Slides")
	}
	if f := foundIn(t, "deck"); !slices.Contains(f.Copies, "codex") {
		t.Fatalf("same files: %+v", f)
	}
	hide(t, filepath.Join(h, ".codex/skills/deck"))
	if f := foundIn(t, "deck"); slices.Contains(f.Copies, "codex") {
		t.Fatalf("a folder written a moment ago was taken as read: %+v", f)
	}
}

// A folder that couldn't be read whole isn't remembered as different:
// once it can be read, it is compared again.
func TestUnreadableSkillIsntRememberedAsDifferent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file can't be made unreadable with a mode on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	h := sandbox(t)
	for _, d := range []string{".claude/skills/deck", ".codex/skills/deck"} {
		skill(t, filepath.Join(h, d), "deck", "Slides")
		age(t, filepath.Join(h, d))
	}
	// not SKILL.md: a skill whose SKILL.md can't be read isn't listed at all
	run := filepath.Join(h, ".codex/skills/deck/scripts/run.sh")
	fi, _ := os.Stat(run)
	os.Chmod(run, 0)
	os.Chtimes(run, fi.ModTime(), fi.ModTime())
	if f := foundIn(t, "deck"); slices.Contains(f.Copies, "codex") {
		t.Fatalf("an unreadable folder was taken as the same: %+v", f)
	}
	os.Chmod(run, 0o644)
	if f := foundIn(t, "deck"); !slices.Contains(f.Copies, "codex") {
		t.Fatalf("readable again, it's the same: %+v", f)
	}
}

// A library skill edited some time ago, after a read that hashed it, is
// hashed again: its copies are said to be behind it, though every file
// of it was written long before the page is read.
func TestEditedLibrarySkillIsHashedAgain(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex"}))
	ok(t)(SetSkillHow("", HowCopy))
	age(t, h)
	v, err := Read(nil)
	if err != nil || v.Skills[0].Behind != nil {
		t.Fatalf("behind at once: %+v %v", v.Skills, err)
	}
	// the same size, an hour ago: only the time tells it changed
	md := filepath.Join(skillDir("pdf"), "SKILL.md")
	write(t, md, strings.Replace(read(t, md), "Read PDFs", "Read PDFS", 1))
	age(t, skillDir("pdf"))
	if v, _ = Read(nil); !slices.Equal(v.Skills[0].Behind, []string{"claude", "codex"}) {
		t.Fatalf("the library's skill changed, and its copies aren't behind: %v", v.Skills[0].Behind)
	}
}

// What hashFiles says of a folder is what it said before the memo: the
// hashes are kept in the library and in each copy's mark.
func TestHashUnchangedByTheMemo(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "SKILL.md"), "---\nname: x\n---\n")
	write(t, filepath.Join(d, "scripts/run.sh"), "echo hi\n")
	write(t, filepath.Join(d, ".DS_Store"), "finder")
	write(t, filepath.Join(d, ".git/HEAD"), "ref")
	if err := os.Symlink("scripts/run.sh", filepath.Join(d, "run")); err != nil {
		t.Skip(err)
	}
	sum := func() string {
		x := sha256.New()
		fmt.Fprintf(x, "file SKILL.md 16\n---\nname: x\n---\nlink run scripts/run.sh\nfile scripts/run.sh 8\necho hi\n")
		return hex.EncodeToString(x.Sum(nil))
	}
	for _, nf := range []bool{false, true} {
		if got := hashFiles(d, nf); got != sum() {
			t.Errorf("noFinder %v: %s, want %s", nf, got, sum())
		}
	}
}
