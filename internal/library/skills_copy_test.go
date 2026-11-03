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
	// the copy link makes where it can't link, wherever this runs: the
	// library giving copies (a copy left where links are given is made a
	// link again, #896)
	ok(t)(SetSkillHow("", HowCopy))
	if linked(p) {
		t.Fatal("claude's pdf is still a link")
	}
	if !ours(p, "pdf") {
		t.Fatal("the copy isn't magpie's")
	}

	version = "two"
	ok(t)(UpdateSkill("pdf", false))
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

// Remaking a copy must never strand it half-removed: when a file in the old
// copy can't be removed (held open on Windows; a read-only folder here), the
// agent still gets the whole new copy, magpie still knows it for its own, and
// what's left of the old one is removed on a later sync.
func TestSkillCopySwapSurvivesAnUndeletableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only folders don't stop removal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can remove files from a read-only folder")
	}
	h := sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball(t, map[string]string{
			"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs one\n---\n",
		}))
	}))
	defer srv.Close()
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/" + ref }
	defer func() { tarballURL = old }()

	ok(t)(InstallSkills("owner/repo", []string{"skills/pdf"}, []string{"claude"}))
	p := filepath.Join(h, ".claude/skills/pdf")
	// a skill with a folder in it, the folder holding the file that will stick
	if err := os.MkdirAll(filepath.Join(skillDir("pdf"), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(skillDir("pdf"), "sub/forms.md"), "forms one")
	// a copy, as where magpie can't link (#896: a copy left where links are
	// given is made a link again)
	ok(t)(SetSkillHow("", HowCopy))
	if linked(p) {
		t.Fatal("claude's pdf is still a link")
	}
	// sub/forms.md in the agent's copy can't be removed
	sub := filepath.Join(p, "sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	stuck := []string{}
	t.Cleanup(func() {
		for _, d := range append(stuck, sub) {
			os.Chmod(d, 0o755)
		}
	})

	write(t, filepath.Join(skillDir("pdf"), "SKILL.md"), "---\nname: pdf\ndescription: PDFs two\n---\n")
	write(t, filepath.Join(skillDir("pdf"), "sub/forms.md"), "forms two")
	ok(t)(Sync())
	if !ours(p, "pdf") {
		t.Fatal("the copy isn't magpie's after a swap with an undeletable file")
	}
	if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "PDFs two") {
		t.Fatalf("claude's copy after the update:\n%s", s)
	}
	if s := read(t, filepath.Join(p, "sub/forms.md")); s != "forms two" {
		t.Fatalf("sub/forms.md after the update: %q", s)
	}
	leftover := filepath.Join(filepath.Dir(p), ".pdf.magpie-old")
	if _, err := os.Lstat(leftover); err != nil {
		t.Fatalf("the old copy that couldn't be removed: %v", err)
	}
	stuck = append(stuck, filepath.Join(leftover, "sub"))

	// a later sync still knows the copy and is not refused
	ok(t)(Sync())
	if !ours(p, "pdf") {
		t.Fatal("the copy isn't magpie's on the next sync")
	}

	// once the file can be removed, the next sync clears what was left
	os.Chmod(filepath.Join(leftover, "sub"), 0o755)
	ok(t)(Sync())
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the old copy is still there after a sync: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("claude's skills folder: %v", names)
	}
}

// An agent in WSL set to take copies gets its copy made again through the
// same swap: with a file in its old copy that can't be removed, it still
// gets the whole new copy, magpie still knows it for its own, and the
// leftover is cleared by a later sync.
func TestWSLSkillCopySwapSurvivesAnUndeletableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only folders don't stop removal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can remove files from a read-only folder")
	}
	h := wslSandbox(t)
	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	write(t, filepath.Join(src, "pdf/sub/forms.md"), "forms one")
	ok(t)(SetSkillHow(wslCodex, HowCopy))
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{wslCodex}))
	p := filepath.Join(h, ".codex/skills/pdf")
	if linked(p) || !ours(p, "pdf") || read(t, filepath.Join(p, "sub/forms.md")) != "forms one" {
		t.Fatal("codex@wsl didn't get magpie's copy")
	}
	sub := filepath.Join(p, "sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(filepath.Dir(p), ".pdf.magpie-old")
	t.Cleanup(func() {
		os.Chmod(sub, 0o755)
		os.Chmod(filepath.Join(leftover, "sub"), 0o755)
	})

	write(t, filepath.Join(src, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Changed\n---\n")
	write(t, filepath.Join(src, "pdf/sub/forms.md"), "forms two")
	res := ok(t)(Sync())
	if !slices.Contains(res.Changed, wslCodex) {
		t.Errorf("changed: %v", res.Changed)
	}
	if !ours(p, "pdf") {
		t.Fatal("codex@wsl's copy isn't magpie's after a swap with an undeletable file")
	}
	if !strings.Contains(read(t, filepath.Join(p, "SKILL.md")), "Changed") || read(t, filepath.Join(p, "sub/forms.md")) != "forms two" {
		t.Fatal("codex@wsl's copy wasn't made again whole")
	}
	if _, err := os.Lstat(leftover); err != nil {
		t.Fatalf("the old copy that couldn't be removed: %v", err)
	}

	// once the file can be removed, the next sync clears what was left
	os.Chmod(filepath.Join(leftover, "sub"), 0o755)
	ok(t)(Sync())
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the old copy is still there after a sync: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		t.Fatalf("codex@wsl's skills folder has %d entries", len(entries))
	}
}
