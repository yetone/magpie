package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// fakeScience is claude-science's daemon as its CLI tells it: status,
// stop and serve --detached, each one noted.
type fakeScience struct {
	dir       string // its data folder, where its lock file is
	running   bool
	pid, port int
	env       []string // the environment the daemon running was started in
	calls     []string
	// what its status says of the daemon running (nil: no daemon object,
	// as an older build), and the version of the program installed
	daemon             map[string]any
	version, installed string
}

func (f *fakeScience) run(env []string, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "status":
		if !f.running {
			return []byte(`{"running":false}`), nil
		}
		v := f.version
		if v == "" {
			v = "0.1.62"
		}
		return json.Marshal(map[string]any{"running": true, "pid": f.pid, "port": f.port, "version": v, "data_dir": "/x", "daemon": f.daemon})
	case "--version":
		return []byte("claude-science " + f.installed + " (release, public)\n"), nil
	case "stop":
		f.running, f.env = false, nil
	case "serve":
		f.running, f.pid, f.env, f.port = true, f.pid+1, env, 8000
		f.version, f.daemon = f.installed, map[string]any{"active_frames": 0, "active_conversations": 0, "launch_context": "terminal"}
		if i := slices.Index(args, "--port"); i > 0 {
			f.port = 0
			json.Unmarshal([]byte(args[i+1]), &f.port)
		}
	}
	f.lock()
	return nil, nil
}

// lock writes its lock file as the daemon does, removed when it stops.
func (f *fakeScience) lock() {
	path := filepath.Join(f.dir, "operon.lock")
	if !f.running {
		os.Remove(path)
		return
	}
	b, _ := json.Marshal(map[string]any{"pid": f.pid, "version": "0.1.62", "port": f.port, "sandbox_port": f.port + 1})
	os.WriteFile(path, b, 0o600)
}

func (f *fakeScience) base() (string, bool) {
	for _, kv := range f.env {
		if v, ok := strings.CutPrefix(kv, scienceVar+"="); ok {
			return v, true
		}
	}
	return "", false
}

// Claude Science is connected by restarting its daemon with
// ANTHROPIC_BASE_URL set to the gateway, on the port it was on, and by
// nothing else; disconnected by restarting it without the variable, one
// set in magpie's own environment too. A daemon magpie didn't start is
// drift, never stopped while a session runs in it; one not running is
// started while it is connected.
func TestClaudeScienceRestartsItsDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scienceVar, "https://elsewhere.example")
	f := &fakeScience{dir: t.TempDir(), running: true, pid: 40, port: 8123}
	f.lock()
	oldRun, oldBin, oldWait, oldDir := scienceRun, scienceBin, scienceWait, scienceDir
	t.Cleanup(func() { scienceRun, scienceBin, scienceWait, scienceDir = oldRun, oldBin, oldWait, oldDir })
	scienceRun, scienceBin, scienceWait = f.run, func() string { return "/bin/claude-science" }, time.Second
	scienceDir = func() string { return f.dir }
	resetScienceKeep(t)
	a, err := Find(ScienceID)
	if err != nil {
		t.Fatal(err)
	}
	field := a.Fields[0]
	if field.Get() != "" || a.Check() != "" || a.Notice() != "" {
		t.Fatal("connected before Connect")
	}

	if err := field.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.base(); !ok || got != gateway.URL() {
		t.Errorf("its daemon was started on %q (set %v), want %s", got, ok, gateway.URL())
	}
	if want := []string{"status", "stop", "serve --detached --no-browser --port 8123", "status"}; !slices.Equal(f.calls, want) {
		t.Errorf("ran %q, want %q", f.calls, want)
	}
	if field.Get() != magpieID || a.Check() != "" || !strings.Contains(a.Notice(), "Claude account") {
		t.Errorf("after Connect: %q, check %q, notice %q", field.Get(), a.Check(), a.Notice())
	}
	// set again, as Connect does after connecting (Pick): left running
	f.calls = nil
	if err := field.Set(magpieID); err != nil || !slices.Equal(f.calls, []string{"status"}) {
		t.Errorf("set again: %v, ran %q", err, f.calls)
	}
	if !strings.Contains(a.Launch(), scienceVar+"="+gateway.URL()) && !strings.Contains(a.Launch(), `$env:`+scienceVar+`="`+gateway.URL()) {
		t.Errorf("launch %q", a.Launch())
	}

	// the window, opened while none ran, started its own, and a session
	// runs in it: drift, and the keeper leaves it running
	f.pid, f.env, f.calls = 77, nil, nil
	f.daemon = map[string]any{"active_frames": 1, "active_conversations": 1, "launch_context": "app-child"}
	f.lock()
	if d := a.Check(); !strings.Contains(d, "without magpie's gateway") {
		t.Errorf("a daemon magpie didn't start: check %q", d)
	}
	f.calls = nil
	keepScienceOnce()
	if !slices.Equal(f.calls, []string{"status"}) || f.pid != 77 {
		t.Errorf("the keeper touched a daemon running: %q", f.calls)
	}
	// none running (the computer restarted): started on the gateway
	f.running, f.calls = false, nil
	f.lock()
	keepScienceOnce()
	if got, _ := f.base(); !f.running || got != gateway.URL() || a.Check() != "" {
		t.Errorf("not started again on the gateway: %q, %q", f.calls, got)
	}
	if f.port != 8123 {
		t.Errorf("started again on port %d, not the 8123 it was on", f.port)
	}

	// disconnected: running again, without the variable even where
	// magpie's own environment has it
	f.calls = nil
	if err := field.Set(""); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.base(); !f.running || ok {
		t.Errorf("after Disconnect: running %v, %s=%q", f.running, scienceVar, got)
	}
	if field.Get() != "" || a.Check() != "" || a.Notice() != "" {
		t.Errorf("still connected after Disconnect")
	}
	if _, err := os.Stat(scienceMarkPath()); !os.IsNotExist(err) {
		t.Errorf("the note is left: %v", err)
	}
	// and the keeper starts nothing now
	f.running, f.calls = false, nil
	f.lock()
	keepScienceOnce()
	if f.running || len(f.calls) != 0 {
		t.Errorf("started while disconnected: %q", f.calls)
	}
	// a disconnect with none running starts none
	writeScienceMark(scienceMark{URL: gateway.URL(), PID: 1})
	if err := field.Set(""); err != nil || f.running {
		t.Errorf("a disconnect started one: %v %q", err, f.calls)
	}
	// and with none installed there is nothing to connect
	scienceBin = func() string { return "" }
	if err := field.Set(magpieID); err == nil || field.Get() != "" || a.detect() {
		t.Errorf("connected with none installed: %v", err)
	}
}

// It is found where its installers put it: the Mac app's bundle, Windows'
// per-user Programs folder, ~/.local/bin.
func TestFindClaudeScience(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	if got := findScience(home); got != "" {
		t.Fatalf("found %q in an empty home", got)
	}
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join(home, "Applications", "Claude Science.app", "Contents", "Resources", "bin", "claude-science")
	case "windows":
		want = filepath.Join(home, "AppData", "Local", "Programs", "ClaudeScience", "claude-science.exe")
	default:
		want = filepath.Join(home, ".local", "bin", "claude-science")
	}
	os.MkdirAll(filepath.Dir(want), 0o755)
	if err := os.WriteFile(want, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findScience(home); got != want {
		t.Errorf("found %q, want %q", got, want)
	}
}

func resetScienceKeep(t *testing.T) {
	scienceKeep.asked, scienceKeep.lockPID, scienceKeep.bin, scienceKeep.retook = time.Time{}, 0, "", nil
	t.Cleanup(func() {
		scienceKeep.asked, scienceKeep.lockPID, scienceKeep.bin, scienceKeep.retook = time.Time{}, 0, "", nil
	})
}

// shuitunlulu_ on Discord: Claude Science went back off magpie after it
// was connected. Its app (0.1.72) attaches its window to a daemon running,
// and with none starts its own (launch_context app-child) without
// ANTHROPIC_BASE_URL; its "let the app run it instead", and its offer once
// the app updated past the daemon, do the same. The keeper puts magpie's
// daemon back, on the port the other was on, once no session runs in it:
// never a busy one, one whose status doesn't say, or one a service
// manager would start again; and stops after a few in an hour.
func TestClaudeScienceTakesBackFromItsApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := filepath.Join(t.TempDir(), "claude-science")
	os.WriteFile(bin, []byte("0.1.72"), 0o755)
	f := &fakeScience{dir: t.TempDir(), running: true, pid: 40, port: 8123, installed: "0.1.72"}
	f.lock()
	oldRun, oldBin, oldWait, oldDir := scienceRun, scienceBin, scienceWait, scienceDir
	t.Cleanup(func() { scienceRun, scienceBin, scienceWait, scienceDir = oldRun, oldBin, oldWait, oldDir })
	scienceRun, scienceBin, scienceWait = f.run, func() string { return bin }, time.Second
	scienceDir = func() string { return f.dir }
	resetScienceKeep(t)
	if err := scienceOn(true); err != nil {
		t.Fatal(err)
	}
	ours := f.pid
	keep := func() {
		t.Helper()
		f.calls = nil
		scienceKeep.asked = time.Time{} // a minute went by
		keepScienceOnce()
	}
	keep()
	if f.pid != ours || slices.Contains(f.calls, "stop") {
		t.Fatalf("its own daemon, on the gateway and current, was restarted: %q", f.calls)
	}

	// the app's window, opened while none ran, started its own
	app := func(daemon map[string]any) {
		f.pid, f.port, f.env, f.daemon = 900, 18481, nil, daemon
		f.lock()
	}
	busy := map[string]any{"active_frames": 1, "active_conversations": 1, "launch_context": "app-child"}
	app(busy)
	keep()
	if f.pid != 900 || slices.Contains(f.calls, "stop") {
		t.Errorf("a daemon a session runs in was stopped: %q", f.calls)
	}
	app(nil) // a status that doesn't say: unknown, left
	keep()
	if f.pid != 900 || slices.Contains(f.calls, "stop") {
		t.Errorf("a daemon whose status says nothing of its sessions was stopped: %q", f.calls)
	}
	app(map[string]any{"active_frames": 0, "active_conversations": 0, "launch_context": "launchd"})
	keep()
	if f.pid != 900 || slices.Contains(f.calls, "stop") {
		t.Errorf("a daemon launchd keeps was stopped: %q", f.calls)
	}
	idle := map[string]any{"active_frames": 0, "active_conversations": 0, "launch_context": "app-child"}
	app(idle)
	if d := scienceCheck(); !strings.Contains(d, "without magpie's gateway") {
		t.Errorf("check %q", d)
	}
	// the lock's pid changed: asked at once, not a minute later
	scienceKeep.asked = time.Now()
	scienceKeep.lockPID = ours
	f.calls = nil
	keepScienceOnce()
	if got, _ := f.base(); f.pid == 900 || got != gateway.URL() || f.port != 18481 {
		t.Errorf("not put back on the gateway on its port: %q, pid %d, base %q, port %d", f.calls, f.pid, got, f.port)
	}
	if m, _ := readScienceMark(); m.PID != f.pid || scienceCheck() != "" {
		t.Errorf("mark %+v, check %q", m, scienceCheck())
	}
	// and the lock unchanged within the minute: status isn't asked
	f.calls = nil
	keepScienceOnce()
	if len(f.calls) != 0 {
		t.Errorf("asked again with the lock unchanged: %q", f.calls)
	}

	// the app updated: its program is newer than magpie's daemon
	f.installed = "0.1.73"
	os.WriteFile(bin, []byte("0.1.73 build"), 0o755)
	before := f.pid
	keep()
	if f.pid == before || f.version != "0.1.73" {
		t.Errorf("an older daemon of magpie's was left after the app updated: %q", f.calls)
	}
	keep()
	if slices.Contains(f.calls, "--version") {
		t.Errorf("the version asked again with the program unchanged: %q", f.calls)
	}

	// put back three times in the hour (the app twice and the update):
	// one the user keeps putting back is theirs
	app(idle)
	keep()
	if f.pid == 900 {
		t.Fatalf("not put back a second time")
	}
	app(idle)
	keep()
	if f.pid != 900 || slices.Contains(f.calls, "stop") {
		t.Errorf("put back past %d in an hour: %q", scienceRetakes, f.calls)
	}

	// disconnected: the app's own is left
	scienceKeep.retook = nil
	os.Remove(scienceMarkPath())
	keep()
	if f.pid != 900 || len(f.calls) != 0 {
		t.Errorf("touched while disconnected: %q", f.calls)
	}
}

func TestOlderVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.1.72", "0.1.73", true}, {"0.1.9", "0.1.72", true}, {"0.1.72", "0.1.72", false}, {"0.2.0", "0.1.99", false}, {"", "0.1.72", false}, {"0.1.72", "", false}, {"x", "0.1.72", false}, {"0.1", "0.1.1", true}} {
		if got := olderVersion(c.a, c.b); got != c.want {
			t.Errorf("olderVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
