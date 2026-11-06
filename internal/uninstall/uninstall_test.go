package uninstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/library"
)

// fakes puts a fake in for everything Run asks of the system, and records
// what it was asked, in order.
func fakes(t *testing.T, list []*agent.Agent, isWired map[string]bool, failing map[string]error) *[]string {
	t.Helper()
	var did []string
	wasAgents, wasWired, wasDisconnect := agents, wired, disconnectOne
	wasFailover, wasLeftovers, wasPort, wasRestart := endFailover, endLeftovers, portRollouts, restartCodexApp
	wasService, wasAutostart, wasShortcut, wasScheme, wasPII, wasExe := removeService, autostartOff, removeShortcut, removeScheme, endPII, executable
	t.Cleanup(func() {
		agents, wired, disconnectOne = wasAgents, wasWired, wasDisconnect
		endFailover, endLeftovers, portRollouts, restartCodexApp = wasFailover, wasLeftovers, wasPort, wasRestart
		removeService, autostartOff, removeShortcut, removeScheme, endPII, executable = wasService, wasAutostart, wasShortcut, wasScheme, wasPII, wasExe
	})
	agents = func() []*agent.Agent { return list }
	wired = func(a *agent.Agent) bool { return isWired[a.ID] }
	disconnectOne = func(a *agent.Agent) error {
		did = append(did, "disconnect "+a.ID)
		if err := failing[a.ID]; err != nil {
			return err
		}
		isWired[a.ID] = false
		return nil
	}
	endFailover = func() (bool, error) { did = append(did, "failover"); return true, nil }
	endLeftovers = func() (bool, error) { did = append(did, "leftovers"); return false, nil }
	portRollouts = func(time.Duration) { did = append(did, "rollouts") }
	restartCodexApp = func(context.Context, time.Duration) (int, bool, error) {
		did = append(did, "codex app")
		return 1, false, nil
	}
	step := func(name string) func() error {
		return func() error { did = append(did, name); return nil }
	}
	removeService = step("service")
	autostartOff = step("autostart")
	removeShortcut = func(string) error { did = append(did, "shortcut"); return nil }
	removeScheme = func(string) error { did = append(did, "scheme"); return nil }
	endPII = step("pii")
	executable = func() (string, error) { return "/opt/magpie/magpie", nil }
	return &did
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gone(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return errors.Is(err, os.ErrNotExist)
}

// Uninstalling stops the service before anything else (one still serving
// would wire agents again), disconnects only the agents magpie is in,
// restarts the Codex app's app-server when Codex was one of them, and
// removes magpie's files last.
func TestRunOrder(t *testing.T) {
	codex := &agent.Agent{ID: "codex", Name: "Codex"}
	claude := &agent.Agent{ID: "claude", Name: "Claude Code"}
	gemini := &agent.Agent{ID: "gemini", Name: "Gemini CLI"}
	did := fakes(t, []*agent.Agent{codex, claude, gemini}, map[string]bool{"codex": true, "claude": true}, nil)
	settings := filepath.Join(appdir.Config(), "settings.json")
	write(t, settings, "{}")
	write(t, filepath.Join(appdir.Cache(), "models.json"), "{}")

	r := Run(context.Background())

	want := []string{"service", "autostart", "shortcut", "scheme", "pii", "failover", "disconnect codex", "disconnect claude", "leftovers", "rollouts", "codex app"}
	if !slices.Equal(*did, want) {
		t.Fatalf("did %q, want %q", *did, want)
	}
	if r.Failed() || r.CodexApp {
		t.Fatalf("report: %+v", r)
	}
	if !gone(t, appdir.Config()) || !gone(t, appdir.Cache()) {
		t.Fatal("magpie's folders left")
	}
	if r.Exe != "/opt/magpie/magpie" {
		t.Fatalf("exe %q", r.Exe)
	}
	if len(r.Agents) != 2 || r.Agents[0].What != "Codex" || r.Agents[1].What != "Claude Code" {
		t.Fatalf("agents %+v", r.Agents)
	}
}

// KeepData, for install.sh --uninstall, does Run's agents and system and
// leaves magpie's files for a magpie installed again.
func TestKeepData(t *testing.T) {
	codex := &agent.Agent{ID: "codex", Name: "Codex"}
	did := fakes(t, []*agent.Agent{codex}, map[string]bool{"codex": true}, nil)
	settings := filepath.Join(appdir.Config(), "settings.json")
	write(t, settings, "{}")

	r := KeepData(context.Background())

	want := []string{"service", "autostart", "shortcut", "scheme", "pii", "failover", "disconnect codex", "leftovers", "rollouts", "codex app"}
	if !slices.Equal(*did, want) {
		t.Fatalf("did %q, want %q", *did, want)
	}
	if gone(t, settings) || len(r.Data) != 0 || r.Failed() {
		t.Fatalf("settings gone or data touched: %+v", r)
	}
}

// An agent that can't be disconnected is said, and the rest are still
// done; Codex not among them leaves the Codex app alone.
func TestRunSaysWhatFailed(t *testing.T) {
	claude := &agent.Agent{ID: "claude", Name: "Claude Code"}
	goose := &agent.Agent{ID: "goose", Name: "Goose"}
	did := fakes(t, []*agent.Agent{claude, goose}, map[string]bool{"claude": true, "goose": true}, map[string]error{"claude": errors.New("settings.json: permission denied")})
	r := Run(context.Background())
	if slices.Contains(*did, "codex app") {
		t.Fatalf("the Codex app restarted with no Codex connected: %q", *did)
	}
	if !slices.Contains(*did, "disconnect goose") {
		t.Fatalf("goose skipped after claude failed: %q", *did)
	}
	if !r.Failed() {
		t.Fatal("not failed")
	}
	says := strings.Join(r.Says(), "\n")
	if !strings.Contains(says, "! disconnected Claude Code: settings.json: permission denied") || !strings.Contains(says, "✓ disconnected Goose") {
		t.Fatalf("says:\n%s", says)
	}
}

// A Codex turn still running past the wait leaves the app-server alone,
// and the report tells the user to reopen the app.
func TestRunCodexAppLeft(t *testing.T) {
	codex := &agent.Agent{ID: "codex", Name: "Codex"}
	fakes(t, []*agent.Agent{codex}, map[string]bool{"codex": true}, nil)
	restartCodexApp = func(context.Context, time.Duration) (int, bool, error) { return 0, true, nil }
	if r := Run(context.Background()); !r.CodexApp {
		t.Fatal("CodexApp not said")
	}
}

// What no Disconnect could name (the marks gone with magpie's folder) goes
// even with no agent connected, says so, and restarts the Codex app's
// app-server for it alone; one that can't be taken is said.
func TestRunTakesCodexLeftovers(t *testing.T) {
	t.Run("taken", func(t *testing.T) {
		did := fakes(t, nil, map[string]bool{}, nil)
		endLeftovers = func() (bool, error) { return true, nil }
		r := Run(context.Background())
		if !slices.Contains(*did, "codex app") {
			t.Fatalf("the Codex app not restarted for its leftovers alone: %q", *did)
		}
		if len(r.Agents) != 1 || r.Agents[0].What != "Codex's leftover settings" || r.Agents[0].Err != "" {
			t.Fatalf("agents %+v", r.Agents)
		}
	})
	t.Run("failed", func(t *testing.T) {
		fakes(t, nil, map[string]bool{}, nil)
		endLeftovers = func() (bool, error) { return false, errors.New("config.toml: permission denied") }
		r := Run(context.Background())
		if r.CodexApp {
			t.Fatal("the Codex app restarted over a leftover that failed")
		}
		if !r.Failed() {
			t.Fatal("not failed")
		}
		if says := strings.Join(r.Says(), "\n"); !strings.Contains(says, "! disconnected Codex's leftover settings: config.toml: permission denied") {
			t.Fatalf("says:\n%s", says)
		}
	})
}

// Skills and servers the user's agents use from the Library stay; the
// rest of magpie's folder goes.
func TestDataKeepsLibraryInUse(t *testing.T) {
	fakes(t, nil, map[string]bool{}, nil)
	skill := filepath.Join(library.Dir(), "skills", "docx", "SKILL.md")
	write(t, skill, "# docx")
	settings := filepath.Join(appdir.Config(), "settings.json")
	write(t, settings, "{}")
	write(t, filepath.Join(appdir.Config(), "usage", "2026-10.db"), "x")

	r := &Report{}
	Data(r)
	if !gone(t, settings) || !gone(t, filepath.Join(appdir.Config(), "usage")) {
		t.Fatal("magpie's own files left")
	}
	if gone(t, skill) {
		t.Fatal("a skill in use removed")
	}
	if len(r.Kept) != 1 || r.Kept[0].What != library.Dir() {
		t.Fatalf("kept %+v", r.Kept)
	}

	// an empty Library goes with the rest
	os.RemoveAll(filepath.Join(library.Dir(), "skills", "docx"))
	r = &Report{}
	Data(r)
	if !gone(t, appdir.Config()) || len(r.Kept) != 0 {
		t.Fatalf("empty library kept: %+v", r)
	}
}

// Never the home, a root or a relative path, from a variable pointing at
// one; a folder inside another is listed once.
func TestFoldersNeverHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	remove, _ := Folders()
	if len(remove) == 0 {
		t.Fatal("nothing to remove")
	}
	for _, p := range remove {
		if !filepath.IsAbs(p) || within(home, p) {
			t.Fatalf("would remove %s (home %s)", p, home)
		}
		for _, q := range remove {
			if p != q && within(p, q) {
				t.Fatalf("%s listed inside %s", p, q)
			}
		}
	}
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	h := filepath.Join(root, "Users", "me")
	for p, ok := range map[string]bool{
		"":                                    false,
		"magpie":                              false,
		root:                                  false,
		h:                                     false,
		filepath.Join(root, "Users"):          false,
		filepath.Join(h, ".config", "magpie"): true,
		filepath.Join(root, "Users", "me2"):   true,
	} {
		if removable(p, h) != ok {
			t.Errorf("removable(%q) = %v", p, !ok)
		}
	}
}

// What the app starts as it quits removes magpie's files once the app is
// gone.
func TestAfterExit(t *testing.T) {
	fakes(t, nil, map[string]bool{}, nil)
	settings := filepath.Join(appdir.Config(), "settings.json")
	write(t, settings, "{}")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := AfterExit(cmd.Process.Pid, time.Minute)
	if time.Since(start) > 30*time.Second {
		t.Fatal("waited on a process that had ended")
	}
	if r.Failed() || !gone(t, settings) {
		t.Fatalf("report %+v", r)
	}
}
