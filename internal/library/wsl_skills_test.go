package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
)

// The copies each agent in WSL got of its own before (cutemmuu on X) are
// links to the distro's one copy after the next sync: one edited in an
// agent, its edit the newest, goes to the library and every agent first;
// one whose edit lost is kept with the backups; a skill folder of the
// user's own beside them, and one they put where magpie's copy goes, are
// never touched.
func TestWSLOldCopiesBecomeLinks(t *testing.T) {
	h := wslSandbox(t)
	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "csv"), "csv", "Read CSVs")
	// what magpie gave them before: a marked copy in each
	for _, id := range []string{wslCodex, wslClaude, wslPi} {
		ok(t)(SetSkillHow(id, HowCopy))
	}
	ok(t)(InstallSkills(src, []string{"pdf", "csv"}, []string{"claude", wslCodex, wslClaude, wslPi}))
	cx, cl, pi := filepath.Join(h, ".codex/skills"), filepath.Join(h, ".claude/skills"), filepath.Join(h, ".pi/agent/skills")
	for _, d := range []string{cx, cl, pi} {
		if isLink(t, filepath.Join(d, "pdf")) || !ours(filepath.Join(d, "pdf"), "pdf") {
			t.Fatalf("%s: no copy of the old kind to start from", d)
		}
	}
	// Codex's pdf edited in the distro; Claude Code's csv edited, then the
	// library's after it, so Claude Code's loses
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(cx, "pdf/SKILL.md"), "---\nname: pdf\ndescription: Edited in WSL Codex\n---\n")
	write(t, filepath.Join(cl, "csv/SKILL.md"), "---\nname: csv\ndescription: Edited in WSL Claude\n---\n")
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(src, "csv/SKILL.md"), "---\nname: csv\ndescription: Edited by hand\n---\n")
	// the user's own skill beside them, and a folder of theirs where
	// magpie's copy of pdf would go
	write(t, filepath.Join(cl, "mine/SKILL.md"), "---\nname: mine\ndescription: Mine\n---\n")
	store := filepath.Join(h, ".local/share/magpie/skills")
	write(t, filepath.Join(store, "csv/SKILL.md"), "the user's own csv")

	// the update: no way of their own set any more, as a user who never
	// picked one has
	var problems []Problem
	for _, id := range []string{wslCodex, wslClaude, wslPi} {
		res, err := SetSkillHow(id, "")
		if err != nil {
			t.Fatal(err)
		}
		problems = append(problems, res.Problems...)
	}
	// the user's csv in magpie's place is said, for each agent given csv
	for _, p := range problems {
		if p.What != "skill:csv" || !strings.Contains(p.Error, "in the way of magpie's copy of csv") {
			t.Errorf("problem: %+v", p)
		}
	}
	t.Logf("problems: %+v", problems)
	for _, d := range []string{cx, cl, pi} {
		p := filepath.Join(d, "pdf")
		if to, err := os.Readlink(p); err != nil || to != filepath.Join(store, "pdf") {
			t.Errorf("%s: not a link to the distro's copy: %q %v", p, to, err)
		}
		if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Edited in WSL Codex") {
			t.Errorf("%s lost Codex's edit:\n%s", p, s)
		}
	}
	if s := read(t, filepath.Join(src, "pdf/SKILL.md")); !strings.Contains(s, "Edited in WSL Codex") {
		t.Errorf("the library didn't take Codex's edit:\n%s", s)
	}
	if !backedUp(t, "Edited in WSL Claude") {
		t.Error("Claude Code's edit, which lost, wasn't kept")
	}
	if s := read(t, filepath.Join(h, ".claude/skills/mine/SKILL.md")); !strings.Contains(s, "Mine") {
		t.Errorf("the user's own skill: %q", s)
	}
	// csv's place in the store is the user's: nothing is put there, and
	// the agents keep what they had of it
	if s := read(t, filepath.Join(store, "csv/SKILL.md")); s != "the user's own csv" {
		t.Errorf("the user's folder in magpie's place was written over: %q", s)
	}
	for _, d := range []string{cx, cl, pi} {
		if p := filepath.Join(d, "csv"); isLink(t, p) || !ours(p, "csv") {
			t.Errorf("%s: what it had of csv is gone", p)
		}
	}
	if s := read(t, filepath.Join(home(), ".claude/skills/pdf/SKILL.md")); !strings.Contains(s, "Edited in WSL Codex") {
		t.Errorf("this machine's Claude Code:\n%s", s)
	}
}

// "User edits it, then sync": an edit made in an agent in WSL, through its
// link, is in the distro's copy, and the sync takes it into the library
// rather than making the copy again over it; every agent has it after.
func TestWSLEditThroughLinkReachesTheLibrary(t *testing.T) {
	h := wslSandbox(t)
	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", wslCodex, wslClaude}))
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(h, ".codex/skills/pdf/SKILL.md"), "---\nname: pdf\ndescription: Edited through the link\n---\n")
	for range 2 { // a sync, and the one of the next start
		ok(t)(Sync())
		for _, p := range []string{filepath.Join(src, "pdf"), filepath.Join(home(), ".claude/skills/pdf"), filepath.Join(h, ".claude/skills/pdf")} {
			if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Edited through the link") {
				t.Fatalf("%s after a sync:\n%s", p, s)
			}
		}
	}
	if !isLink(t, filepath.Join(h, ".codex/skills/pdf")) {
		t.Error("Codex's link was made a copy")
	}
}

// A skill an agent in WSL had of its own, the very same files, gives way
// to the link, kept aside; and a skill no agent there is given any more
// that was edited in the distro's copy is kept, not deleted.
func TestWSLStoreSweepKeepsEdits(t *testing.T) {
	h := wslSandbox(t)
	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{wslCodex}))
	store := filepath.Join(h, ".local/share/magpie/skills/pdf")
	// edited in the distro's copy and unticked in one change: the edit is
	// taken first, then the copy goes
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(store, "SKILL.md"), "---\nname: pdf\ndescription: Edited in the store\n---\n")
	ok(t)(SkillAgents("pdf", []string{}))
	if _, err := os.Lstat(store); !os.IsNotExist(err) {
		t.Errorf("the distro's copy is still there: %v", err)
	}
	if s := read(t, filepath.Join(src, "pdf/SKILL.md")); !strings.Contains(s, "Edited in the store") && !backedUp(t, "Edited in the store") {
		t.Error("the edit in the distro's copy was lost")
	}
}

// On a real WSL (MAGPIE_WSL_TEST_DISTRO, on Windows): the agents of a
// distro whose home is a folder of the test's own in /tmp there get links
// the distro makes and follows, which magpie reads back through wsl.exe
// (Windows can't), so a sync with nothing new changes nothing; an edit
// made in the distro's copy reaches the library; a way of copies takes
// the link away for a copy and back; unticked, the links and the copy go.
func TestWSLSkillsLinkedLive(t *testing.T) {
	name := os.Getenv("MAGPIE_WSL_TEST_DISTRO")
	if name == "" || runtime.GOOS != "windows" {
		t.Skip("MAGPIE_WSL_TEST_DISTRO not set")
	}
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32")+string(os.PathListSeparator)+os.Getenv("PATH"))
	sh := func(script string, args ...string) string {
		t.Helper()
		cmd := exec.Command("wsl.exe", append([]string{"-d", name, "--exec", "sh", "-c", script, "sh"}, args...)...)
		cmd.Env = append(os.Environ(), "WSL_UTF8=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v %s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := "/tmp/magpie-wsl-skills-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sh(`mkdir -p -- "$1/.codex" "$1/.claude" && printf 'model = "m"\n' > "$1/.codex/config.toml" && printf '{}\n' > "$1/.claude/settings.json"`, base)
	t.Cleanup(func() { sh(`rm -rf -- "$1"`, base) })
	sandbox(t)
	// the real wsl.exe, after the sandbox's PATH
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32")+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := `\\wsl.localhost\` + name
	if _, err := os.Stat(root + `\`); err != nil {
		root = `\\wsl$\` + name
	}
	t.Cleanup(agent.FakeWSL(map[string]string{name: "home:" + base + "\ndir:.codex\ndir:.claude\n"}, map[string]string{name: root}))
	t.Cleanup(agent.FakeWSLRealLinks())
	codex, claude := "codex@wsl:"+name, "claude@wsl:"+name
	h := filepath.Join(root, filepath.FromSlash(base))

	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", codex, claude}))
	for _, d := range []string{"/.codex/skills/pdf", "/.claude/skills/pdf"} {
		if got := sh(`readlink -- "$1"`, base+d); got != base+"/.local/share/magpie/skills/pdf" {
			t.Fatalf("%s leads to %q", d, got)
		}
		if got := sh(`cat -- "$1/SKILL.md"`, base+d); !strings.Contains(got, "Read PDFs") {
			t.Fatalf("the distro reads through %s: %q", d, got)
		}
		if p := filepath.Join(h, filepath.FromSlash(d)); !ours(p, "pdf") || !linked(p) {
			t.Fatalf("%s isn't magpie's link, as Windows reads it", p)
		}
	}
	if res := ok(t)(Sync()); len(res.Changed) != 0 {
		t.Errorf("a sync with nothing new wrote %v", res.Changed)
	}
	if v := viewOf(t, codex); v == nil || v.How != HowLink || v.MustCopy {
		t.Errorf("Codex's row: %+v", v)
	}

	// "user edits it, then sync", in the distro, through the link
	time.Sleep(1100 * time.Millisecond)
	sh(`printf -- '---\nname: pdf\ndescription: Edited in WSL\n---\n' > "$1/SKILL.md"`, base+"/.codex/skills/pdf")
	ok(t)(Sync())
	for _, p := range []string{filepath.Join(src, "pdf"), filepath.Join(home(), ".claude/skills/pdf")} {
		if s := read(t, filepath.Join(p, "SKILL.md")); !strings.Contains(s, "Edited in WSL") {
			t.Errorf("%s after the edit in WSL:\n%s", p, s)
		}
	}

	// copies for Codex: its link goes for a copy, and back
	ok(t)(SetSkillHow(codex, HowCopy))
	if got := sh(`[ -L "$1" ] && echo link || { [ -f "$1/.magpie-library" ] && echo copy; }`, base+"/.codex/skills/pdf"); got != "copy" {
		t.Fatalf("Codex given copies has %q", got)
	}
	ok(t)(SetSkillHow(codex, ""))
	if got := sh(`readlink -- "$1"`, base+"/.codex/skills/pdf"); got != base+"/.local/share/magpie/skills/pdf" {
		t.Fatalf("Codex's copy made a link again leads to %q", got)
	}

	ok(t)(SkillAgents("pdf", []string{"claude"}))
	if got := sh(`for p in "$@"; do { [ -e "$p" ] || [ -L "$p" ]; } && echo "$p"; done; true`,
		base+"/.codex/skills/pdf", base+"/.claude/skills/pdf", base+"/.local/share/magpie/skills/pdf"); got != "" {
		t.Errorf("still there once unticked: %s", got)
	}
}
