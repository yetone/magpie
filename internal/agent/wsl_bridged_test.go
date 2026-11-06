package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// ecalpal_ on Discord (magpie 0.1.2689, WSL 3.0.3.0, Ubuntu 22.04.5,
// .wslconfig vmSwitch=WSL_Bridge_Switch, networkingMode=Bridged,
// dhcp=false): in bridged networking the distro's default route is the
// LAN's router, 192.168.9.1, while Windows is at 192.168.9.147/24 on
// vEthernet (WSL_Bridge_Switch). magpie wrote the router into wsl.json and
// into Pi's models.json, where nothing listens. Windows' address on the
// bridge is the distro's way to it, and its key is the WSL key that
// address takes. NAT and mirrored keep their own values.
func TestWSLBridgedReachesWindowsOnTheBridge(t *testing.T) {
	for _, c := range []struct {
		net, cidr, route, want string
	}{
		{"bridged", "192.168.9.147/24", "192.168.9.1", "192.168.9.147"},
		{"nat", "172.20.0.1/20", "172.20.0.1", "172.20.0.1"},
		{"mirrored", "192.168.9.147/24", "192.168.9.1", ""},
	} {
		fakeWSLNet(t, c.cidr)
		d := parseProbe("Ubuntu-22.04", "home:/home/me\ndir:.pi\nroute:default via "+c.route+" dev eth0 proto static\nnet:"+c.net+"\n")
		if d == nil || d.Gateway != c.want {
			t.Fatalf("%s: %+v", c.net, d)
		}
		d.mirror("")
		want := gateway.URL()
		if c.want != "" {
			want = "http://" + c.want + ":" + gateway.Port()
		}
		if d.base() != want {
			t.Errorf("%s: %s, want %s", c.net, d.base(), want)
		}
	}
}

// One an older magpie set up on the router has its wsl.json and Pi's
// models.json written over once magpie starts again and probes the
// running distro: the address and the key with it.
func TestWSLBridgedOldRouterWrittenOver(t *testing.T) {
	root, home := piDistroHome(t, `{"defaultProvider": "xai", "defaultModel": "grok-4.6"}`)
	models := filepath.Join(home, ".pi", "agent", "models.json")
	// what 0.1.2689 wrote: Pi on the router, with the gateway's token
	old := distro{Name: "Ubuntu-22.04", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.pi": true},
		Gateway: "192.168.9.1", Net: "bridged", Running: true}
	if err := wslPi(old).Apply("model", "magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	pi := func() map[string]any {
		var f struct {
			Providers map[string]map[string]any `json:"providers"`
		}
		if err := json.Unmarshal([]byte(readFile(models)), &f); err != nil {
			t.Fatal(err)
		}
		return f.Providers["magpie"]
	}
	if p := pi(); p["baseUrl"] != "http://192.168.9.1:"+gateway.Port()+"/v1" {
		t.Fatalf("old config: %v", p)
	}

	fakeWSL(t, "Ubuntu-22.04\r\n", "Ubuntu-22.04\r\n", map[string]string{
		"Ubuntu-22.04": "home:/home/me\ndir:.pi\nroute:default via 192.168.9.1 dev eth0 proto static\nnet:bridged\n",
	}, map[string]string{"Ubuntu-22.04": root})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	if err := os.WriteFile(wslStatePath(), []byte(`{"Ubuntu-22.04":{"name":"Ubuntu-22.04","home":"/home/me","root":"","has":{"dir:.pi":true},"probe":`+
		jsonInt(wslProbeVersion)+`,"net":"bridged","gateway":"192.168.9.1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeWSLNet(t, "192.168.9.147/24")
	ds := wslDistros()
	if len(ds) != 1 || ds[0].Gateway != "192.168.9.147" || len(ds[0].Was) != 1 || ds[0].Was[0] != "192.168.9.1" {
		t.Fatalf("%+v", ds)
	}
	wslSave()
	var saved map[string]distro
	if err := json.Unmarshal([]byte(readFile(wslStatePath())), &saved); err != nil || saved["Ubuntu-22.04"].Gateway != "192.168.9.147" {
		t.Fatalf("wsl.json: %v\n%s", err, readFile(wslStatePath()))
	}
	a := wslPi(ds[0])
	if !a.Wired() {
		t.Fatalf("not wired: SyncCatalog would leave it: %v live %v\n%s", a.Values(), ds[0].live(), readFile(filepath.Join(home, ".pi", "agent", "settings.json")))
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	p := pi()
	if p["baseUrl"] != "http://192.168.9.147:"+gateway.Port()+"/v1" || p["apiKey"] != gateway.WSLKey() || gateway.WSLKey() == "" {
		t.Fatalf("after a sync: %v (WSL key %q)", p, gateway.WSLKey())
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }
