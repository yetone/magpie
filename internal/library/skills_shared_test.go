package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

func setOmpVersion(t *testing.T, v string) {
	t.Helper()
	was := ompVersion
	ompVersion = func(*agent.Agent) string { return v }
	t.Cleanup(func() { ompVersion = was })
}

func alwaysOf(t *testing.T, name string) []string {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Skills {
		if s.Name == name {
			return s.Always
		}
	}
	t.Fatalf("no skill %s in the view", name)
	return nil
}

// usrname1833: Pi reads ~/.agents/skills itself, from 0.54.0 (its
// CHANGELOG, package-manager.js userAgentsSkillsDir), omp from 11.0.0
// (discovery/agents.ts) and OmO's senpi in every version on npm. A skill
// kept there is theirs whatever is ticked (the lock), and no second link
// is made in their own folders; an older or unknown one is as before.
func TestPiReadsSharedSkillsFromItsVersion(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	write(t, filepath.Join(h, ".omp/agent/mcp.json"), "{}")
	holdCLI(t, h, "omp")
	write(t, filepath.Join(h, ".omo/agent/settings.json"), "{}")
	holdCLI(t, h, "omo")
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orch"), "orch", "Orchestrate")
	own := map[string]string{
		"pi":  filepath.Join(h, ".pi/agent/skills/orch"),
		"omp": filepath.Join(h, ".omp/agent/skills/orch"),
		"omo": filepath.Join(h, ".omo/agent/skills/orch"),
	}

	setPiVersion(t, "0.53.0")
	setOmpVersion(t, "10.6.2")
	ok(t)(ImportSkill("orch"))
	ok(t)(SkillAgents("orch", []string{"claude", "pi", "omp", "omo"}))
	for _, id := range []string{"pi", "omp"} {
		if !ours(own[id], "orch") {
			t.Errorf("%s before it read ~/.agents/skills hasn't its own link", id)
		}
	}
	if _, err := os.Lstat(own["omo"]); !os.IsNotExist(err) {
		t.Errorf("omo reads ~/.agents/skills, yet got a second link: %v", err)
	}
	a := alwaysOf(t, "orch")
	if slices.Contains(a, "pi") || slices.Contains(a, "omp") || !slices.Contains(a, "omo") || !slices.Contains(a, "codex") {
		t.Errorf("always before: %v", a)
	}

	setPiVersion(t, "0.54.0")
	setOmpVersion(t, "11.0.0")
	ok(t)(Sync())
	for _, id := range []string{"pi", "omp"} {
		if _, err := os.Lstat(own[id]); !os.IsNotExist(err) {
			t.Errorf("%s reads ~/.agents/skills now, yet keeps a second link: %v", id, err)
		}
	}
	a = alwaysOf(t, "orch")
	for _, id := range []string{"pi", "omp", "omo", "codex"} {
		if !slices.Contains(a, id) {
			t.Errorf("%s isn't said to have it always: %v", id, a)
		}
	}
	if slices.Contains(a, "claude") {
		t.Errorf("claude reads no ~/.agents/skills: %v", a)
	}

	// a version not known is taken as an older one, as before
	setPiVersion(t, "")
	ok(t)(Sync())
	if !ours(own["pi"], "orch") || slices.Contains(alwaysOf(t, "orch"), "pi") {
		t.Error("a pi of no known version isn't as before")
	}
}

// usrname1833: the library's skills can go into ~/.agents/skills too, as
// magpie's links to the library's folders, so every agent and tool that
// reads that folder has every skill. Off (the default) is as before;
// turning it off takes out magpie's links there and nothing else.
func TestSkillsWhereShared(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	setPiVersion(t, "0.70.0")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "mine"), "mine", "Mine")
	shared := filepath.Join(h, ".agents/skills")
	// the user's own skills there: one magpie has no skill by, and one by
	// a name the library has, with other files
	skill(t, filepath.Join(shared, "theirs"), "theirs", "Theirs")
	write(t, filepath.Join(shared, "mine", "SKILL.md"), "---\nname: mine\ndescription: Not the library's\n---\n")
	pdf := filepath.Join(shared, "pdf")
	pi, cl := filepath.Join(h, ".pi/agent/skills/pdf"), filepath.Join(h, ".claude/skills/pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "pi"}))
	if _, err := os.Lstat(pdf); !os.IsNotExist(err) {
		t.Fatalf("the default put pdf in ~/.agents/skills: %v", err)
	}
	if !ours(pi, "pdf") {
		t.Fatal("pi hasn't pdf")
	}
	if v, _ := Read(nil); v.SharedSkills || v.SharedDir != shared {
		t.Errorf("view: shared %v at %q", v.SharedSkills, v.SharedDir)
	}

	if _, err := SetSkillsWhere("everywhere"); err == nil {
		t.Error("a place it doesn't know was taken")
	}
	ok(t)(SetSkillsWhere(SkillsInShared))
	if !ours(pdf, "pdf") || !linked(pdf) {
		t.Fatal("pdf isn't magpie's link in ~/.agents/skills")
	}
	// a sync leaves the link as it is, not taken out and made again,
	// which a reader of the folder would find missing in between
	was, _ := os.Lstat(pdf)
	ok(t)(Sync())
	if now, err := os.Lstat(pdf); err != nil || !os.SameFile(was, now) {
		t.Errorf("a sync made the link again: %v", err)
	}
	// pi reads that folder: no second link; claude doesn't: its stays
	if _, err := os.Lstat(pi); !os.IsNotExist(err) {
		t.Errorf("pi keeps a second link: %v", err)
	}
	if !ours(cl, "pdf") {
		t.Error("claude lost its link")
	}
	a := alwaysOf(t, "pdf")
	for _, id := range []string{"pi", "codex", "gemini"} {
		if !slices.Contains(a, id) {
			t.Errorf("%s isn't said to have it always: %v", id, a)
		}
	}
	if slices.Contains(a, "claude") {
		t.Errorf("claude: %v", a)
	}
	if v, _ := Read(nil); !v.SharedSkills {
		t.Error("the view doesn't say skills go to ~/.agents/skills")
	}
	// the user's own are as they were
	if _, err := os.Stat(filepath.Join(shared, "theirs", "SKILL.md")); err != nil {
		t.Error("the user's own skill went")
	}

	// a library skill by a name the user has there with other files:
	// theirs stays, and the agents ticked for the library's get it in
	// their own folders
	ok(t)(InstallSkills(src, []string{"mine"}, []string{"claude", "pi"}))
	if !ours(filepath.Join(h, ".pi/agent/skills/mine"), "mine") {
		t.Error("pi hasn't the library's mine")
	}
	if ours(filepath.Join(shared, "mine"), "mine") || !strings.Contains(read(t, filepath.Join(shared, "mine", "SKILL.md")), "Not the library's") {
		t.Error("the user's own mine was written over")
	}

	// off on this computer: out of ~/.agents/skills, and pi's own is back
	ok(t)(SkillHere("pdf", false))
	if _, err := os.Lstat(pdf); !os.IsNotExist(err) {
		t.Errorf("a skill off here stays in ~/.agents/skills: %v", err)
	}
	ok(t)(SkillHere("pdf", true))
	if !ours(pdf, "pdf") {
		t.Error("on again, pdf isn't back")
	}

	// a skill unticked everywhere is still in the shared folder
	ok(t)(SkillAgents("pdf", nil))
	if !ours(pdf, "pdf") {
		t.Error("unticked, pdf left ~/.agents/skills")
	}

	// back to each agent's folder: magpie's links there go, nothing else
	ok(t)(SkillAgents("pdf", []string{"claude", "pi"}))
	r, err := SetSkillsWhere(SkillsInAgents)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 0 {
		t.Errorf("problems: %+v", r.Problems)
	}
	if _, err := os.Lstat(pdf); !os.IsNotExist(err) {
		t.Errorf("pdf stays in ~/.agents/skills: %v", err)
	}
	if !ours(pi, "pdf") {
		t.Error("pi hasn't its own link back")
	}
	if slices.Contains(alwaysOf(t, "pdf"), "pi") {
		t.Error("pi still said to have it always")
	}
	for _, n := range []string{"theirs", "mine"} {
		if _, err := os.Stat(filepath.Join(shared, n, "SKILL.md")); err != nil {
			t.Errorf("the user's own %s went", n)
		}
	}
	if _, err := os.Stat(filepath.Join(skillDir("pdf"), "SKILL.md")); err != nil {
		t.Error("the library's pdf went")
	}
}

// A skill ticked for Kimi Code, whose folder ~/.agents/skills is, and then
// unticked for it stays there while the setting keeps it there.
func TestSkillsWhereSharedWithKimi(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi/config.toml"), "")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	pdf := filepath.Join(h, ".agents/skills/pdf")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"kimi"}))
	ok(t)(SetSkillsWhere(SkillsInShared))
	ok(t)(SkillAgents("pdf", []string{"claude"}))
	if !ours(pdf, "pdf") {
		t.Fatal("unticked for Kimi Code, pdf left ~/.agents/skills")
	}
	ok(t)(Sync())
	if !ours(pdf, "pdf") {
		t.Fatal("a second sync took pdf out")
	}
	ok(t)(SetSkillsWhere(SkillsInAgents))
	if _, err := os.Lstat(pdf); !os.IsNotExist(err) {
		t.Errorf("pdf stays: %v", err)
	}
}
