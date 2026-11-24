package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// #7460 (Nine499): with Claude Code in WSL, its telemetry switches went to
// Windows' C:\Users\<user>\.claude\settings.json. The WSL agent has its own
// switch, in the distro's settings.json, and Settings › Privacy's switch
// for every agent reaches it there; Windows' file is never made. A distro
// that stopped is never started for it, and what the switch wrote there is
// taken out once it runs again.
func TestTelemetryInWSL(t *testing.T) {
	home, hostPath := telemetryHome(t, ".claude/settings.json", "")
	t.Setenv("PATH", t.TempDir()) // no Claude Code on this computer
	root := t.TempDir()
	const probe = "home:/home/me\ndir:.claude\nbin:claude\n"
	t.Cleanup(FakeWSL(map[string]string{"Ubuntu": probe}, map[string]string{"Ubuntu": root}))
	path := filepath.Join(root, "home", "me", ".claude", "settings.json")
	writeAt(t, path, `{"model": "opus"}`)
	const id = "claude@wsl:Ubuntu"
	twin := func() *Agent {
		t.Helper()
		a, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	hostUntouched := func(when string) {
		t.Helper()
		if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
			t.Fatalf("%s: Windows' settings.json was made: %v", when, err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
			t.Fatalf("%s: Windows' .claude was made: %v", when, err)
		}
	}
	keys := func(when string, want bool) {
		t.Helper()
		for _, k := range []string{"env.DISABLE_TELEMETRY", "env.DISABLE_ERROR_REPORTING"} {
			if v, ok := jsonAt(t, path, k); ok != want || (want && v != "1") {
				t.Fatalf("%s: %s in WSL = %v, %v:\n%s", when, k, v, ok, readFile(path))
			}
		}
		if v, _ := jsonAt(t, path, "model"); v != "opus" {
			t.Fatalf("%s: the user's model moved:\n%s", when, readFile(path))
		}
	}

	// its own row
	a := twin()
	if st := telemetryOf(t, a); st.Off || st.File != "/home/me/.claude/settings.json" {
		t.Fatalf("before: %+v", st)
	}
	if err := a.SetTelemetry(true); err != nil {
		t.Fatal(err)
	}
	keys("its row off", true)
	hostUntouched("its row off")
	if err := twin().SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
	keys("its row on", false)

	// Settings › Privacy's switch for every agent
	if err := SetTelemetryAll(true); err != nil {
		t.Fatal(err)
	}
	keys("every agent off", true)
	hostUntouched("every agent off")

	// a distro that stopped is asked first and never started
	live := twin()
	StopFakeWSL("Ubuntu")
	runs := FakeWSLRuns("Ubuntu")
	if _, err := live.Telemetry.Get(); err == nil {
		t.Fatal("a stopped distro's telemetry was read")
	}
	if err := SetTelemetryAll(false); err != nil {
		t.Fatal(err)
	}
	keys("every agent on, distro stopped", true)
	if !telemetryAllMarked() {
		t.Fatal("the stopped distro's mark was forgotten")
	}
	giveBackTelemetryAll()
	keys("given back while stopped", true)
	if got := FakeWSLRuns("Ubuntu"); got != runs {
		t.Fatalf("a stopped distro was started: %d to %d runs", runs, got)
	}

	// running again: what the switch wrote goes
	StartFakeWSL("Ubuntu")
	giveBackTelemetryAll()
	keys("given back once it runs", false)
	if telemetryAllMarked() {
		t.Fatal("the mark stayed")
	}
	hostUntouched("the end")
}
