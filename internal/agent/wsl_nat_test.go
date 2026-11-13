package agent

import (
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/wslrun"
)

// fakeWSLNet makes cidr the WSL network Windows has an address on.
func fakeWSLNet(t *testing.T, cidr string) {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	old := gateway.WSLNets
	gateway.WSLNets = func() []*net.IPNet { return []*net.IPNet{n} }
	t.Cleanup(func() { gateway.WSLNets = old })
}

// zaidu259434 on X: Codex in WSL (NAT) failed to connect with magpie's
// sharing off. At Windows' address on the WSL network, where the gateway
// now listens itself, every agent is given the WSL key, sharing on or off,
// and Codex, signed in or not, has it as its provider's key. Elsewhere off
// loopback it is the LAN key as before, and on loopback the token.
func TestWSLAgentsGetTheWSLKey(t *testing.T) {
	home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"gpt-5.5\"\n")
	fakeWSLNet(t, "172.20.0.1/20")
	nat := "http://172.20.0.1:" + gateway.Port()
	k := keyAt(nat)
	if k == gateway.Token || k != gateway.WSLKey() || !strings.HasPrefix(k, "wsl-") {
		t.Fatalf("key at the WSL network: %q", k)
	}
	if keyAt(gateway.URL()) != gateway.Token || keyAt("http://192.168.1.5:3425") != gateway.Token {
		t.Error("the WSL key given beyond the WSL network")
	}
	if !ourKey(k) || !gatewayKey(k) || !gatewayKey(agentKeyAt("qoder", nat)) || agentKeyAt("qoder", nat) != k+"-qoder" {
		t.Errorf("WSL keys not known as magpie's: %q", agentKeyAt("qoder", nat))
	}
	if oc := magpieProviderJSONAt("opencode", "opencode", nat).(map[string]any)["options"].(map[string]any); oc["apiKey"] != k {
		t.Errorf("OpenCode in WSL: %v", oc["apiKey"])
	}

	a := wslCodex(fakeDistro(home, false))
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `experimental_bearer_token = "`+k+`"`) {
		t.Fatalf("Codex in WSL not given the WSL key:\n%s", cfg)
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %s", c)
	}
}

// What answers at Windows' address on the WSL network is asked from inside
// the distro, never from Windows, where the gateway's own listener answers
// whatever the firewall lets in from WSL: a distro that can't connect is
// told so, with the firewall rule that lets it in; one that can, or can't
// tell (nothing to ask with), isn't. A stopped distro is never run.
func TestWSLReachAskedInsideTheDistro(t *testing.T) {
	home, _ := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"gpt-5.5\"\n")
	fakeWSLNet(t, "172.20.0.1/20")
	nat := "http://172.20.0.1:" + gateway.Port()
	// from Windows both answer: only the distro's answer can tell
	fakeReach(t, map[string]bool{gateway.URL(): true, nat: true})
	var mu sync.Mutex
	inside, runs := "no", 0
	was := wslRun
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if slices.Contains(args, wslReachScript) {
			runs++
			if args[1] != "Ubuntu-24.04" || !slices.Contains(args, nat) {
				t.Errorf("asked %v", args)
			}
			return []byte(inside + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { wslRun = was })
	d := fakeDistro(home, false)
	a := wslCodex(d)
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	dr := driftSettled(t, a)
	if dr == nil || dr.Kind != "unreachable" || dr.Addr != nat || !strings.Contains(dr.Detail, "firewall") || !strings.Contains(dr.Detail, "New-NetFirewallRule") || !strings.Contains(dr.Detail, "-Profile Any") {
		t.Fatalf("blocked from WSL: %+v", dr)
	}
	for _, said := range []string{"ok", "?"} {
		mu.Lock()
		inside = said
		mu.Unlock()
		reachForget()
		if dr := driftSettled(t, a); dr != nil {
			t.Errorf("WSL says %s: %+v", said, dr)
		}
	}
	// stopped: never run to be asked
	mu.Lock()
	inside, runs = "no", 0
	mu.Unlock()
	reachForget()
	d.up = func() bool { return false }
	off := wslCodex(d)
	if off.reachFrom == nil {
		t.Fatal("no probe")
	}
	if _, known := off.reachFrom(nat); known {
		t.Error("a stopped distro's answer known")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if runs != 0 {
		t.Errorf("a stopped distro was run %d times", runs)
	}
}

// H1_B1_K1 on X: a company's firewall kept WSL off Windows' address on
// vEthernet (WSL), while Windows' LAN address answered from WSL. Codex in
// a distro under NAT, found shut at the WSL network's address from inside
// the distro, is set again at the address of Windows' that answers there,
// with nothing asked of the user; the gateway listened at the others only
// while they were tried. Once the WSL network's address answers again and
// the via doesn't, it goes back there.
func TestWSLFirewalledFindsAnotherWay(t *testing.T) {
	home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"gpt-5.5\"\n")
	fakeWSLNet(t, "172.20.0.1/20")
	nat, lan := "http://172.20.0.1:"+gateway.Port(), "http://10.0.0.132:"+gateway.Port()
	fakeReach(t, map[string]bool{gateway.URL(): true, nat: true, lan: true})
	oldVia := gateway.WSLVia
	gateway.WSLVia = func() []net.IP { v, _ := wslrun.Via(); return v }
	t.Cleanup(func() { gateway.WSLVia = oldVia; gateway.SetWSLVia(nil, nil) })
	oldC, oldL := wslViaCandidates, wslViaListening
	var mu sync.Mutex
	var tried [][]net.IP
	wslViaCandidates = func() []string { return []string{"10.0.0.99", "10.0.0.132"} }
	wslViaListening = func(ip string) bool {
		mu.Lock()
		defer mu.Unlock()
		_, t := wslrun.Via()
		tried = append(tried, t)
		return true
	}
	t.Cleanup(func() { wslViaCandidates, wslViaListening = oldC, oldL })
	// inside the distro: only the LAN address answers
	answers := map[string]string{nat: "no", lan: "ok", "http://10.0.0.99:" + gateway.Port(): "no"}
	was := wslRun
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if slices.Contains(args, wslReachScript) {
			return []byte(answers[args[len(args)-3]] + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { wslRun = was })
	d := fakeDistro(home, false)
	wsl.Lock()
	seen := wsl.seen
	wsl.seen = map[string]*distro{d.Name: &d}
	wsl.Unlock()
	t.Cleanup(func() { wsl.Lock(); wsl.seen = seen; wsl.Unlock() })
	a := wslCodex(d)
	oldOn := wslOnGateway
	wslOnGateway = func() []*Agent { return []*Agent{wslCodex(d)} }
	t.Cleanup(func() { wslOnGateway = oldOn })
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `base_url = "`+nat+`/v1"`) {
		t.Fatalf("not on the WSL network first:\n%s", cfg)
	}

	driftSettled(t, a)
	if cfg := read(); !strings.Contains(cfg, `base_url = "`+lan+`/v1"`) || strings.Contains(cfg, "172.20.0.1") {
		t.Fatalf("not set again at the address that answers:\n%s", cfg)
	}
	if v, tr := wslrun.Via(); len(v) != 1 || v[0].String() != "10.0.0.132" || len(tr) != 0 {
		t.Errorf("via %v, still trying %v", v, tr)
	}
	mu.Lock()
	if len(tried) == 0 || len(tried[0]) != 2 {
		t.Errorf("listened at while tried: %v", tried)
	}
	mu.Unlock()
	reachForget()
	if dr := driftSettled(t, wslCodex(d)); dr != nil {
		t.Fatalf("set again, still %+v", dr)
	}

	// the firewall opened, the LAN's address shut: back on the WSL network
	mu.Lock()
	answers[nat], answers[lan] = "ok", "no"
	mu.Unlock()
	reachForget()
	driftSettled(t, wslCodex(d))
	if cfg := read(); !strings.Contains(cfg, `base_url = "`+nat+`/v1"`) || strings.Contains(cfg, "10.0.0.132") {
		t.Fatalf("not back on the WSL network:\n%s", cfg)
	}
	if v, _ := wslrun.Via(); len(v) != 0 {
		t.Errorf("via kept: %v", v)
	}
}
