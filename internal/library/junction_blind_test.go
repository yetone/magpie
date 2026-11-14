package library

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// onWindowsLinks makes the sync judge links as it does on Windows, where
// magpie's link may be a junction (skipsJunctions).
func onWindowsLinks(t *testing.T) {
	t.Helper()
	old := linkOS
	linkOS = "windows"
	t.Cleanup(func() { linkOS = old })
}

// #6610, #5107: on Windows, Antigravity and Crush skip a junction in their
// skills folder, so they get marked copies, made again when the library's
// changes; an edit made in one goes to the library and to the agents
// given links. Elsewhere they get links.
func TestJunctionBlindAgentsGetCopies(t *testing.T) {
	h, cfg := antigravity(t)
	write(t, filepath.Join(h, ".config/crush/crush.json"), "{}")
	for _, id := range []string{"agy", "crush"} {
		if tg := targetByID(id); tg == nil || tg.Copy != (runtime.GOOS == "windows") {
			t.Fatalf("%s here: %+v", id, tg)
		}
	}
	onWindowsLinks(t)
	for _, id := range []string{"agy", "crush"} {
		if tg := targetByID(id); tg == nil || !tg.Copy {
			t.Fatalf("%s on Windows: %+v", id, tg)
		}
	}
	if tg := targetByID("claude"); tg == nil || tg.Copy {
		t.Fatalf("claude on Windows: %+v", tg)
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"agy", "crush", "claude"}))
	ag, cr, cl := filepath.Join(cfg, "skills/pdf"), filepath.Join(h, ".config/crush/skills/pdf"), filepath.Join(h, ".claude/skills/pdf")
	for _, p := range []string{ag, cr} {
		if isLink(t, p) || !ours(p, "pdf") {
			t.Fatalf("%s isn't magpie's copy", p)
		}
	}
	if runtime.GOOS != "windows" && !isLink(t, cl) {
		t.Fatal("Claude Code's isn't a link")
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if (a.ID == "agy" || a.ID == "crush") && (!a.MustCopy || a.How != HowCopy) {
			t.Errorf("%s: must copy %v, how %q", a.ID, a.MustCopy, a.How)
		}
	}

	// the user edits Antigravity's copy, then syncs (twice: the next start)
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(ag, "SKILL.md"), "---\nname: pdf\ndescription: Edited in Antigravity\n---\n")
	for range 2 {
		ok(t)(Sync())
		for _, p := range []string{ag, cr, cl, filepath.Join(src, "pdf")} {
			if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Edited in Antigravity") {
				t.Fatalf("%s after a sync:\n%s", p, s)
			}
		}
		if isLink(t, ag) || !ours(ag, "pdf") {
			t.Fatal("Antigravity's isn't magpie's copy any more")
		}
	}

	// the library's changes: the copies are behind till a sync
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(src, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Changed\n---\n")
	v, _ = Read(nil)
	if b := v.Skills[0].Behind; !slices.Equal(b, []string{"agy", "crush"}) {
		t.Fatalf("behind: %v", b)
	}
	ok(t)(Sync())
	for _, p := range []string{ag, cr} {
		if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Changed") {
			t.Errorf("%s wasn't copied again:\n%s", p, s)
		}
	}
}

// Crush reads ~/.agents/skills besides its own folder; magpie's link
// there is no skill to it on Windows, so it gets its own copy.
func TestJunctionBlindAgentSkipsSharedLink(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".config/crush/crush.json"), "{}")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(SetSkillsWhere(SkillsInShared))
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"crush"}))
	shared, cr := filepath.Join(sharedSkillsDir(), "pdf"), filepath.Join(h, ".config/crush/skills/pdf")
	if !isLink(t, shared) {
		t.Fatal("no link in ~/.agents/skills")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Lstat(cr); !os.IsNotExist(err) {
			t.Fatalf("Crush got a second one beside ~/.agents/skills: %v", err)
		}
	}
	onWindowsLinks(t)
	ok(t)(Sync())
	if isLink(t, cr) || !ours(cr, "pdf") {
		t.Fatal("Crush has no copy of its own on Windows")
	}
}

// A project's .agents/skills entry that Antigravity reads is a copy on
// Windows, and an edit made in it goes to the library; one no such agent
// reads is a link again.
func TestJunctionBlindProjectCopies(t *testing.T) {
	h, proj := projectLib(t)
	onWindowsLinks(t)
	ok(t)(ProjectSkill(proj, "pdf", []string{"agy", "claude"}))
	sh, cl := filepath.Join(proj, ".agents/skills/pdf"), filepath.Join(proj, ".claude/skills/pdf")
	if isLink(t, sh) || !ours(sh, "pdf") {
		t.Fatal(".agents/skills/pdf isn't magpie's copy")
	}
	if runtime.GOOS != "windows" && !isLink(t, cl) {
		t.Fatal(".claude/skills/pdf isn't a link")
	}

	// the user edits the project's copy, then syncs
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(sh, "SKILL.md"), "---\nname: pdf\ndescription: Edited in the project\n---\n")
	for range 2 {
		ok(t)(Sync())
		for _, p := range []string{sh, cl, filepath.Join(h, "src/pdf")} {
			if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Edited in the project") {
				t.Fatalf("%s after a sync:\n%s", p, s)
			}
		}
	}
	if isLink(t, sh) || !ours(sh, "pdf") {
		t.Fatal("the project's copy isn't magpie's any more")
	}

	// Antigravity unticked: Codex's entry is a link again (on Windows a
	// copy as fresh as the library's stands for one)
	ok(t)(ProjectSkill(proj, "pdf", []string{"codex"}))
	if runtime.GOOS != "windows" && !isLink(t, sh) {
		t.Error(".agents/skills/pdf is still a copy")
	}
}
