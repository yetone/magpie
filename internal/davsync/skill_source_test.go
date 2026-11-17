package davsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// #5715 (JJasonsun on Discord): a library past a sync's size limit is
// said, never a silent success. A skill from a git repository goes as its
// source, and the other computer fetches it itself: its size doesn't
// count, and one that can't be fetched there is said until Update fetches
// it. A skill of the user's own too big to go is named.
func TestSyncSkillsBySourceAndLeftOut(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git isn't installed")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	big := strings.Repeat("x", 17<<20) // past a file's 16 MiB
	repo := t.TempDir()
	work := filepath.Join(repo, "work")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q", "--bare", "skills.git")
	run(repo, "init", "-q", work)
	for p, body := range map[string]string{
		"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n",
		"skills/pdf/big.txt":  big,
	} {
		p = filepath.Join(work, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "skills")
	run(work, "push", "-q", filepath.Join(repo, "skills.git"), "HEAD:refs/heads/main")
	addr := "file://" + filepath.ToSlash(filepath.Join(repo, "skills.git"))
	if !strings.HasPrefix(addr, "file:///") {
		addr = "file:///" + strings.TrimPrefix(addr, "file://") // C:/… on Windows
	}
	withGit := func(c computer) {
		t.Helper()
		if err := os.Symlink(git, filepath.Join(string(c), "bin", "git")); err != nil {
			t.Fatal(err)
		}
	}

	f, srv := newFakeS3(t)
	cfg := f.config(srv)
	cfg.Keys, cfg.Agents = false, false
	ctx := context.Background()
	a, b := newComputer(t), newComputer(t)

	// A: pdf from the repository, and a skill of its own too big to go
	a.use(t)
	withGit(a)
	if _, err := library.InstallSkills(addr, []string{"skills/pdf"}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := library.CreateSkill("mine", "my own", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library.Dir(), "skills", "mine", "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if v := Status(); v.Error != "" || !slices.Equal(v.LibraryLeft, []string{"mine"}) {
		t.Fatalf("A's sync: error %q, left out %v, want mine said", v.Error, v.LibraryLeft)
	}

	// B, with no git: pdf can't be fetched, and that is said
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	v := Status()
	if v.Error != "" || len(v.LibraryProblems) != 1 || !strings.HasPrefix(v.LibraryProblems[0], "pdf: not fetched") {
		t.Fatalf("B's sync: error %q, problems %q", v.Error, v.LibraryProblems)
	}
	// git here now: Update fetches it, the whole of it, and nothing is said
	withGit(b)
	if _, err := library.UpdateSkill("pdf", false); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(library.Dir(), "skills", "pdf", "big.txt")); err != nil || len(got) != len(big) {
		t.Fatalf("pdf's big.txt on B: %d bytes, %v", len(got), err)
	}
	if v := Status(); len(v.LibraryProblems) != 0 {
		t.Errorf("fetched, still said: %q", v.LibraryProblems)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if v := Status(); v.Error != "" || len(v.LibraryProblems) != 0 || !slices.Equal(v.LibraryLeft, []string{"mine"}) {
		t.Errorf("B's next sync: error %q, problems %q, left %v", v.Error, v.LibraryProblems, v.LibraryLeft)
	}
	// what B never had of mine isn't taken from A by B's sync
	a.use(t)
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(library.Dir(), "skills", "mine", "big.txt")); err != nil || len(got) != len(big) {
		t.Errorf("A's mine/big.txt after B's sync: %d bytes, %v", len(got), err)
	}
	if v := Status(); v.Error != "" || !slices.Equal(v.LibraryLeft, []string{"mine"}) {
		t.Errorf("A's next sync: error %q, left %v", v.Error, v.LibraryLeft)
	}
}
