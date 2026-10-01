package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func isLink(t *testing.T, p string) bool {
	t.Helper()
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func gone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s is still there", p)
	}
}

// projectLib is a sandbox with a library skill pdf (from a folder) and a
// project folder with a .gitignore of the user's.
func projectLib(t *testing.T) (h, proj string) {
	h = sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "docx"), "docx", "Word")
	ok(t)(InstallSkills(src, []string{"pdf", "docx"}, nil))
	proj = filepath.Join(t.TempDir(), "app")
	write(t, filepath.Join(proj, ".gitignore"), "node_modules/\n")
	ok(t)(AddProject(proj))
	return h, proj
}

func TestProjectSkillsLinked(t *testing.T) {
	_, proj := projectLib(t)
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude", "codex", "gemini"}))
	for _, e := range []string{".claude/skills/pdf", ".agents/skills/pdf"} {
		p := filepath.Join(proj, e)
		if !isLink(t, p) || !ours(p, "pdf") {
			t.Errorf("%s isn't a link to the library's", e)
		}
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	want := "node_modules/\n\n" + ignoreBegin + "\n/.agents/skills/pdf\n/.claude/skills/pdf\n" + ignoreEnd + "\n"
	if g := read(t, filepath.Join(proj, ".gitignore")); g != want {
		t.Errorf(".gitignore:\n%s", g)
	}
	v, _ := Read(nil)
	if len(v.Projects) != 1 || !slices.Equal(v.Projects[0].Skills["pdf"], []string{"claude", "codex", "gemini"}) {
		t.Errorf("projects: %+v", v.Projects)
	}

	// one agent of two sharing .agents/skills taken away: it stays for the other
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude", "codex"}))
	if !isLink(t, filepath.Join(proj, ".agents/skills/pdf")) {
		t.Error(".agents/skills/pdf went while codex still has it")
	}
	ok(t)(ProjectSkill(proj, "pdf", []string{"codex"}))
	gone(t, filepath.Join(proj, ".claude/skills/pdf"))
	gone(t, filepath.Join(proj, ".claude")) // magpie made it, and it's empty
	if g := read(t, filepath.Join(proj, ".gitignore")); strings.Contains(g, ".claude") || !strings.Contains(g, "/.agents/skills/pdf") {
		t.Errorf(".gitignore:\n%s", g)
	}

	// removing the project takes away all it placed, and its lines
	ok(t)(RemoveProject(proj))
	gone(t, filepath.Join(proj, ".agents"))
	if g := read(t, filepath.Join(proj, ".gitignore")); g != "node_modules/\n" {
		t.Errorf(".gitignore after: %q", g)
	}
	if v, _ := Read(nil); len(v.Projects) != 0 {
		t.Errorf("projects: %+v", v.Projects)
	}
}

func TestProjectGitignoreMadeAndTakenAway(t *testing.T) {
	_, proj := projectLib(t)
	os.Remove(filepath.Join(proj, ".gitignore"))
	ok(t)(ProjectSkill(proj, "docx", []string{"claude"}))
	if g := read(t, filepath.Join(proj, ".gitignore")); g != ignoreBegin+"\n/.claude/skills/docx\n"+ignoreEnd+"\n" {
		t.Errorf(".gitignore: %q", g)
	}
	ok(t)(ProjectSkill(proj, "docx", nil))
	gone(t, filepath.Join(proj, ".gitignore"))
	gone(t, filepath.Join(proj, ".claude/skills/docx"))
}

func TestProjectSkillsCopied(t *testing.T) {
	h, proj := projectLib(t)
	ok(t)(ProjectCopy(proj, true))
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude"}))
	p := filepath.Join(proj, ".claude/skills/pdf")
	if isLink(t, p) || !ours(p, "pdf") {
		t.Fatal("not magpie's copy")
	}
	if s := read(t, filepath.Join(p, "scripts/run.sh")); s != "echo hi\n" {
		t.Errorf("run.sh: %q", s)
	}
	// the library's changes: the next change to the library copies it again
	write(t, filepath.Join(h, "src/pdf/SKILL.md"), "---\nname: pdf\ndescription: Changed\n---\n")
	ok(t)(Sync())
	if !strings.Contains(read(t, filepath.Join(p, "SKILL.md")), "Changed") {
		t.Error("the copy wasn't made again")
	}
	// back to links: the copy becomes a link
	// (not on Windows, where a copy as fresh as the library's stands for one)
	ok(t)(ProjectCopy(proj, false))
	if runtime.GOOS != "windows" && !isLink(t, p) {
		t.Error("still a copy")
	}
}

func TestProjectRefusesForeignFolder(t *testing.T) {
	_, proj := projectLib(t)
	skill(t, filepath.Join(proj, ".claude/skills/pdf"), "pdf", "The project's own")
	r, err := ProjectSkill(proj, "pdf", []string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || r.Problems[0].Agent != proj || !strings.Contains(r.Problems[0].Error, "isn't magpie's") {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if !strings.Contains(read(t, filepath.Join(proj, ".claude/skills/pdf/SKILL.md")), "The project's own") {
		t.Error("the project's own was overwritten")
	}
	if !isLink(t, filepath.Join(proj, ".agents/skills/pdf")) {
		t.Error("codex's went with it")
	}
	if g := read(t, filepath.Join(proj, ".gitignore")); strings.Contains(g, ".claude/skills/pdf") {
		t.Errorf("the project's own is in .gitignore:\n%s", g)
	}
	// removing the project leaves the folder that isn't magpie's
	ok(t)(ProjectSkill(proj, "pdf", nil))
	ok(t)(RemoveProject(proj))
	if _, err := os.Stat(filepath.Join(proj, ".claude/skills/pdf/SKILL.md")); err != nil {
		t.Error("the project's own went")
	}
}

func TestProjectRefusesHomeAndUnknownAgents(t *testing.T) {
	h, proj := projectLib(t)
	if _, err := AddProject(h); err == nil {
		t.Error("home added as a project")
	}
	if _, err := AddProject(proj); err == nil {
		t.Error("added twice")
	}
	if _, err := ProjectSkill(proj, "pdf", []string{"crush"}); err == nil {
		t.Error("crush given a project skill")
	}
}

func TestRemoveSkillLeavesProjects(t *testing.T) {
	_, proj := projectLib(t)
	ok(t)(ProjectSkill(proj, "pdf", []string{"claude"}))
	ok(t)(RemoveSkill("pdf"))
	gone(t, filepath.Join(proj, ".claude/skills/pdf"))
	if v, _ := Read(nil); len(v.Projects[0].Skills) != 0 {
		t.Errorf("skills: %+v", v.Projects[0].Skills)
	}
}

func TestUpdateSkillRefreshesProjects(t *testing.T) {
	sandbox(t)
	version := "one"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, map[string]string{
			"skills/pdf/SKILL.md":  "---\nname: pdf\ndescription: PDFs " + version + "\n---\n",
			"skills/docx/SKILL.md": "---\nname: docx\ndescription: Word " + version + "\n---\n",
		}))
	}))
	defer srv.Close()
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	defer func() { tarballURL = old }()

	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf", "skills/docx"}, nil))
	linked, copied := filepath.Join(t.TempDir(), "linked"), filepath.Join(t.TempDir(), "copied")
	os.MkdirAll(linked, 0o755)
	os.MkdirAll(copied, 0o755)
	ok(t)(AddProject(linked))
	ok(t)(AddProject(copied))
	ok(t)(ProjectCopy(copied, true))
	for _, p := range []string{linked, copied} {
		ok(t)(ProjectSkill(p, "pdf", []string{"claude"}))
		ok(t)(ProjectSkill(p, "docx", []string{"codex"}))
	}
	version = "two"
	ok(t)(UpdateSkill("pdf"))
	ok(t)(UpdateSomeSkills([]string{"docx"}))
	for _, p := range []string{linked, copied} {
		for _, e := range []string{".claude/skills/pdf", ".agents/skills/docx"} {
			if s := read(t, filepath.Join(p, e, "SKILL.md")); !strings.Contains(s, " two") {
				t.Errorf("%s/%s after the update: %q", filepath.Base(p), e, s)
			}
		}
	}
	if !isLink(t, filepath.Join(linked, ".claude/skills/pdf")) || isLink(t, filepath.Join(copied, ".claude/skills/pdf")) {
		t.Error("links and copies changed places")
	}
	version = "three"
	ok(t)(UpdateSkills())
	if s := read(t, filepath.Join(copied, ".agents/skills/docx/SKILL.md")); !strings.Contains(s, "three") {
		t.Errorf("copy after update all: %q", s)
	}
}

func TestRemoveSkillWhoseFolderIsGone(t *testing.T) {
	projectLib(t)
	if err := os.RemoveAll(skillDir("pdf")); err != nil {
		t.Fatal(err)
	}
	ok(t)(RemoveSkill("pdf"))
	if v, _ := Read(nil); slices.ContainsFunc(v.Skills, func(s SkillView) bool { return s.Name == "pdf" }) {
		t.Error("pdf still listed")
	}
}
