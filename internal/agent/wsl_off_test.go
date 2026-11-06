package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// #1264 (Etsuya233): with Settings' Detect agents in WSL off, magpie asks
// wsl.exe nothing on its own: no distro is listed or probed, no agent of
// one is shown, and the Sessions page lists none of a distro's sessions,
// even one it listed while the setting was on. Turned on again, the
// distro's agents are back.
func TestWSLDetectionOff(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	home := filepath.Join(root, "home", "me")
	id := "44444444-2222-3333-4444-555555555555"
	project := filepath.Join(home, ".claude", "projects", "-home-me-app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"Tidy the README"},"uuid":"u1","timestamp":"2026-09-20T10:00:01.000Z","cwd":"/home/me/app","sessionId":"` + id + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(FakeWSL(map[string]string{"Ubuntu": "home:/home/me\ndir:.claude\n"}, map[string]string{"Ubuntu": root}))
	var asked []string
	run := wslRun
	wslRun = func(d time.Duration, args ...string) ([]byte, error) {
		asked = append(asked, strings.Join(args, " "))
		return run(d, args...)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	listed := func() bool {
		for _, s := range sessions.List(0) {
			if s.ID == id {
				return true
			}
		}
		return false
	}
	inWSL := func() int {
		n := 0
		for _, a := range All() {
			if a.WSL != "" {
				n++
			}
		}
		return n
	}

	// on (the default): the distro's Claude Code and its session are found
	if inWSL() == 0 || !listed() {
		t.Fatal("with detection on, the distro's agent or session isn't found")
	}

	if err := settings.Save(settings.Settings{NoWSLAgents: true}); err != nil {
		t.Fatal(err)
	}
	asked = nil
	if n := inWSL(); n != 0 {
		t.Fatalf("%d WSL agents with detection off", n)
	}
	if listed() {
		t.Fatal("a distro's session is listed with detection off")
	}
	if WSLRunning("Ubuntu") || len(wslHomes()) != 0 {
		t.Fatal("a distro is said to run, or its home given, with detection off")
	}
	if len(asked) != 0 {
		t.Fatalf("wsl.exe asked with detection off: %q", asked)
	}

	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	if inWSL() == 0 {
		t.Fatal("turned on again, the distro's agent isn't back")
	}
}

// #4872: with Settings' Detect agents in WSL off, the stand-in and
// classifier paths of a request ask wsl.exe nothing either: wslReady (for
// wslClaudeStandIn and wslCodexStandIn) and wslClaudeClassifier listed the
// running distros on their own, and on a machine without WSL Windows'
// inbox wsl.exe answers each of those with a console window asking to
// install it — one per Claude request.
func TestWSLStandInAsksNothingWhenOff(t *testing.T) {
	root, home := claudeDistroHome(t, "")
	fakeWSL(t, "Debian\r\n", "Debian\r\n", map[string]string{
		"Debian": "home:/home/me\ndir:.claude\nroute:default via 172.20.0.1 dev eth0\n",
	}, map[string]string{"Debian": root})
	nat := "http://172.20.0.1:" + gateway.Port()
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"),
		[]byte(`{"env":{"ANTHROPIC_BASE_URL":"`+nat+`","ANTHROPIC_MODEL":"a/main","ANTHROPIC_DEFAULT_HAIKU_MODEL":"a/small"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// every wsl.exe ask, the distro lists among them, not only a probe
	var asks []string
	run := wslRun
	wslRun = func(d time.Duration, args ...string) ([]byte, error) {
		asks = append(asks, strings.Join(args, " "))
		return run(d, args...)
	}

	// detection on: the distro's Claude Code's stand-in is found
	wslDistros()
	if got := wslClaudeStandIn("claude-haiku-4-5-20251001"); got != "a/small" {
		t.Fatalf("detection on, no stand-in: %q", got)
	}

	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	if err := settings.Save(settings.Settings{NoWSLAgents: true}); err != nil {
		t.Fatal(err)
	}
	asks = nil
	// the running list is asked again once it is older than wslRunningAge;
	// each of the three paths would ask it on its own
	stale := func() { wsl.Lock(); wsl.runAt = time.Time{}; wsl.Unlock() }
	stale()
	wslClaudeStandIn("claude-haiku-4-5-20251001")
	stale()
	wslCodexStandIn()
	stale()
	wslClaudeClassifier("claude-haiku-4-5-20251001")
	if len(asks) != 0 {
		t.Fatalf("wsl.exe asked with detection off: %q", asks)
	}
}

// #4872: agent's own wsl.exe runner (every list, probe, link and reach
// of a distro's) is refused at proc with detection off, past any gate of
// agent's that a path skips.
func TestWSLRunnerRefusedWhenOff(t *testing.T) {
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	if err := settings.Save(settings.Settings{NoWSLAgents: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wslRun(5*time.Second, "-l", "--running", "-q"); !errors.Is(err, proc.ErrWSLOff) {
		t.Fatalf("detection off, wslRun ran wsl.exe: %v", err)
	}
	if _, err := wslExec("Debian", "true"); !errors.Is(err, proc.ErrWSLOff) {
		t.Fatalf("detection off, wslExec ran wsl.exe: %v", err)
	}
}
