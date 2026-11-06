package library

import (
	"os"
	"path/filepath"
	"testing"
)

// math_milian: a skill given to every agent was in ~/.agents/skills (the
// user's own copy) and in ~/.pi/agent/skills (magpie's link to the
// library's own copy of the same files). Pi reads both folders and drops
// a skill read twice only when both paths are one file (realpath,
// dist/core/skills.js), so it warned "hunk-review" collision. An agent
// that reads ~/.agents/skills itself gets no second tree there: magpie's
// link or copy in its own folder goes, the user's copy stays as it is.
func TestSharedCopyNotGivenTwice(t *testing.T) {
	for _, how := range []string{HowLink, HowCopy} {
		t.Run(how, func(t *testing.T) {
			h := sandbox(t)
			// no agent whose folder ~/.agents/skills is (Goose)
			os.RemoveAll(filepath.Join(h, ".config/goose"))
			os.Remove(filepath.Join(h, "bin/goose"))
			os.Remove(filepath.Join(h, "bin/goose.exe"))
			setPiVersion(t, "1.4.0")
			src := filepath.Join(h, "src/skills")
			skill(t, filepath.Join(src, "hunk-review"), "hunk-review", "Review hunks")
			pi := filepath.Join(h, ".pi/agent/skills/hunk-review")
			cx := filepath.Join(h, ".codex/skills/hunk-review")
			cl := filepath.Join(h, ".claude/skills/hunk-review")

			ok(t)(SetSkillHow("", how))
			ok(t)(InstallSkills(src, []string{"hunk-review"}, []string{"claude", "codex", "pi"}))
			for _, p := range []string{pi, cx, cl} {
				if !ours(p, "hunk-review") {
					t.Fatalf("%s isn't given it before the shared copy", p)
				}
			}

			// the user's own copy of the same files in ~/.agents/skills
			shared := filepath.Join(h, ".agents/skills/hunk-review")
			skill(t, shared, "hunk-review", "Review hunks")
			ok(t)(Sync())
			for _, p := range []string{pi, cx} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Errorf("%s still has a second copy: %v", p, err)
				}
			}
			if !ours(cl, "hunk-review") {
				t.Error("claude reads no ~/.agents/skills, yet lost it")
			}
			if ours(shared, "hunk-review") || !exists(filepath.Join(shared, "SKILL.md")) || !exists(filepath.Join(shared, "scripts/run.sh")) {
				t.Error("the user's copy in ~/.agents/skills was touched")
			}

			// the user's copy changes: it is a skill of each reader's own by
			// that name, which the user settles; a second one beside it is
			// still the skill twice
			write(t, filepath.Join(shared, "SKILL.md"), "---\nname: hunk-review\ndescription: Mine\n---\n")
			r, err := Sync()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{pi, cx} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Errorf("%s has the library's beside the user's own: %v", p, err)
				}
			}
			for _, id := range []string{"pi", "codex"} {
				if !ownProblem(r, id, "hunk-review") {
					t.Errorf("%s isn't said to have its own hunk-review: %+v", id, r.Problems)
				}
			}
			if ownProblem(r, "claude", "hunk-review") || !ours(cl, "hunk-review") {
				t.Error("claude reads no ~/.agents/skills, yet was touched")
			}
			if read(t, filepath.Join(shared, "SKILL.md")) != "---\nname: hunk-review\ndescription: Mine\n---\n" {
				t.Error("the user's changed copy was touched")
			}
		})
	}
}

// Pi before 0.54.0 reads its own folder only: it keeps its link.
func TestSharedCopyOldPiKeepsItsOwn(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	setPiVersion(t, "0.53.0")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "hunk-review"), "hunk-review", "Review hunks")
	skill(t, filepath.Join(h, ".agents/skills/hunk-review"), "hunk-review", "Review hunks")
	ok(t)(InstallSkills(src, []string{"hunk-review"}, []string{"pi"}))
	if !ours(filepath.Join(h, ".pi/agent/skills/hunk-review"), "hunk-review") {
		t.Error("pi 0.53 lost its own link")
	}
}

func ownProblem(r *Result, agent, name string) bool {
	for _, p := range r.Problems {
		if p.Agent == agent && p.What == "skill:"+name && p.Own {
			return true
		}
	}
	return false
}

// The user's own copy kept in ~/.agents/skills under another folder name
// (hunk-review-main, as a repository's checkout names it): Pi, Codex and
// Gemini CLI know a skill by its SKILL.md's name, so the library's in
// their own folder too is the skill twice. A magpie link left there by an
// older magpie goes on the next sync.
func TestSharedCopyOtherFolderName(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	setPiVersion(t, "1.4.0")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "hunk-review"), "hunk-review", "Review hunks")
	ok(t)(InstallSkills(src, []string{"hunk-review"}, []string{"claude", "codex", "pi", "gemini"}))
	pi := filepath.Join(h, ".pi/agent/skills/hunk-review")
	if !ours(pi, "hunk-review") {
		t.Fatal("pi isn't given it")
	}
	shared := filepath.Join(h, ".agents/skills/hunk-review-main")
	skill(t, shared, "hunk-review", "Review hunks")
	ok(t)(Sync())
	for _, d := range []string{".pi/agent/skills", ".codex/skills", ".gemini/skills"} {
		if _, err := os.Lstat(filepath.Join(h, d, "hunk-review")); !os.IsNotExist(err) {
			t.Errorf("%s still has a second one: %v", d, err)
		}
	}
	if !ours(filepath.Join(h, ".claude/skills/hunk-review"), "hunk-review") {
		t.Error("claude lost it")
	}
	if !exists(filepath.Join(shared, "SKILL.md")) {
		t.Error("the user's copy was touched")
	}
}

// The user's own hunk-review in ~/.agents/skills differs from the
// library's (the skills CLI updated it): each reader has one of its own,
// and the user settles it. Use the library's sets the shared one aside
// with the backups and gives the library's; Keep leaves the reader the
// user's, with nothing of the library's beside it.
func TestSharedOwnSettled(t *testing.T) {
	for _, how := range []string{"use", "keep"} {
		t.Run(how, func(t *testing.T) {
			h := sandbox(t)
			os.RemoveAll(filepath.Join(h, ".config/goose"))
			os.Remove(filepath.Join(h, "bin/goose"))
			os.Remove(filepath.Join(h, "bin/goose.exe"))
			setPiVersion(t, "1.4.0")
			src := filepath.Join(h, "src/skills")
			skill(t, filepath.Join(src, "hunk-review"), "hunk-review", "Review hunks")
			shared := filepath.Join(h, ".agents/skills/hunk-review")
			skill(t, shared, "hunk-review", "Review hunks, older")
			r, err := InstallSkills(src, []string{"hunk-review"}, []string{"codex", "pi"})
			if err != nil {
				t.Fatal(err)
			}
			if !ownProblem(r, "pi", "hunk-review") || !ownProblem(r, "codex", "hunk-review") {
				t.Fatalf("not said: %+v", r.Problems)
			}
			pi := filepath.Join(h, ".pi/agent/skills/hunk-review")
			if _, err := os.Lstat(pi); !os.IsNotExist(err) {
				t.Fatalf("pi got a second one: %v", err)
			}
			if how == "keep" {
				r, err := KeepAgentSkill("hunk-review", "pi")
				if err != nil {
					t.Fatal(err)
				}
				if ownProblem(r, "pi", "hunk-review") {
					t.Error("pi still asked after keeping its own")
				}
				if _, err := os.Lstat(pi); !os.IsNotExist(err) || !exists(filepath.Join(shared, "SKILL.md")) {
					t.Error("keeping its own changed something")
				}
				return
			}
			r, err = UseLibrarySkill("hunk-review", "pi")
			if err != nil {
				t.Fatal(err)
			}
			if ownProblem(r, "pi", "hunk-review") || ownProblem(r, "codex", "hunk-review") {
				t.Errorf("still asked: %+v", r.Problems)
			}
			if _, err := os.Lstat(shared); !os.IsNotExist(err) {
				t.Error("the user's own wasn't set aside")
			}
			for _, p := range []string{pi, filepath.Join(h, ".codex/skills/hunk-review")} {
				if !ours(p, "hunk-review") {
					t.Errorf("%s hasn't the library's", p)
				}
			}
		})
	}
}
