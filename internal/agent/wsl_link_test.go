package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// The probe asks the distro where each agent folder that is a link leads
// (#4502), with readlink -f there, and where Windows' drives are mounted:
// a folder that isn't a link isn't listed.
func TestWSLProbeLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe is a shell script")
	}
	home := filepath.Join(t.TempDir(), "home", "me")
	to := filepath.Join(t.TempDir(), "Users", "me", ".codex")
	for _, d := range []string{to, filepath.Join(home, ".claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(to, filepath.Join(home, ".codex")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", wslProbeScript())
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, _ := cmd.Output()
	d := parseProbe("U", string(out))
	real, _ := filepath.EvalSymlinks(to)
	if d == nil || d.Links[".codex"] != real {
		t.Fatalf("the probe said:\n%s\nread as %+v", out, d)
	}
	if _, ok := d.Links[".claude"]; ok {
		t.Fatalf("a folder that isn't a link is listed: %v", d.Links)
	}
}

// What the probe says of links and drives, and where magpie opens a path
// below each: a link to Windows' C: on C: itself, one elsewhere in the
// distro through its root, and one it couldn't resolve not at all. The
// agent is told the path through the link, as it knows it.
func TestWSLOpenThroughLinks(t *testing.T) {
	d := parseProbe("U", "home:/home/me\ndir:.codex\n"+
		"link:.codex /mnt/c/Users/me/.codex\nlink:.claude /data/claude/\nlink:.pi\nlink:.bashrc /etc/bashrc\n"+
		"win:/mnt/c C:\nwin:/mnt/wsl/d d:\nwin:/mnt/e\n")
	if d == nil {
		t.Fatal("no distro")
	}
	if _, ok := d.Links[".bashrc"]; ok {
		t.Errorf("a path the probe doesn't ask is taken: %v", d.Links)
	}
	if d.Links[".claude"] != "/data/claude" || d.Links[".pi"] != "" || d.Drives["/mnt/c"] != "C:" || d.Drives["/mnt/wsl/d"] != "D:" || len(d.Drives) != 2 {
		t.Fatalf("links %q, drives %q", d.Links, d.Drives)
	}
	sep := string(filepath.Separator)
	d.Root = filepath.Join(t.TempDir(), "U")
	t.Cleanup(FakeWSLDrives(map[string]string{"C:": filepath.Join(t.TempDir(), "C")}))
	c := wslDrive("C:")
	h := d.local(d.Home)
	for _, tc := range []struct{ in, want, err string }{
		{filepath.Join(h, ".codex"), c + filepath.Join("Users", "me", ".codex"), ""},
		{filepath.Join(h, ".codex", "skills", "pdf"), c + filepath.Join("Users", "me", ".codex", "skills", "pdf"), ""},
		{filepath.Join(h, ".claude", "settings.json"), d.local("/data/claude/settings.json"), ""},
		{filepath.Join(h, ".codexx", "a"), filepath.Join(h, ".codexx", "a"), ""},
		{filepath.Join(h, ".omo", "agent"), filepath.Join(h, ".omo", "agent"), ""},
		{filepath.Join(h, ".pi", "agent", "x"), "", "~/.pi in WSL U is a link magpie couldn't follow"},
	} {
		got, err := d.open(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("open %s: %q %v", tc.in, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("open %s: %q %v, want %q", tc.in, got, err, tc.want)
		}
		if n := d.native(got); n != d.native(tc.in) && !strings.HasPrefix(tc.in, filepath.Join(h, ".codexx")) {
			t.Errorf("native %s: %q, want %q", got, n, d.native(tc.in))
		}
	}
	if n := d.native(c + "Users" + sep + "me" + sep + ".codex" + sep + "AGENTS.md"); n != "/home/me/.codex/AGENTS.md" {
		t.Errorf("the agent is told %q", n)
	}
	// a probe before Drives: WSL's own /mnt/<letter>
	d.Drives = nil
	if got, _ := d.open(filepath.Join(h, ".codex", "x")); got != c+filepath.Join("Users", "me", ".codex", "x") {
		t.Errorf("no drives probed: %q", got)
	}
}

// linkedDistro is a running distro whose ~/.codex and ~/.claude are links
// to /mnt/c/Users/me/… that, as through \\wsl.localhost, can't be followed
// here: nothing below them can be made or read. C: has the real folders,
// with Claude Code's session.
func linkedDistro(t *testing.T, links string) (home, c string) {
	t.Helper()
	syncHome(t)
	root, drive := t.TempDir(), t.TempDir()
	home = filepath.Join(root, "home", "me")
	c = filepath.Join(drive, "Users", "me")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".codex", ".claude"} {
		if err := os.MkdirAll(filepath.Join(c, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/mnt/c/Users/me/"+d, filepath.Join(home, d)); err != nil {
			t.Skip("no symlinks here:", err)
		}
	}
	t.Cleanup(FakeWSL(map[string]string{"Ubuntu": "home:/home/me\ndir:.codex\ndir:.claude\n" + links + "win:/mnt/c C:\n"},
		map[string]string{"Ubuntu": root}))
	t.Cleanup(FakeWSLDrives(map[string]string{"C:": drive}))
	return home, c
}

func wslAgentByID(t *testing.T, id string) *Agent {
	t.Helper()
	for _, a := range wslAgents() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no %s in %v", id, wslAgents())
	return nil
}

// Codex in a distro whose ~/.codex leads to C: (#4502) is set up there:
// its config is read and written on C:, and its sessions are listed from
// it; Claude Code's sessions too.
func TestWSLAgentFolderIsALink(t *testing.T) {
	_, c := linkedDistro(t, "link:.codex /mnt/c/Users/me/.codex\nlink:.claude /mnt/c/Users/me/.claude\n")
	a := wslAgentByID(t, "codex@wsl:Ubuntu")
	if a.Unreached() != nil || a.Path != filepath.Join(c, ".codex", "config.toml") {
		t.Fatalf("Codex: path %q, unreached %v", a.Path, a.Unreached())
	}
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(c, ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(cfg), `model = "fake/m1"`) {
		t.Fatalf("Codex's config on C: (%v):\n%s", err, cfg)
	}

	id := "44444444-2222-3333-4444-555555555555"
	project := filepath.Join(c, ".claude", "projects", "-home-me-app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"Tidy the README"},"uuid":"u1","timestamp":"2026-09-20T10:00:01.000Z","cwd":"/home/me/app","sessionId":"` + id + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	found := false
	for _, s := range sessions.List(0) {
		found = found || s.ID == id && s.WSL == "Ubuntu"
	}
	if !found {
		t.Fatal("the session on C: behind ~/.claude isn't listed")
	}
}

// A link the distro couldn't resolve: the agent behind it says why, and
// no change of it is written anywhere.
func TestWSLAgentFolderLinkUnresolved(t *testing.T) {
	_, c := linkedDistro(t, "link:.codex\nlink:.claude /mnt/c/Users/me/.claude\n")
	a := wslAgentByID(t, "codex@wsl:Ubuntu")
	const why = "~/.codex in WSL Ubuntu is a link magpie couldn't follow"
	if err := a.Unreached(); err == nil || !strings.Contains(err.Error(), why) || a.Notice == nil || !strings.Contains(a.Notice(), why) {
		t.Fatalf("Codex behind a link not resolved: %v", err)
	}
	if a.Sync == nil {
		t.Fatal("no Sync")
	}
	if err := a.Sync(); err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("Sync: %v", err)
	}
	for _, f := range a.Fields {
		if f.Set != nil {
			if err := f.Set("x"); err == nil || !strings.Contains(err.Error(), why) {
				t.Fatalf("%s set: %v", f.Key, err)
			}
		}
	}
	if es, _ := os.ReadDir(filepath.Join(c, ".codex")); len(es) != 0 {
		t.Fatalf("written where the link was guessed to lead: %v", es)
	}
	if b := wslAgentByID(t, "claude@wsl:Ubuntu"); b.Unreached() != nil {
		t.Fatalf("Claude Code beside it: %v", b.Unreached())
	}
}
