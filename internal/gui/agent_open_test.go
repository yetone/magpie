package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
)

// Open in terminal on an Agents row (coolmantou on Discord): the page
// sends only the agent's id, and the line Terminal runs is made here from
// the agent's own program. A path that spells shell commands runs that
// program and nothing else, and an id that isn't an agent's opens nothing.
func TestAgentOpenRunsOnlyTheAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the .command file is a POSIX shell's")
	}
	sandboxHome(t)
	// the folder Claude Code's program is in, named to run commands if
	// its path went into the line unquoted
	dir := filepath.Join(t.TempDir(), `it's a' $(touch made1) `+"`touch made2`"+`; touch made3 #`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "claude")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	proc.Forget()
	t.Cleanup(proc.Forget)

	var opened []string
	was := agentTerminal
	agentTerminal = func(run, label, choice string) error { opened = append(opened, run); return nil }
	t.Cleanup(func() { agentTerminal = was })

	h := Handler(nil, nil)
	post := func(id string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"agent": id})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/open", strings.NewReader(string(b))))
		return rec.Code
	}
	// an id that isn't an agent's, whatever it spells, opens nothing
	for _, id := range []string{"claude; touch made4", "claude'$(touch made5)'", "cl"} {
		if code := post(id); code == http.StatusNoContent || len(opened) != 0 {
			t.Fatalf("%q: %d, opened %q", id, code, opened)
		}
	}

	var row *agentJSON
	s := state()
	for i := range s.Agents {
		if s.Agents[i].ID == "claude" {
			row = &s.Agents[i]
		}
	}
	if row == nil {
		t.Fatalf("claude not detected on PATH %q", os.Getenv("PATH"))
	}
	if !row.Terminal || row.Command == "" {
		t.Fatalf("the row's terminal is %v on %s", row.Terminal, runtime.GOOS)
	}

	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if code := post("claude"); code != http.StatusNoContent || len(opened) != 1 {
		t.Fatalf("claude on %s: %d, opened %q", runtime.GOOS, code, opened)
	}
	run := opened[0]
	if run != agentCommand(a) || run != row.Command {
		t.Fatalf("opened %q, the row copies %q", run, row.Command)
	}

	// the line run as the .command file runs it, in a folder of its own
	work := t.TempDir()
	cmd := proc.Command("/bin/sh", "-c", run)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	for _, d := range []string{work, dir} {
		if made, _ := filepath.Glob(filepath.Join(d, "made*")); len(made) != 0 {
			t.Fatalf("%q ran more than the agent: %q", run, made)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(work, "ran")); err != nil || string(b) != "ran\n" {
		t.Fatalf("%q didn't run the agent's program: %q (%v %s)", run, b, err, out)
	}
}

// A desktop app's row has no command to open, and neither has an agent in
// a WSL distro off Windows: no Open in terminal.
func TestAgentCommandNeedsAProgram(t *testing.T) {
	sandboxHome(t)
	proc.Forget()
	t.Cleanup(proc.Forget)
	wsl := &agent.Agent{ID: "claude@wsl:Ubuntu", Bin: "claude", WSL: "Ubuntu"}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if run := agentLine(&agent.Agent{ID: "app", Name: "An app"}, "", goos); run != "" {
			t.Errorf("app on %s: %q", goos, run)
		}
		if run := agentLine(wsl, "", goos); (run != "") != (goos == "windows") {
			t.Errorf("wsl on %s: %q", goos, run)
		}
	}
	// a program name a shell would read more into is not sent into a distro
	if run := agentLine(&agent.Agent{ID: "x@wsl:U", Bin: "claude;rm", WSL: "U"}, "", "windows"); run != "" {
		t.Errorf("wsl with a bad name: %q", run)
	}
	// one that starts on magpie by its environment (agy) opens on that
	if run := agentCommand(&agent.Agent{ID: "agy", Bin: "agy", Launch: func() string { return "X=1 agy" }}); run != "X=1 agy" {
		t.Errorf("agy: %q", run)
	}
}

// Codex's Open the Codex app (Yink88564391 on X) opens the desktop app where one
// is installed and the setting codexOpen doesn't say terminal; the row menu's
// Open in terminal still opens the terminal; a value the setting can't
// take is refused.
func TestCodexOpenButton(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in codex is a POSIX script")
	}
	sandboxHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\necho codex-cli 0.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	proc.Forget()
	t.Cleanup(proc.Forget)
	var apps, terms []string
	wasApp, wasTerm := codexAppOpener, agentTerminal
	t.Cleanup(func() { codexAppOpener, agentTerminal = wasApp, wasTerm })
	codexAppOpener = func(context.Context) error { apps = append(apps, "app"); return nil }
	agentTerminal = func(run, label, choice string) error { terms = append(terms, run); return nil }
	wasInst := codexAppInstalled
	t.Cleanup(func() { codexAppInstalled = wasInst })
	codexAppInstalled = func() bool { return true }

	h := Handler(nil, nil)
	post := func(path string, body any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(string(b))))
		return rec.Code
	}
	row := func() agentJSON {
		for _, a := range state().Agents {
			if a.ID == "codex" {
				return a
			}
		}
		return agentJSON{}
	}

	if !row().OpenApp {
		t.Fatal("app installed and no choice, yet Codex does not open in the app")
	}
	if code := post("/api/agents/open", map[string]any{"agent": "codex"}); code != http.StatusNoContent || len(apps) != 1 {
		t.Fatalf("Open: %d, apps %v", code, apps)
	}
	if code := post("/api/settings/codex-open", map[string]string{"open": "browser"}); code == http.StatusOK {
		t.Fatal("an Open that opens a browser was accepted")
	}
	if code := post("/api/settings/codex-open", map[string]string{"open": "terminal"}); code != http.StatusOK {
		t.Fatalf("terminal: %d", code)
	}
	if row().OpenApp {
		t.Fatal("terminal chosen, yet the button is the app's")
	}
	post("/api/agents/open", map[string]any{"agent": "codex"})
	if len(apps) != 1 {
		t.Fatalf("terminal chosen, yet the app opened: %v", apps)
	}
	post("/api/settings/codex-open", map[string]string{"open": "app"})
	post("/api/agents/open", map[string]any{"agent": "codex", "terminal": true})
	if len(apps) != 1 {
		t.Fatalf("Open in terminal opened the app: %v", apps)
	}
}

// revealHost is the app's windows, keeping what they were asked to show.
type revealHost struct {
	folderOnly
	shown *[]string
}

func (h revealHost) RevealFolder(p string) error { *h.shown = append(*h.shown, p); return nil }
func (h revealHost) OpenFolder(p string) error   { *h.shown = append(*h.shown, p); return nil }

// Show settings file on an Agents row (InkCoalW on X): the file shown is
// the agent's own, as detected. The page sends only the id; an id that
// isn't an agent's, or a path, shows nothing, and the page served to a
// browser shows nothing on the computer it runs on.
func TestAgentRevealShowsOnlyItsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("claude here is a POSIX shell's program")
	}
	h := sandboxHome(t)
	dir := t.TempDir()
	prog := filepath.Join(dir, "claude")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	proc.Forget()
	t.Cleanup(proc.Forget)
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if a.Path == "" || !strings.HasPrefix(a.Path, h) {
		t.Fatalf("claude's file is %q, not in the sandbox %q", a.Path, h)
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var shown []string
	post := func(w Windows, id string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"agent": id})
		rec := httptest.NewRecorder()
		Handler(w, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/reveal", strings.NewReader(string(b))))
		return rec.Code
	}
	for _, id := range []string{"nope", a.Path, "../claude", ""} {
		if code := post(revealHost{shown: &shown}, id); code == http.StatusNoContent || len(shown) != 0 {
			t.Fatalf("%q: %d, shown %q", id, code, shown)
		}
	}
	if code := post(webTab{}, "claude"); code == http.StatusNoContent {
		t.Fatalf("the browser's page showed a file: %d", code)
	}
	if code := post(revealHost{shown: &shown}, "claude"); code != http.StatusNoContent || len(shown) != 1 || shown[0] != a.Path {
		t.Fatalf("claude: %d, shown %q, want %q", code, shown, a.Path)
	}
	// not written yet: its folder
	os.Remove(a.Path)
	shown = nil
	if code := post(revealHost{shown: &shown}, "claude"); code != http.StatusNoContent || len(shown) != 1 || shown[0] != filepath.Dir(a.Path) {
		t.Fatalf("claude with no file: %d, shown %q", code, shown)
	}
}
