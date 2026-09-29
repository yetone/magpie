package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func symlink(t *testing.T, to, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, at); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// #227: the user-wide ~/.agents/skills is found, links to it (symlinks,
// junctions on Windows) are the same skill, and bringing one in leaves the
// shared folder as it is.
func TestSharedAgentsSkills(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/skills")
	// a skill kept elsewhere (D:\aimer-skills\…) linked into the shared folder
	ext := filepath.Join(h, "aimer-skills/grilling")
	skill(t, ext, "grilling", "Grill")
	symlink(t, ext, filepath.Join(shared, "grilling"))
	// real folders in the shared folder
	skill(t, filepath.Join(shared, "orchestration"), "orchestration", "Orchestrate")
	skill(t, filepath.Join(shared, "orca-cli"), "orca-cli", "Orca")
	// agents' links to it: absolute, relative, and to a link
	symlink(t, filepath.Join(shared, "orchestration"), filepath.Join(h, ".pi/agent/skills/orchestration"))
	symlink(t, filepath.Join("..", "..", ".agents", "skills", "orchestration"), filepath.Join(h, ".codex/skills/orchestration"))
	symlink(t, filepath.Join(shared, "grilling"), filepath.Join(h, ".gemini/skills/grilling"))
	// a byte copy of its own
	skill(t, filepath.Join(h, ".codex/skills/orca-cli"), "orca-cli", "Orca")
	// not a skill, and a hidden one
	write(t, filepath.Join(shared, "notes/README.md"), "")
	skill(t, filepath.Join(shared, ".hidden"), "hidden", "")

	found := func() map[string]FoundSkill {
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]FoundSkill{}
		for _, f := range v.FoundSkills {
			m[f.Name] = f
		}
		return m
	}
	byName := found()
	if len(byName) != 3 {
		t.Fatalf("found: %+v", byName)
	}
	if f := byName["grilling"]; f.Shared != filepath.Join(shared, "grilling") || f.Link != realDir(ext) || !slices.Equal(f.Agents, []string{"gemini"}) || len(f.Others) != 0 {
		t.Errorf("grilling: %+v", f)
	}
	f := byName["orchestration"]
	agents := slices.Sorted(slices.Values(f.Agents))
	if f.Shared == "" || f.Link != "" || !slices.Equal(agents, []string{"codex", "pi"}) || len(f.Others) != 0 {
		t.Errorf("orchestration is one skill in the shared folder, pi and codex: %+v", f)
	}
	if f := byName["orca-cli"]; f.Shared == "" || len(f.Agents) != 0 || !slices.Equal(f.Others, []string{"codex"}) {
		t.Errorf("orca-cli: %+v", f)
	}

	ok(t)(ImportSkill("orchestration"))
	ok(t)(ImportSkill("grilling"))
	ok(t)(ImportSkill("orca-cli"))
	// the shared folder is as it was
	for _, n := range []string{"orchestration", "orca-cli"} {
		if fi, err := os.Lstat(filepath.Join(shared, n)); err != nil || !fi.IsDir() {
			t.Errorf("%s left the shared folder: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(shared, n, "scripts/run.sh")); err != nil {
			t.Errorf("%s's files: %v", n, err)
		}
	}
	if !linked(filepath.Join(shared, "grilling")) {
		t.Error("the shared folder's link went")
	}
	if _, err := os.Stat(filepath.Join(ext, "SKILL.md")); err != nil {
		t.Error("the folder the shared link pointed to went")
	}
	// the library links to them
	for n, want := range map[string]string{"orchestration": filepath.Join(shared, "orchestration"), "grilling": ext, "orca-cli": filepath.Join(shared, "orca-cli")} {
		if !linked(skillDir(n)) || realDir(skillDir(n)) != realDir(want) {
			t.Errorf("the library's %s isn't a link to %s", n, want)
		}
	}
	// the agents that had it have the library's, the one with its own copy keeps it
	for _, d := range []string{".pi/agent/skills/orchestration", ".codex/skills/orchestration", ".gemini/skills/grilling"} {
		if !ours(filepath.Join(h, d), filepath.Base(d)) {
			t.Errorf("%s isn't the library's", d)
		}
	}
	if linked(filepath.Join(h, ".codex/skills/orca-cli")) {
		t.Error("codex's own copy was replaced")
	}
	v, _ := Read(nil)
	for _, s := range v.Skills {
		if s.Kind != "folder" {
			t.Errorf("%s: %+v", s.Name, s)
		}
	}
	if byName := found(); len(byName) != 0 {
		t.Errorf("still found: %+v", byName)
	}
}

func TestRealDir(t *testing.T) {
	d := t.TempDir()
	skill(t, filepath.Join(d, "a/real"), "real", "")
	symlink(t, filepath.Join(d, "a/real"), filepath.Join(d, "b/abs"))
	symlink(t, filepath.Join("..", "b", "abs"), filepath.Join(d, "c/rel"))
	symlink(t, filepath.Join(d, "c"), filepath.Join(d, "dir"))
	want, _ := filepath.EvalSymlinks(filepath.Join(d, "a/real"))
	for _, p := range []string{"a/real", "b/abs", "c/rel", "dir/rel", "dir/rel/scripts/.."} {
		if got := realDir(filepath.Join(d, p)); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	if got := realDir(filepath.Join(d, "dir/rel/scripts")); got != filepath.Join(want, "scripts") {
		t.Errorf("inside: %s", got)
	}
	// a loop, and what isn't there: the path as it is
	symlink(t, filepath.Join(d, "loop2"), filepath.Join(d, "loop1"))
	symlink(t, filepath.Join(d, "loop1"), filepath.Join(d, "loop2"))
	for _, p := range []string{filepath.Join(d, "loop1"), filepath.Join(d, "gone/x")} {
		if got := realDir(p); got != p {
			t.Errorf("%s: %s", p, got)
		}
	}
	if linked(filepath.Join(d, "a/real")) || !linked(filepath.Join(d, "c/rel")) {
		t.Error("isLink")
	}
	if !within(filepath.Join(d, "a", "x"), filepath.Join(d, "a")) || within(filepath.Join(d, "ab"), filepath.Join(d, "a")) || within(d, filepath.Join(d, "a")) {
		t.Error("within")
	}
}

// A junction (what Windows makes without the right to symlink, and what
// the issue's ZCode and Pi folders were) is a link to the folder it points to.
func TestJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows'")
	}
	d := t.TempDir()
	skill(t, filepath.Join(d, "shared/orchestration"), "orchestration", "")
	j := filepath.Join(d, "zcode", "orchestration")
	os.MkdirAll(filepath.Dir(j), 0o755)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", j, filepath.Join(d, "shared", "orchestration")).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	if !linked(j) {
		t.Error("a junction isn't a link")
	}
	if realDir(j) != realDir(filepath.Join(d, "shared", "orchestration")) {
		t.Errorf("%s != %s", realDir(j), realDir(filepath.Join(d, "shared", "orchestration")))
	}
}
