package gui

import (
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
