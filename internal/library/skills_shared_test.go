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
	// theirs stays; an agent that doesn't read the folder gets the
	// library's, and one that does (Pi) has a skill of its own by that
	// name, the user's to settle, with no second one beside it
	r, err := InstallSkills(src, []string{"mine"}, []string{"claude", "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || r.Problems[0].Agent != "pi" || !r.Problems[0].Own {
		t.Errorf("problems: %+v", r.Problems)
	}
	if _, err := os.Lstat(filepath.Join(h, ".pi/agent/skills/mine")); !os.IsNotExist(err) {
		t.Errorf("pi has the library's mine beside the user's: %v", err)
	}
	if !ours(filepath.Join(h, ".claude/skills/mine"), "mine") {
		t.Error("claude hasn't the library's mine")
	}
	if ours(filepath.Join(shared, "mine"), "mine") || !strings.Contains(read(t, filepath.Join(shared, "mine", "SKILL.md")), "Not the library's") {
		t.Error("the user's own mine was written over")
	}
	ok(t)(KeepAgentSkill("mine", "pi"))

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
	r, err = SetSkillsWhere(SkillsInAgents)
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

// Young on Discord: the ChatGPT app's Work registers only Codex's own
// folder as a skill root (readCapabilityRoots, codex-home-skills), so a
// library skill Codex had through ~/.agents/skills alone never reached a
// ChatGPT chat. Codex gets its own link of each one, ticked or not, to
// the library's one folder (Codex lists it once, by its real path); the
// other readers of the folder still get no second one.
func TestChatGPTWorkFindsSharedSkills(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	shared := filepath.Join(h, ".agents/skills")
	cx := func(name string) string { return filepath.Join(h, ".codex/skills", name) }
	gm := func(name string) string { return filepath.Join(h, ".gemini/skills", name) }
	same := func(name string) bool {
		return ours(cx(name), name) && realDir(cx(name)) == realDir(skillDir(name))
	}

	// kept in ~/.agents/skills, brought into the library: ticked for
	// Claude alone, and for Codex too
	skill(t, filepath.Join(shared, "orch"), "orch", "Orchestrate")
	skill(t, filepath.Join(shared, "ship"), "ship", "Ship")
	ok(t)(ImportSkill("orch"))
	ok(t)(ImportSkill("ship"))
	ok(t)(SkillAgents("orch", []string{"claude"}))
	ok(t)(SkillAgents("ship", []string{"claude", "codex", "gemini"}))
	for _, n := range []string{"orch", "ship"} {
		if !same(n) {
			t.Errorf("codex hasn't its own link of %s, kept in ~/.agents/skills", n)
		}
		if _, err := os.Lstat(gm(n)); !os.IsNotExist(err) {
			t.Errorf("gemini got a second %s: %v", n, err)
		}
		if a := alwaysOf(t, n); !slices.Contains(a, "codex") {
			t.Errorf("%s: always %v", n, a)
		}
	}

	// off on this computer: codex's own goes, back when on again
	ok(t)(SkillHere("orch", false))
	if _, err := os.Lstat(cx("orch")); !os.IsNotExist(err) {
		t.Errorf("codex keeps orch, off here: %v", err)
	}
	ok(t)(SkillHere("orch", true))
	if !same("orch") {
		t.Error("orch on again isn't codex's")
	}

	// put in ~/.agents/skills by the setting
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude"}))
	if _, err := os.Lstat(cx("pdf")); !os.IsNotExist(err) {
		t.Errorf("codex has pdf, ticked for claude alone: %v", err)
	}
	ok(t)(SetSkillsWhere(SkillsInShared))
	if !ours(filepath.Join(shared, "pdf"), "pdf") || !same("pdf") {
		t.Error("with skills in ~/.agents/skills too, codex hasn't its own pdf")
	}
	if _, err := os.Lstat(gm("pdf")); !os.IsNotExist(err) {
		t.Errorf("gemini got a second pdf: %v", err)
	}
	// with the router on it is a link of its own still, not one the
	// router lists
	ok(t)(SetSkillRouter(true))
	if !same("pdf") || !same("orch") {
		t.Error("the router took codex's own link of a skill in ~/.agents/skills")
	}
	ok(t)(SetSkillRouter(false))
	// off again: the setting's skill unticked for codex goes from its folder
	ok(t)(SetSkillsWhere(SkillsInAgents))
	if _, err := os.Lstat(cx("pdf")); !os.IsNotExist(err) {
		t.Errorf("codex keeps pdf, no longer in ~/.agents/skills: %v", err)
	}
	if !same("orch") || !same("ship") {
		t.Error("codex lost the skills kept in ~/.agents/skills")
	}

	// codex's own skill by a name it has through ~/.agents/skills, not
	// ticked for it: left as it is, unsaid
	skill(t, filepath.Join(shared, "plan"), "plan", "Plan")
	write(t, filepath.Join(h, ".codex/skills/plan/SKILL.md"), "---\nname: plan\ndescription: Codex's own\n---\n")
	ok(t)(ImportSkill("plan"))
	r, err := Sync()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(r.Problems, func(p Problem) bool { return p.What == "skill:plan" }) {
		t.Errorf("problems: %+v", r.Problems)
	}
	if ours(cx("plan"), "plan") || !strings.Contains(read(t, cx("plan")+"/SKILL.md"), "Codex's own") {
		t.Error("codex's own plan was written over")
	}

	// the user's own skill by the name, other files, in ~/.agents/skills:
	// codex has one of its own, and gets no second one beside it
	write(t, filepath.Join(shared, "mine", "SKILL.md"), "---\nname: mine\ndescription: Not the library's\n---\n")
	skill(t, filepath.Join(src, "mine"), "mine", "Mine")
	r, err = InstallSkills(src, []string{"mine"}, []string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.Problems, func(p Problem) bool { return p.Agent == "codex" && p.Own }) {
		t.Errorf("problems: %+v", r.Problems)
	}
	if _, err := os.Lstat(cx("mine")); !os.IsNotExist(err) {
		t.Errorf("codex has the library's mine beside the user's: %v", err)
	}
}
