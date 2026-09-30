package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where magpie can't link a skill into an agent (Windows without the right
// to make links), it puts a copy there instead. A link shows the library's
// skill as it is now; the copy has to be made again when the library's
// changes, or the agent keeps the skill as it was when it was first given.
func TestSkillCopyFollowsTheLibrary(t *testing.T) {
	h := sandbox(t)
	version := "one"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, map[string]string{
			"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs " + version + "\n---\n",
			"skills/pdf/forms.md": "forms " + version,
		}))
	}))
	defer srv.Close()
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	defer func() { tarballURL = old }()

	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	p := filepath.Join(h, ".claude/skills/pdf")
	// the copy link makes where it can't link, wherever this runs
	if fi, err := os.Lstat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := copyDir(skillDir("pdf"), p); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(p, marker), "copied from "+skillDir("pdf")+"\n")
	}
	if !ours(p, "pdf") {
		t.Fatal("the copy isn't magpie's")
	}

	version = "two"
	ok(t)(UpdateSkill("pdf"))
	if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "PDFs two") {
		t.Fatalf("claude's copy after the update:\n%s", s)
	}
	if s := read(t, filepath.Join(p, "forms.md")); s != "forms two" {
		t.Fatalf("forms.md after the update: %q", s)
	}
	if !ours(p, "pdf") {
		t.Fatal("the copy made again isn't magpie's")
	}

	// a skill changed in the library's folder by hand reaches it on a sync
	write(t, filepath.Join(skillDir("pdf"), "forms.md"), "forms three")
	ok(t)(Sync())
	if s := read(t, filepath.Join(p, "forms.md")); s != "forms three" {
		t.Fatalf("forms.md after a sync: %q", s)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("claude's skills folder: %v", names)
	}
}
