package agent

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// fakeWSLScience is a distro's claude-science and its relay, as magpie
// runs them through wsl.exe (scienceWSLRun).
type fakeWSLScience struct {
	*fakeScience
	relay  []string // where the relay was started to, "" for each stop
	broken string   // what the relay script says when it fails
}

func (f *fakeWSLScience) run(name string, _ time.Duration, script string, args ...string) ([]byte, error) {
	if name != "Ubuntu" {
		panic("ran in " + name)
	}
	if script == scienceRelayScript {
		if f.broken != "" {
			return []byte(f.broken + "\n"), &os.PathError{Op: "wsl", Path: name, Err: os.ErrInvalid}
		}
		f.relay = append(f.relay, args[0]+" "+args[1])
		return nil, nil
	}
	if script != scienceWSLScript {
		panic("ran " + script)
	}
	var env []string
	if args[0] != "" {
		env = []string{scienceVar + "=" + args[0]}
	}
	return f.fakeScience.run(env, "claude-science", args[1:]...)
}

func scienceDistro(t *testing.T) distro {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	d := distro{Name: "Ubuntu", Home: "/home/me", Root: root, Running: true, Gateway: "172.17.32.1", Net: "nat",
		Has: map[string]bool{"bin:claude-science": true}, Probe: wslProbeVersion}
	if err := os.MkdirAll(d.local("/home/me/.claude-science"), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func fakeScienceIn(t *testing.T, d distro) *fakeWSLScience {
	f := &fakeWSLScience{fakeScience: &fakeScience{dir: d.local("/home/me/.claude-science"), running: true, pid: 40, port: 8123}}
	f.lock()
	oldRun, oldWait := scienceWSLRun, scienceWait
	t.Cleanup(func() { scienceWSLRun, scienceWait = oldRun, oldWait })
	scienceWSLRun, scienceWait = f.run, time.Second
	// this machine has none: nothing of it may be run
	oldBin := scienceBin
	t.Cleanup(func() { scienceBin = oldBin })
	scienceBin = func() string { return "" }
	scienceWSLKeeps.Lock()
	delete(scienceWSLKeeps.keep, d.Name)
	scienceWSLKeeps.Unlock()
	return f
}

// Claude Science in a WSL distro under NAT (shuitunlulu_ on Discord): the
// probe finds it in ~/.local/bin, and Connect restarts its daemon there
// through wsl.exe, given the relay magpie starts in the distro to
// Windows' address on the WSL network, with the WSL key in its path, as
// the daemon takes http only to loopback and sends its Claude sign-in as
// its key. The keeper starts it again there; Disconnect restarts it
// without the variable and stops the relay.
func TestClaudeScienceInWSLUnderNAT(t *testing.T) {
	if d := parseProbe("Ubuntu", "home:/home/me\nbin:claude-science /home/me/.local/bin/claude-science\n"); d == nil || !wslKindOf(ScienceID).found(*d) {
		t.Fatal("the probe's claude-science isn't found")
	}
	if !strings.Contains(wslProbeScript(), "wslcmd claude-science") {
		t.Error("the probe doesn't look for claude-science")
	}
	d := scienceDistro(t)
	f := fakeScienceIn(t, d)
	a := wslAgent(wslKindOf(ScienceID), d)
	if a.ID != "claude-science@wsl:Ubuntu" || !a.Detected() {
		t.Fatalf("row %q detected %v", a.ID, a.Detected())
	}
	field := a.Field("provider")
	if field.Get() != "" || a.Check() != "" {
		t.Fatal("connected before Connect")
	}
	if err := field.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	port := gateway.Port()
	key := gateway.WSLKey()
	if !strings.HasPrefix(key, "wsl-") {
		t.Fatalf("WSL key %q", key)
	}
	want := "http://127.0.0.1:" + port + "/" + key
	if got, ok := f.base(); !ok || got != want {
		t.Errorf("its daemon was started on %q (set %v), want %s", got, ok, want)
	}
	if to := net.JoinHostPort("172.17.32.1", port); !slices.Equal(f.relay, []string{port + " " + to}) {
		t.Errorf("relay %q, want one to %s", f.relay, to)
	}
	if wantCalls := []string{"status", "stop", "serve --detached --no-browser --port 8123", "status"}; !slices.Equal(f.calls, wantCalls) {
		t.Errorf("ran %q, want %q", f.calls, wantCalls)
	}
	if field.Get() != magpieID || a.Check() != "" {
		t.Errorf("after Connect: %q, check %q", field.Get(), a.Check())
	}
	// its own notice, not the one telling an agent under NAT to have the
	// gateway listen beyond loopback
	if n := a.Notice(); !strings.Contains(n, "Claude account") {
		t.Errorf("notice %q", n)
	}
	if _, err := os.Stat(scienceWSLMark("Ubuntu")); err != nil {
		t.Errorf("no mark of the distro's: %v", err)
	}
	if _, ok := readScienceMark(); ok {
		t.Error("this machine's Claude Science noted as connected")
	}
	if a.Launch() != "" {
		t.Errorf("launch %q: Windows can't start the distro's", a.Launch())
	}

	// WSL restarted: no daemon and no relay; the keeper starts both
	f.running, f.calls, f.relay = false, nil, nil
	f.lock()
	h := scienceInWSL(d)
	h.keepOnce()
	if got, _ := f.base(); !f.running || got != want || len(f.relay) != 1 {
		t.Errorf("keeper: running %v on %q, relay %q, ran %q", f.running, got, f.relay, f.calls)
	}
	// its own daemon running: the keeper sees the relay is up each time it
	// asks status
	f.calls, f.relay = nil, nil
	h.keep.asked = time.Time{}
	h.keepOnce()
	if !slices.Equal(f.calls, []string{"status"}) || len(f.relay) != 1 {
		t.Errorf("keeper on its own daemon ran %q, relay %q", f.calls, f.relay)
	}

	// no socat: Connect says so
	f.broken = "socat isn't installed in the distro (sudo apt install socat)"
	os.Remove(scienceWSLMark("Ubuntu"))
	if err := field.Set(magpieID); err == nil || !strings.Contains(err.Error(), "socat isn't installed") {
		t.Errorf("without socat: %v", err)
	}
	f.broken = ""
	if err := field.Set(magpieID); err != nil {
		t.Fatal(err)
	}

	f.calls, f.relay = nil, nil
	if err := field.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.base(); ok || !f.running {
		t.Errorf("after Disconnect: running %v env %q", f.running, f.env)
	}
	if !slices.Equal(f.relay, []string{port + " "}) {
		t.Errorf("relay after Disconnect %q, want stopped", f.relay)
	}
	if field.Get() != "" {
		t.Error("still connected")
	}
}

// In mirrored networking the distro's 127.0.0.1 is Windows': its daemon
// is given the gateway's own address, and no relay is started.
func TestClaudeScienceInWSLMirrored(t *testing.T) {
	d := scienceDistro(t)
	d.Mirrored, d.Net = true, "mirrored"
	f := fakeScienceIn(t, d)
	a := wslAgent(wslKindOf(ScienceID), d)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.base(); got != gateway.URL() || len(f.relay) != 0 {
		t.Errorf("mirrored: started on %q, relay %q", got, f.relay)
	}
	if n := a.Notice(); !strings.Contains(n, "Claude account") {
		t.Errorf("notice %q", n)
	}
}

// The keeper runs nothing in WSL while no distro's Claude Science is
// connected.
func TestClaudeScienceWSLKeeperIdle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := wslRun
	t.Cleanup(func() { wslRun = old })
	wslRun = func(time.Duration, ...string) ([]byte, error) { panic("wsl.exe run") }
	if hs := wslScienceHosts(); hs != nil {
		t.Errorf("hosts %v", hs)
	}
	if m := scienceWSLMark(`Ubuntu:24.04/x`); filepath.Base(m) != "claude-science-wsl-Ubuntu_24.04_x.json" {
		t.Errorf("mark %s", m)
	}
}

// Under NAT the relay leads to Windows' address on the WSL network: when
// Windows' firewall keeps WSL out there, the row says so with the rule
// that lets it in, as an agent pointed there itself is told; when WSL gets
// through, nothing is said.
func TestClaudeScienceInWSLFirewall(t *testing.T) {
	d := scienceDistro(t)
	fakeScienceIn(t, d)
	fakeWSLNet(t, "172.17.32.1/20")
	nat := "http://172.17.32.1:" + gateway.Port()
	fakeReach(t, map[string]bool{gateway.URL(): true, nat: true})
	var mu sync.Mutex
	inside := "no"
	was := wslRun
	t.Cleanup(func() { wslRun = was })
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if slices.Contains(args, wslReachScript) && slices.Contains(args, nat) {
			return []byte(inside + "\n"), nil
		}
		return nil, nil
	}
	a := wslAgent(wslKindOf(ScienceID), d)
	if dr := driftSettled(t, a); dr != nil {
		t.Fatalf("not connected: %+v", dr)
	}
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	dr := driftSettled(t, a)
	if dr == nil || !strings.Contains(dr.Detail, "firewall") || !strings.Contains(dr.Detail, "New-NetFirewallRule") {
		t.Fatalf("blocked from WSL: %+v", dr)
	}
	mu.Lock()
	inside = "ok"
	mu.Unlock()
	reachForget()
	if dr := driftSettled(t, a); dr != nil {
		t.Errorf("WSL gets through: %+v", dr)
	}
}
