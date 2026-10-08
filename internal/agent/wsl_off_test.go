package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
