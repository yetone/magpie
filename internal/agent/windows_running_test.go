package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These are named TestDsh… on purpose: the Windows job in
// .github/workflows/test.yml compiles the whole suite but runs only
// `-run '^Test(Dsh|KeepDsh)'` on a real Windows, so a Windows-only check
// under any other name would be compiled on every platform and executed on
// none — it skips on Linux and macOS (GOOS), and Windows never selects it.

// Running reports whether a process whose command line matches a pattern is
// alive, by asking pgrep -f. Windows has no pgrep, so it answered no for
// every pattern — and every one of its 25 call sites is the advice an
// agent's own lists need after magpie changed what that agent reads at
// start ("Codex builds its model list at start-up — restart the Codex app",
// "New dsh sessions start on this; one already open keeps its model until
// you pick another in it"), each inside a Notice closure. So on Windows the
// Agents page wrote the file, said nothing, and a model picked in magpie
// looked like it had done nothing at all.
func TestDshRunningOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows answer is only given there")
	}
	if !Running(`(^|/)codex( |$)`) {
		t.Fatal("a pattern that may match was answered no: Windows can't tell")
	}
	if !Running(`(^|/)dsh( |$)`, `(^|/)codex( |$)`) {
		t.Fatal("the second of two patterns was answered no")
	}
	// no pattern is no question: a caller that asked nothing isn't told a
	// process is there
	if Running() {
		t.Fatal("no pattern was answered yes")
	}
}

// The two rows whose "an open one keeps its model" advice Windows dropped:
// asked there, a pick says what an open session keeps, so it is not left
// looking like it did nothing. claudeRunning and Pencil's own check answer
// yes where a machine can't be asked, which is why only the other 23 call
// sites were silenced.
func TestDshAndCodexAdviceOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows answer is only given there")
	}
	home, _, _ := dshRouteHome(t)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if n := a.Notice(); !strings.Contains(n, "open") {
		t.Fatalf("dsh says nothing about an open session: %q", n)
	}

	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if n := cx.Notice(); !strings.Contains(n, "restart") {
		t.Fatalf("Codex says nothing about restarting: %q", n)
	}
}

// On Windows Running says yes to every pattern, so Codex's restart advice is
// always one a shown notice carries. When Codex's own multi_agent_v2 is on in
// the routed config, the V2 warning is joined with that advice, not put in its
// place — otherwise a user who just switched models loses the "restart Codex"
// they need (#1028 review point 2).
func TestDshCodexV2WarningJoinsRestart(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows answer is only given there")
	}
	home, read := codexAgentsHome(t, "model = \"gpt-5.5\"\n\n[features]\nmulti_agent_v2 = true\n")
	cx := codex(home)
	route(t, cx, read)
	setAgentsV1(t, true)
	n := cx.Notice()
	if !strings.Contains(n, "multi_agent_v2") || !strings.Contains(n, "turn it off") {
		t.Fatalf("no V2 warning to join: %q", n)
	}
	if !strings.Contains(n, "restart") {
		t.Fatalf("the restart advice was lost when the V2 warning showed: %q", n)
	}
}

// Cline's desktop app and its CLI can't be told apart on Windows, where
// Running says yes to both: its advice names both, not the desktop app's
// alone, so one who uses the VS Code extension is still told to reload it.
func TestDshClineAdviceOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows answer is only given there")
	}
	n := cline(t.TempDir()).Notice()
	if !strings.Contains(n, "Reload VS Code") || !strings.Contains(n, "desktop app") {
		t.Fatalf("Cline's advice on Windows leaves one out: %q", n)
	}
}
