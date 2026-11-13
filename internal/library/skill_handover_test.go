package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// noGoose takes Goose, whose own folder is ~/.agents/skills, out of the
// sandbox, so the shared folder holds only what a test puts there.
func noGoose(t *testing.T, h string) {
	t.Helper()
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
}

// realFolder: p is a folder, not a link to one
func realFolder(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// tnt1223 on Discord: a skill kept in ~/.agents/skills is on for every
// agent that reads that folder, whatever is ticked. Handed over, its
// folder is the library's own, the agents that read it there are ticked
// and keep it, and each can then be turned off, here alone too.
func TestHandOverSharedFolder(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orchestration"), "orchestration", "Orchestrate")
	symlink(t, filepath.Join(shared, "orchestration"), filepath.Join(h, ".pi/agent/skills/orchestration"))
	ok(t)(ImportSkill("orchestration"))
	if v := offView(t, "orchestration"); v.Shared != filepath.Join(shared, "orchestration") || v.SharedElsewhere || !slices.Contains(v.Always, "codex") {
		t.Fatalf("before: %+v", v)
	}
	if n, err := SharedSkills(); err != nil || !slices.Equal(n, []string{"orchestration"}) {
		t.Fatalf("shared skills: %v %v", n, err)
	}

	ok(t)(HandOverSkills([]string{"orchestration"}))
	if _, err := os.Lstat(filepath.Join(shared, "orchestration")); !os.IsNotExist(err) {
		t.Fatalf("still in ~/.agents/skills: %v", err)
	}
	lib := skillDir("orchestration")
	if linked(lib) || !realFolder(lib) {
		t.Fatal("the library's isn't a folder of its own")
	}
	if read(t, filepath.Join(lib, "scripts/run.sh")) != "echo hi\n" {
		t.Fatal("its files didn't come along")
	}
	v := offView(t, "orchestration")
	if v.Shared != "" || len(v.Always) != 0 || v.Kind != "" {
		t.Errorf("after: %+v", v)
	}
	// the readers and pi, which linked to it, keep it, ticked now
	for _, id := range []string{"codex", "gemini", "pi"} {
		if !slices.Contains(v.Agents, id) {
			t.Errorf("%s isn't ticked: %v", id, v.Agents)
		}
	}
	if slices.Contains(v.Agents, "claude") {
		t.Errorf("claude never had it: %v", v.Agents)
	}
	for _, d := range []string{".codex/skills/orchestration", ".gemini/skills/orchestration", ".pi/agent/skills/orchestration"} {
		if p := filepath.Join(h, d); !ours(p, "orchestration") {
			t.Errorf("%s isn't the library's", d)
		}
	}
	// and now one agent can be without it
	var rest []string
	for _, id := range v.Agents {
		if id != "codex" {
			rest = append(rest, id)
		}
	}
	ok(t)(SkillAgents("orchestration", rest))
	if given(h, ".codex", "orchestration") {
		t.Error("unticked, codex still has it")
	}
	if !given(h, ".gemini", "orchestration") {
		t.Error("gemini lost it")
	}
	// or this computer
	ok(t)(SkillHere("orchestration", false))
	if given(h, ".gemini", "orchestration") || given(h, ".pi/agent", "orchestration") {
		t.Error("off here, an agent still has it")
	}
	if _, err := os.Lstat(filepath.Join(shared, "orchestration")); !os.IsNotExist(err) {
		t.Error("off here, it came back to ~/.agents/skills")
	}

	// handed back: in ~/.agents/skills again, the library linking to it
	ok(t)(SkillHere("orchestration", true))
	ok(t)(HandBackSkill("orchestration"))
	if !realFolder(filepath.Join(shared, "orchestration")) || read(t, filepath.Join(shared, "orchestration/scripts/run.sh")) != "echo hi\n" {
		t.Fatal("not back in ~/.agents/skills")
	}
	if !linked(lib) || realDir(lib) != realDir(filepath.Join(shared, "orchestration")) {
		t.Fatal("the library doesn't link to it")
	}
	if v := offView(t, "orchestration"); v.Shared == "" {
		t.Errorf("handed back: %+v", v)
	}
}

// An entry in ~/.agents/skills that only links to a folder elsewhere: the
// link goes, the folder stays, and the library links to the folder.
func TestHandOverSharedLink(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	ext := filepath.Join(h, "aimer-skills/grilling")
	skill(t, ext, "grilling", "Grill")
	symlink(t, ext, filepath.Join(shared, "grilling"))
	ok(t)(ImportSkill("grilling"))
	if v := offView(t, "grilling"); v.Shared == "" || !v.SharedElsewhere {
		t.Fatalf("before: %+v", v)
	}
	ok(t)(HandOverSkills([]string{"grilling"}))
	if _, err := os.Lstat(filepath.Join(shared, "grilling")); !os.IsNotExist(err) {
		t.Fatalf("the shared link is still there: %v", err)
	}
	if read(t, filepath.Join(ext, "SKILL.md")) == "" {
		t.Fatal("the folder elsewhere went")
	}
	lib := skillDir("grilling")
	if !linked(lib) || realDir(lib) != realDir(ext) {
		t.Fatal("the library doesn't link to the folder")
	}
	v := offView(t, "grilling")
	if v.Shared != "" || v.Kind != "folder" || realDir(v.Source) != realDir(ext) || !slices.Contains(v.Agents, "codex") {
		t.Errorf("after: %+v", v)
	}
	if !ours(filepath.Join(h, ".codex/skills/grilling"), "grilling") {
		t.Error("codex didn't get the library's")
	}
}

// A skill found in ~/.agents/skills, never brought in, is brought in and
// handed over at once; a name that isn't there is said, and the other is
// handed over all the same.
func TestHandOverFound(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orca-cli"), "orca-cli", "Orca")
	// a user's own link to it in Claude Code's folder
	symlink(t, filepath.Join(shared, "orca-cli"), filepath.Join(h, ".claude/skills/orca-cli"))
	res, err := HandOverSkills([]string{"orca-cli", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unmoved) != 1 || res.Unmoved[0].What != "skill:nope" {
		t.Fatalf("unmoved: %+v", res.Unmoved)
	}
	if _, err := os.Lstat(filepath.Join(shared, "orca-cli")); !os.IsNotExist(err) {
		t.Fatalf("still in ~/.agents/skills: %v", err)
	}
	if lib := skillDir("orca-cli"); linked(lib) || !realFolder(lib) {
		t.Fatal("not in the library's folder")
	}
	v := offView(t, "orca-cli")
	for _, id := range []string{"claude", "codex", "gemini"} {
		if !slices.Contains(v.Agents, id) {
			t.Errorf("%s isn't ticked: %v", id, v.Agents)
		}
	}
	if !ours(filepath.Join(h, ".claude/skills/orca-cli"), "orca-cli") {
		t.Error("claude's link isn't the library's")
	}
	// none at all: an error, and nothing changes
	if _, err := HandOverSkills([]string{"nope"}); err == nil {
		t.Error("handing over nothing there said nothing")
	}
}

// A copy that can't be made leaves the skill where it was: the rename
// fails (another disk), and the copy fails too.
func TestHandOverFailureKeepsOriginal(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orchestration"), "orchestration", "Orchestrate")
	ok(t)(ImportSkill("orchestration"))
	lib := skillDir("orchestration")
	// a file in the way of the folder the move makes in the library's
	// skills folder: neither a rename nor a copy can get there
	skills := filepath.Dir(lib)
	if err := os.Chmod(skills, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(skills, 0o755) })
	if f, err := os.Create(filepath.Join(skills, ".probe")); err == nil {
		f.Close()
		t.Skip("a read-only folder can be written here (root, or Windows)")
	}
	res, err := HandOverSkills([]string{"orchestration"})
	if err == nil {
		t.Fatalf("moved into a read-only folder: %+v", res)
	}
	os.Chmod(skills, 0o755)
	if !realFolder(filepath.Join(shared, "orchestration")) || read(t, filepath.Join(shared, "orchestration/scripts/run.sh")) != "echo hi\n" {
		t.Fatal("the original isn't where it was")
	}
	if !linked(lib) || realDir(lib) != realDir(filepath.Join(shared, "orchestration")) {
		t.Fatal("the library no longer links to it")
	}
	if v := offView(t, "orchestration"); v.Shared == "" {
		t.Errorf("after the failure: %+v", v)
	}
}

// Goose reads ~/.agents/skills as its own folder: handed over, the
// library gives it a link there, and the skill's folder is the library's.
func TestHandOverWithGoose(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orchestration"), "orchestration", "Orchestrate")
	ok(t)(ImportSkill("orchestration"))
	ok(t)(HandOverSkills([]string{"orchestration"}))
	lib := skillDir("orchestration")
	if linked(lib) || !realFolder(lib) {
		t.Fatal("not in the library's folder")
	}
	if !ours(filepath.Join(shared, "orchestration"), "orchestration") {
		t.Fatal("goose's isn't the library's")
	}
	if v := offView(t, "orchestration"); !slices.Contains(v.Agents, "goose") || v.Shared != "" {
		t.Errorf("after: %+v", v)
	}
}

// A skill kept in ~/.agents/skills under another folder's name is still
// the shared folder's: Goose, whose folder that is, isn't given a second
// one beside it, and handed over, the folder moves in by the skill's name.
func TestSharedUnderAnotherName(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/skills")
	skill(t, filepath.Join(shared, "orchestration-v2"), "orchestration", "Orchestrate")
	// linked into the library's folder by hand, and brought in from there
	symlink(t, filepath.Join(shared, "orchestration-v2"), skillDir("orchestration"))
	ok(t)(ImportSkill("orchestration"))
	ok(t)(SkillAgents("orchestration", []string{"claude", "goose"}))
	if _, err := os.Lstat(filepath.Join(shared, "orchestration")); !os.IsNotExist(err) {
		t.Fatalf("goose was given it twice: %v", err)
	}
	v := offView(t, "orchestration")
	if v.Shared != filepath.Join(shared, "orchestration-v2") || !slices.Contains(v.Always, "codex") {
		t.Fatalf("before: %+v", v)
	}
	ok(t)(HandOverSkills([]string{"orchestration"}))
	if _, err := os.Lstat(filepath.Join(shared, "orchestration-v2")); !os.IsNotExist(err) {
		t.Fatalf("still in ~/.agents/skills: %v", err)
	}
	if lib := skillDir("orchestration"); linked(lib) || !realFolder(lib) {
		t.Fatal("not in the library's folder")
	}
	if !ours(filepath.Join(shared, "orchestration"), "orchestration") {
		t.Error("goose isn't given the library's")
	}
}

// ConatusMoon2222 on X: with every skill kept in ~/.agents/skills by the
// setting, handing skills over left magpie's links there (the sync made
// them again). The setting goes off, magpie's links go, the user's own
// entries stay.
func TestHandOverWithSharedSetting(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	shared := filepath.Join(h, ".agents/skills")
	ext := filepath.Join(h, "aimer-skills/grilling")
	skill(t, ext, "grilling", "Grill")
	symlink(t, ext, filepath.Join(shared, "grilling"))
	ok(t)(ImportSkill("grilling"))
	ok(t)(SetSkillsWhere(SkillsInShared))
	// a skill of the library's the setting linked into the shared folder
	ok(t)(CreateSkill("other", "Other", nil))
	own := filepath.Join(shared, "handmade")
	skill(t, own, "handmade", "Mine")

	ok(t)(HandOverSkills([]string{"grilling"}))
	if _, err := os.Lstat(filepath.Join(shared, "grilling")); !os.IsNotExist(err) {
		t.Fatalf("a link is left in ~/.agents/skills: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(shared, "other")); !os.IsNotExist(err) {
		t.Errorf("magpie's link to another skill is left: %v", err)
	}
	if !realFolder(own) {
		t.Error("the user's own folder went")
	}
	if v, err := Read(nil); err != nil || v.SharedSkills {
		t.Error("the setting is still on")
	}
}
