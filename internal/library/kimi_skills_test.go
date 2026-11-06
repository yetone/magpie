package library

import (
	"os"
	"path/filepath"
	"testing"
)

// A skill given to the new Kimi Code (2.x) goes into its own folder
// ($KIMI_CODE_HOME/skills, else ~/.kimi-code/skills — its docs,
// MoonshotAI/kimi-code, docs/en/customization/skills.md "Skill
// Locations"), not into ~/.agents/skills: one there reaches every agent
// that reads that folder, whatever the library ticks (#4379). Kimi Code
// reads the shared folder as well as its own, so a skill kept there for
// another agent gets no second link of its own.
func TestKimiCodeSkillsOwnFolder(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi-code/config.toml"), "")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	own := filepath.Join(h, ".kimi-code/skills/pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"kimi"}))
	if !ours(own, "pdf") {
		t.Error("Kimi Code hasn't the skill in ~/.kimi-code/skills")
	}
	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Error("the skill sits in ~/.agents/skills, which every reader reads")
	}

	// a skill Goose has too stays one entry in the shared folder, which
	// Kimi Code reads where it is: no second link of it in Kimi Code's own
	skill(t, filepath.Join(src, "orca"), "orca", "Orca")
	ok(t)(InstallSkills(src, []string{"orca"}, []string{"goose", "kimi"}))
	if !ours(filepath.Join(h, ".agents/skills/orca"), "orca") {
		t.Error("goose hasn't its skill in ~/.agents/skills")
	}
	if _, err := os.Lstat(filepath.Join(h, ".kimi-code/skills/orca")); !os.IsNotExist(err) {
		t.Error("Kimi Code has a second link to Goose's skill")
	}
}

// A skill an older magpie put in ~/.agents/skills for Kimi Code is taken
// out of there once it is Kimi Code's alone: the link left behind would go
// on reaching every reader of the folder, whatever the library ticks
// (#4379). One an agent whose folder the shared one is (Goose) still gives
// stays.
func TestKimiOldSharedEntrySwept(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi-code/config.toml"), "")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	own := filepath.Join(h, ".kimi-code/skills/pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"kimi"}))
	// what a magpie before Kimi Code's own folder left in the shared one
	if _, err := os.Lstat(shared); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(skillDir("pdf"), shared); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}

	// Goose's skill is delivered in the shared folder and stays there
	skill(t, filepath.Join(src, "orca"), "orca", "Orca")
	ok(t)(InstallSkills(src, []string{"orca"}, []string{"goose"}))
	ok(t)(Sync())

	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Error("the shared entry of Kimi Code's own skill is still there")
	}
	if !ours(own, "pdf") {
		t.Error("Kimi Code hasn't the skill in its own folder")
	}
	if !ours(filepath.Join(h, ".agents/skills/orca"), "orca") {
		t.Error("goose's skill went from ~/.agents/skills")
	}
}

// The sweep of ~/.agents/skills takes magpie's own entries alone: the
// user's own folders and links there, one by the name of a library skill
// Kimi Code is given among them, stay as they were, files and targets
// (#4379). A skill given to Kimi Code and Codex, both of which read the
// shared folder besides their own, goes into each one's own folder and
// not into the shared one.
func TestKimiSweepLeavesTheUsersOwnSharedSkills(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi-code/config.toml"), "")
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "mine"), "mine", "The user's own")
	skill(t, filepath.Join(shared, "pdf"), "pdf", "The user's own pdf")
	skill(t, filepath.Join(h, "elsewhere/ext"), "ext", "Linked by the user")
	if err := os.Symlink(filepath.Join(h, "elsewhere/ext"), filepath.Join(shared, "ext")); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, n := range []string{"mine", "pdf", "ext"} {
		b, err := os.ReadFile(filepath.Join(shared, n, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		before[n] = string(b)
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "orca"), "orca", "Orca")
	// the user's own pdf there is Kimi Code's own by that name: no second
	// one beside it, the user's to settle
	if r, err := InstallSkills(src, []string{"pdf"}, []string{"kimi"}); err != nil || len(r.Problems) != 1 || r.Problems[0].Agent != "kimi" || !r.Problems[0].Own {
		t.Fatalf("pdf: %+v %v", r, err)
	}
	if _, err := os.Lstat(filepath.Join(h, ".kimi-code/skills/pdf")); !os.IsNotExist(err) {
		t.Errorf("Kimi Code has the library's pdf beside the user's: %v", err)
	}
	ok(t)(KeepAgentSkill("pdf", "kimi"))
	ok(t)(InstallSkills(src, []string{"orca"}, []string{"kimi", "codex"}))
	ok(t)(Sync())
	ok(t)(Sync())

	for n, want := range before {
		p := filepath.Join(shared, n)
		if ours(p, n) {
			t.Errorf("%s in ~/.agents/skills became magpie's", n)
		}
		b, err := os.ReadFile(filepath.Join(p, "SKILL.md"))
		if err != nil || string(b) != want {
			t.Errorf("the user's own %s changed: %q %v", n, b, err)
		}
	}
	if to, err := os.Readlink(filepath.Join(shared, "ext")); err != nil || to != filepath.Join(h, "elsewhere/ext") {
		t.Errorf("the user's link moved: %q %v", to, err)
	}
	if _, err := os.Lstat(filepath.Join(shared, "orca")); !os.IsNotExist(err) {
		t.Error("Kimi Code and Codex's skill sits in ~/.agents/skills")
	}
	for _, d := range []string{".kimi-code/skills/orca", ".codex/skills/orca"} {
		if !ours(filepath.Join(h, d), "orca") {
			t.Errorf("%s isn't magpie's link", d)
		}
	}
}
