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

// #896: skills are given as links or as copies, for every agent or one;
// a change of way makes each again, a copy behind the library's is said
// and made again by a sync, and the user's own folders are never touched.
func TestSkillHow(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	// the user's own skill, which the library has nothing to do with
	skill(t, filepath.Join(h, ".claude/skills/mine"), "mine", "Mine")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex"}))
	cl, cx := filepath.Join(h, ".claude/skills/pdf"), filepath.Join(h, ".codex/skills/pdf")
	if !isLink(t, cl) || !isLink(t, cx) {
		t.Fatal("not linked at first")
	}

	if _, err := SetSkillHow("", "both"); err == nil {
		t.Error("an unknown way was taken")
	}
	if _, err := SetSkillHow("", ""); err == nil {
		t.Error("the library was given no way")
	}

	// copies for every agent
	ok(t)(SetSkillHow("", HowCopy))
	for _, p := range []string{cl, cx} {
		if isLink(t, p) {
			t.Fatalf("%s is still a link", p)
		}
		if read(t, filepath.Join(p, "SKILL.md")) != read(t, filepath.Join(src, "pdf/SKILL.md")) {
			t.Errorf("%s: not a copy of the skill", p)
		}
		if !ours(p, "pdf") {
			t.Errorf("%s: not marked magpie's", p)
		}
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.CopySkills {
		t.Error("the page doesn't say copies")
	}
	for _, a := range v.Agents {
		if (a.ID == "claude" || a.ID == "codex") && (a.How != HowCopy || a.HowOwn) {
			t.Errorf("%s: how %q own %v", a.ID, a.How, a.HowOwn)
		}
	}
	if len(v.Skills) != 1 || v.Skills[0].Behind != nil {
		t.Fatalf("behind at once: %+v", v.Skills)
	}

	// the library's skill changes (a folder edited by hand): the copies
	// are behind till a sync makes them again
	write(t, filepath.Join(src, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Changed\n---\n")
	v, _ = Read(nil)
	if b := v.Skills[0].Behind; !slices.Equal(b, []string{"claude", "codex"}) {
		t.Fatalf("behind: %v", b)
	}
	ok(t)(Sync())
	if !strings.Contains(read(t, filepath.Join(cl, "SKILL.md")), "Changed") {
		t.Error("the sync didn't copy it again")
	}
	if v, _ = Read(nil); v.Skills[0].Behind != nil {
		t.Errorf("still behind: %v", v.Skills[0].Behind)
	}

	// one agent its own way
	ok(t)(SetSkillHow("codex", HowLink))
	if !isLink(t, cx) || isLink(t, cl) {
		t.Fatal("codex isn't a link again, or claude's copy went")
	}
	v, _ = Read(nil)
	for _, a := range v.Agents {
		if a.ID == "codex" && (a.How != HowLink || !a.HowOwn) {
			t.Errorf("codex: how %q own %v", a.How, a.HowOwn)
		}
	}
	if _, err := SetSkillHow("no such/agent", HowCopy); err == nil {
		t.Error("a malformed agent id was given a way")
	}

	// an edit made in claude's copy isn't lost when it is a link again:
	// the library takes it (takeEdits), and the link shows it
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(cl, "notes.md"), "my notes\n")
	ok(t)(SetSkillHow("", HowLink))
	if !isLink(t, cl) {
		t.Fatal("claude isn't a link again")
	}
	if read(t, filepath.Join(src, "pdf/notes.md")) != "my notes\n" || read(t, filepath.Join(cl, "notes.md")) != "my notes\n" {
		t.Error("the edit in the copy wasn't taken into the library")
	}
	// codex goes the library's way again
	ok(t)(SetSkillHow("codex", HowCopy))
	ok(t)(SetSkillHow("codex", ""))
	if !isLink(t, cx) {
		t.Error("codex didn't go back to the library's way")
	}

	// a skill of the user's by the library's name stands, whichever way
	ok(t)(SkillAgents("pdf", []string{"codex"}))
	skill(t, cl, "pdf", "The user's own")
	for _, how := range []string{HowCopy, HowLink} {
		ok(t)(SetSkillHow("", how))
		if isLink(t, cl) || ours(cl, "pdf") || !strings.Contains(read(t, filepath.Join(cl, "SKILL.md")), "The user's own") {
			t.Fatalf("%s: the user's own folder was touched", how)
		}
	}
	if !strings.Contains(read(t, filepath.Join(h, ".claude/skills/mine/SKILL.md")), "Mine") {
		t.Error("the user's own skill was touched")
	}

	// a copy is taken away when the skill is
	ok(t)(SetSkillHow("", HowCopy))
	if isLink(t, cx) {
		t.Fatal("codex isn't a copy")
	}
	ok(t)(RemoveSkill("pdf"))
	if _, err := os.Lstat(cx); !os.IsNotExist(err) {
		t.Error("codex's copy is still there")
	}
	if _, err := os.Stat(filepath.Join(cl, "SKILL.md")); err != nil {
		t.Error("the user's own pdf went with the library's")
	}
}

// Two failed hashes don't prove a copy is unchanged: switching back to a
// link must keep the agent's edits even when neither folder can be read.
func TestSkillRelinkKeepsUnreadableCopy(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix read permissions without root")
	}
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	write(t, filepath.Join(src, "pdf/blocked.txt"), "unchanged\n")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	ok(t)(SetSkillHow("", HowCopy))
	p := filepath.Join(h, ".claude/skills/pdf")
	write(t, filepath.Join(p, "notes.md"), "my notes\n")
	// The library's skill written long ago and hashed, as a run under load
	// finds it past the memo's settleFor: a mode alone doesn't change what
	// the memo stamps, so blocked.txt is written again as it's made
	// unreadable, as an edit that leaves it so would.
	age(t, src)
	hashDir(realDir(skillDir("pdf")))
	for _, dir := range []string{filepath.Join(src, "pdf"), p} {
		f := filepath.Join(dir, "blocked.txt")
		if err := os.Chmod(f, 0); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if err := os.Chtimes(f, now, now); err != nil {
			t.Fatal(err)
		}
		if hashDir(realDir(dir)) != "" {
			t.Fatal("the unreadable file didn't prevent hashing", dir)
		}
	}

	ok(t)(SetSkillHow("", HowLink))
	if !isLink(t, p) {
		t.Fatal("claude's copy wasn't replaced by a link")
	}
	var kept []string
	filepath.WalkDir(BackupDir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "notes.md" {
			kept = append(kept, p)
		}
		return nil
	})
	if len(kept) != 1 || read(t, kept[0]) != "my notes\n" {
		t.Fatalf("the unreadable copy's edits weren't kept: %v", kept)
	}
}

// The By agent sheet says why an agent gets copies it didn't ask for
// (4kT1FohCTP97031 on X read Claude Desktop's bare "Only ever takes
// copies" as Claude Code's): Claude Code takes links as every agent
// does, Claude Desktop only copies, for Cowork's reason, and an agent
// reading the very same folder as one given copies gets copies too, and
// says so, rather than claim links.
func TestSkillHowSaysWhyCopies(t *testing.T) {
	h := sandbox(t)
	p, _ := desktopFiles(t, h)
	write(t, p, `{}`)
	desktopAccount(t, filepath.Dir(p), "org1", "acct1", `{"lastUpdated": "2026-10-01T00:00:00.000Z", "skills": []}`)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "claude-desktop"}))

	how := func() map[string]AgentView {
		t.Helper()
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]AgentView{}
		for _, a := range v.Agents {
			m[a.ID] = a
		}
		return m
	}
	m := how()
	if a := m["claude"]; a.How != HowLink || a.MustCopy || a.CopyWhy != "" {
		t.Errorf("Claude Code: how %q, must copy %v, why %q; it takes links", a.How, a.MustCopy, a.CopyWhy)
	}
	if !isLink(t, filepath.Join(h, ".claude/skills/pdf")) {
		t.Error("Claude Code's skill isn't a link")
	}
	if a := m["claude-desktop"]; a.How != HowCopy || !a.MustCopy || a.CopyWhy != copyDesktop {
		t.Errorf("Claude Desktop: how %q, must copy %v, why %q", a.How, a.MustCopy, a.CopyWhy)
	}

	// Codex reads Claude Code's very folder and is to have copies: both
	// get copies, and Claude Code's row says whose
	cx := filepath.Join(h, ".codex/skills")
	os.RemoveAll(cx)
	os.MkdirAll(filepath.Dir(cx), 0o755)
	if err := os.Symlink(filepath.Join(h, ".claude/skills"), cx); err != nil {
		t.Skipf("no symlink here: %v", err)
	}
	ok(t)(SetSkillHow("codex", HowCopy))
	if isLink(t, filepath.Join(h, ".claude/skills/pdf")) {
		t.Fatal("the shared folder kept a link while Codex is to have copies")
	}
	m = how()
	if a := m["claude"]; a.How != HowCopy || a.MustCopy || a.CopyWhy != copyShared || !slices.Equal(a.CopyWith, []string{"Codex"}) {
		t.Errorf("Claude Code: how %q, must copy %v, why %q with %v; it gets copies for Codex", a.How, a.MustCopy, a.CopyWhy, a.CopyWith)
	}
	if a := m["codex"]; a.How != HowCopy || a.CopyWhy != "" {
		t.Errorf("Codex: how %q, why %q; copies are its own way", a.How, a.CopyWhy)
	}

	// Codex back to links: Claude Code's folder is links again
	ok(t)(SetSkillHow("codex", ""))
	if !isLink(t, filepath.Join(h, ".claude/skills/pdf")) {
		t.Error("not a link again")
	}
	if a := how()["claude"]; a.How != HowLink || a.CopyWhy != "" {
		t.Errorf("Claude Code after: how %q, why %q", a.How, a.CopyWhy)
	}
}
