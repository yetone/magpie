package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wslSettle waits for the listing of the WSL distros under way, if any.
func wslSettle() {
	wslSess.Lock()
	done := wslSess.done
	wslSess.Unlock()
	if done != nil {
		<-done
	}
}

// wslDistro is a distro's home in a temp dir with Claude Code's, Codex's
// and Pi's sessions in it, and this computer's own folders empty; WSLHomes
// says it runs while *running is true.
func wslDistro(t *testing.T) (home string, running *bool) {
	setup(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "own-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "own-codex"))
	home = filepath.Join(dir, "Ubuntu", "home", "me")
	copyTree(t, "testdata/claude", filepath.Join(home, ".claude"))
	copyTree(t, "testdata/codex", filepath.Join(home, ".codex"))
	copyTree(t, "testdata/pi", filepath.Join(home, ".pi", "agent"))
	on := true
	WSLHomes = func() []WSLHome { return []WSLHome{{Distro: "Ubuntu", Home: home, Running: on}} }
	t.Cleanup(func() { wslSettle(); WSLHomes = nil; Reset() })
	Reset()
	return home, &on
}

// The sessions of Claude Code, Codex and Pi in a WSL distro are listed with
// the distro, resumed there through wsl.exe; their calls count in usage.
func TestWSLSessionsListed(t *testing.T) {
	home, _ := wslDistro(t)
	ss := List(0)
	got := map[string]Session{}
	for _, s := range ss {
		if s.WSL != "Ubuntu" || !strings.HasPrefix(s.Path, home) {
			t.Fatalf("a session not from the distro: %+v", s)
		}
		got[s.Agent] = s
	}
	for _, a := range []string{"claude", "codex", "pi"} {
		if _, ok := got[a]; !ok {
			t.Fatalf("no %s session from WSL in %+v", a, ss)
		}
	}
	cc := got["claude"]
	if cc.ID != "11111111-2222-3333-4444-555555555555" || cc.Cwd != "/work/app" || cc.Tokens.zero() {
		t.Fatalf("claude: %+v", cc)
	}
	want := `wsl.exe -d 'Ubuntu' --cd '/work/app' -e sh -lc 'exec ${SHELL:-sh} -lic ''claude --resume 11111111-2222-3333-4444-555555555555'''`
	if cc.Resume != want {
		t.Fatalf("resume\n got %s\nwant %s", cc.Resume, want)
	}
	if r := got["codex"].Resume; !strings.HasPrefix(r, "wsl.exe -d 'Ubuntu' ") || !strings.Contains(r, "''codex resume ") {
		t.Fatalf("codex resume %q", r)
	}
	var calls int
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "claude" || c.Agent == "codex" {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("the distro's calls aren't counted")
	}
	dirs := strings.Join(Dirs(), "\n")
	if !strings.Contains(dirs, filepath.Join(home, ".claude")) || !strings.Contains(dirs, filepath.Join(home, ".codex")) {
		t.Fatalf("dirs %s", dirs)
	}

	// listed and resumed, never deleted from Windows
	for _, m := range ListAgent("claude") {
		if m.Deletable {
			t.Fatalf("a WSL session deletable: %+v", m)
		}
	}
	if _, err := Delete("claude", cc.ID); err == nil || !strings.Contains(err.Error(), "WSL Ubuntu") {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(cc.Path); err != nil {
		t.Fatal(err)
	}
}

// A stopped distro's sessions are listed as last read, and nothing in it is
// opened: that would start it.
func TestWSLStoppedDistroKept(t *testing.T) {
	home, running := wslDistro(t)
	if n := len(List(0)); n != 4 {
		t.Fatalf("want the 4 sessions, got %d", n)
	}
	wslSettle()
	Saved()
	// magpie starts again; the distro has stopped, and its files can't be
	// read (a read would start it): here they are gone
	*running = false
	Reset()
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ss := List(0)
		if len(ss) != 4 {
			t.Fatalf("pass %d: want the 4 sessions as last read, got %+v", i, ss)
		}
		for _, s := range ss {
			if s.WSL != "Ubuntu" || s.Tokens.zero() || s.Resume == "" {
				t.Fatalf("pass %d: %+v", i, s)
			}
		}
		wslSettle()
	}
}

// Off WSL (WSLHomes nil, as on macOS and Linux) nothing changes.
func TestWSLNoneWithoutHomes(t *testing.T) {
	setup(t)
	if WSLHomes != nil {
		t.Fatal("WSLHomes set in this package's tests")
	}
	for _, s := range List(0) {
		if s.WSL != "" {
			t.Fatalf("%+v", s)
		}
	}
	if r := ResumeCommand("claude", "abc", ""); r != "claude --resume abc" {
		t.Fatal(r)
	}
}
