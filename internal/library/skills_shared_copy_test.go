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

			// the user's copy changes: it is another skill by that name,
			// and the library's is given again
			write(t, filepath.Join(shared, "SKILL.md"), "---\nname: hunk-review\ndescription: Mine\n---\n")
			ok(t)(Sync())
			for _, p := range []string{pi, cx} {
				if !ours(p, "hunk-review") {
					t.Errorf("%s hasn't the library's again", p)
				}
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
