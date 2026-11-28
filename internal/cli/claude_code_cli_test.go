package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/libc"
	"github.com/yetone/magpie/internal/settings"
)

// claudeCodeOut runs `magpie claude-code args…` and gives what it printed.
func claudeCodeOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(r)
		out.Write(b)
		close(done)
	}()
	err = claudeCodeCmd(args)
	w.Close()
	os.Stdout = old
	<-done
	r.Close()
	return out.String(), err
}

// `magpie claude-code install --yes` on a machine with no Claude Code (a
// container, Jorben on Discord) downloads the release's build, says from
// where and to where, and the subscription then runs it.
func TestClaudeCodeInstallDownloadsTheRelease(t *testing.T) {
	platform, err := claudecode.Platform(runtime.GOOS, runtime.GOARCH, false)
	if err != nil {
		t.Skip(err)
	}
	if libc.Musl() {
		t.Skip("a musl Linux asks for the -musl build")
	}
	const body = "#!/bin/sh\necho '2.1.296 (Claude Code)'\n"
	sum := sha256.Sum256([]byte(body))
	exe := map[bool]string{true: "claude.exe", false: "claude"}[runtime.GOOS == "windows"]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprint(w, "2.1.296")
		case "/2.1.296/manifest.json":
			fmt.Fprintf(w, `{"version":"2.1.296","manifestSignatureEnforcement":"flag","platforms":{%q:{"binary":%q,"checksum":%q,"size":%d}}}`,
				platform, exe, hex.EncodeToString(sum[:]), len(body))
		case "/2.1.296/" + platform + "/" + exe:
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldBase, oldOwn := claudecode.Base, claudeCode.own
	claudecode.Base, claudeCode.own = srv.URL, func() string { return "" }
	t.Cleanup(func() { claudecode.Base, claudeCode.own = oldBase, oldOwn; _ = claudecode.Remove() })

	out, err := claudeCodeOut(t)
	if err != nil || !strings.Contains(out, "magpie claude-code install") {
		t.Fatalf("status before: %q, %v", out, err)
	}
	out, err = claudeCodeOut(t, "install", "--yes")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, want := range []string{"2.1.296", srv.URL + "/2.1.296/" + platform + "/" + exe, claudecode.Root(), "SHA-256", "downloaded and checked"} {
		if !strings.Contains(out, want) {
			t.Errorf("install didn't say %q:\n%s", want, out)
		}
	}
	if p, v := claudecode.Downloaded(); v != "2.1.296" || p == "" {
		t.Fatalf("Downloaded() = %q, %q", p, v)
	}
	if out, _ := claudeCodeOut(t, "install", "--yes"); !strings.Contains(out, "downloaded already") {
		t.Errorf("a second install: %q", out)
	}
	if out, _ := claudeCodeOut(t); !strings.Contains(out, "downloaded by magpie") {
		t.Errorf("status after: %q", out)
	}
	if _, err := claudeCodeOut(t, "remove"); err != nil {
		t.Fatal(err)
	}
	if p, _ := claudecode.Downloaded(); p != "" {
		t.Errorf("remove left %s", p)
	}
}

// `magpie claude-code runs N` is the user's own bound on Claude Code
// processes (#2956), which the gateway's 503 names; auto gives it back to
// the machine's memory, and a value that isn't a count is refused and
// writes nothing.
func TestClaudeCodeRunsSetsTheUsersOwn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	noGateway(t)
	out, err := claudeCodeOut(t, "runs", "3")
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ClaudeRunsMost; got != 3 {
		t.Fatalf("claudeRunsMost is %d after `runs 3`", got)
	}
	if !strings.Contains(out, "3 Claude Code processes may run at once") || !strings.Contains(out, "your setting") {
		t.Fatalf("runs 3 said:\n%s", out)
	}
	for _, bad := range []string{"0", "-2", "lots", "--yes"} {
		if _, err := claudeCodeOut(t, "runs", bad); err == nil {
			t.Fatalf("runs %s was taken", bad)
		}
		if got := settings.Load().ClaudeRunsMost; got != 3 {
			t.Fatalf("runs %s wrote %d", bad, got)
		}
	}
	if _, err := claudeCodeOut(t, "runs", "--help"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ClaudeRunsMost; got != 3 {
		t.Fatalf("runs --help wrote %d", got)
	}
	if _, err := claudeCodeOut(t, "runs", "auto"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ClaudeRunsMost; got != 0 {
		t.Fatalf("runs auto left %d", got)
	}
	out, err = claudeCodeOut(t, "runs")
	if err != nil || strings.Contains(out, "your setting") {
		t.Fatalf("runs after auto said (%v):\n%s", err, out)
	}
}

// noGateway has `magpie claude-code runs` find no magpie running, never
// the one on this computer.
func noGateway(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	old := claudeCode.gateway
	claudeCode.gateway = func() string { return base }
	t.Cleanup(func() { claudeCode.gateway = old })
}

// `magpie claude-code runs` lists the Claude Code processes the running
// magpie has for Claude subscriptions, each with its pid, model, what it
// waits for, until when and its account, and says what they are: a task
// manager names them by Claude Code's version and "magpie", with nothing
// to tell why so many run (@Candydidi3 on X). With no magpie answering,
// it says none of its Claude Code runs.
func TestClaudeCodeRunsListsTheProcesses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	runs := gateway.ClaudeRuns{Object: "claude-runs", Live: 3, IdleMost: 6, IdleFor: time.Hour, Runs: []gateway.ClaudeRun{
		{PID: 4512, Model: "claude-opus-4-7", Account: "me@example.com", State: "answering", Since: now.Add(-12 * time.Second), Started: now.Add(-time.Minute)},
		{PID: 4520, Model: "claude-opus-4-7", Account: "me@example.com", State: "idle", Since: now.Add(-4 * time.Minute), Ends: now.Add(56*time.Minute + 30*time.Second), RSS: 180 << 20},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gateway.ClaudeRunsPath {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(runs)
	}))
	defer srv.Close()
	old := claudeCode.gateway
	claudeCode.gateway = func() string { return srv.URL }
	t.Cleanup(func() { claudeCode.gateway = old })
	out, err := claudeCodeOut(t, "runs")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"2 Claude Code running for Claude subscription conversations, 1 more for a test, warm-up or usage read",
		"pid 4512", "answering 12s",
		"pid 4520", "kept for its next turn 4m", "ends in 56m", "180.0 MB", "me@example.com",
		"claude-mcp-helper", "by its version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("runs says no %q:\n%s", want, out)
		}
	}
	noGateway(t)
	out, err = claudeCodeOut(t, "runs")
	if err != nil || !strings.Contains(out, "none of its Claude Code is running") {
		t.Fatalf("with no magpie running, runs said (%v):\n%s", err, out)
	}
}

// `magpie claude-code screen` prints what each hidden Claude Code shows
// now, as the gateway read it, its layout kept, to see where a run is
// stuck (FremontDango on Discord, #8075); with a pid, that run's alone.
// A -p run says why it has none; a magpie too old to read screens, and a
// pid magpie doesn't run, are said so.
func TestClaudeCodeScreenPrintsEachRunsScreen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	runs := gateway.ClaudeRuns{Object: "claude-runs", Runs: []gateway.ClaudeRun{
		{PID: 4512, Model: "claude-opus-5-5", Account: "me@example.com", State: "answering", Since: now.Add(-40 * time.Second),
			Screen: []string{"╭───────────────╮", "│ ✻ Thinking…   │", "╰───────────────╯"}},
		{PID: 4520, Model: "claude-sonnet-5", State: "idle", Since: now.Add(-time.Minute), ScreenWhy: "Claude Code runs as claude -p here, which draws no screen"},
	}}
	var asked atomic.Value
	var mu sync.Mutex // runs, changed between asks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gateway.ClaudeRunsPath {
			http.NotFound(w, r)
			return
		}
		asked.Store(r.URL.RawQuery)
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(runs)
	}))
	defer srv.Close()
	old := claudeCode.gateway
	claudeCode.gateway = func() string { return srv.URL }
	t.Cleanup(func() { claudeCode.gateway = old })
	out, err := claudeCodeOut(t, "screen")
	if err != nil || asked.Load() != "screen=1" {
		t.Fatalf("asked %v: %v", asked.Load(), err)
	}
	for _, want := range []string{"pid 4512", "answering 40s", "    │ ✻ Thinking…   │", "pid 4520", "draws no screen"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen says no %q:\n%s", want, out)
		}
	}
	out, err = claudeCodeOut(t, "screen", "4520")
	if err != nil || strings.Contains(out, "4512") || !strings.Contains(out, "pid 4520") {
		t.Fatalf("screen 4520 (%v):\n%s", err, out)
	}
	if _, err := claudeCodeOut(t, "screen", "99"); err == nil || !strings.Contains(err.Error(), "no Claude Code of magpie's has pid 99") {
		t.Fatalf("an unknown pid: %v", err)
	}
	if _, err := claudeCodeOut(t, "screen", "--yes"); err == nil {
		t.Fatal("a flag it doesn't know was taken")
	}
	mu.Lock()
	runs.Runs[0].Screen = nil
	mu.Unlock()
	if _, err := claudeCodeOut(t, "screen", "4512"); err == nil || !strings.Contains(err.Error(), "doesn't show Claude Code's screen") {
		t.Fatalf("an older magpie: %v", err)
	}
	mu.Lock()
	runs.Runs = nil
	mu.Unlock()
	if out, err := claudeCodeOut(t, "screen"); err != nil || !strings.Contains(out, "no Claude Code running") {
		t.Fatalf("none running (%v):\n%s", err, out)
	}
}
