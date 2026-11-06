package wslrun

import (
	"net"
	"testing"
)

func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

// ecalpal_ on Discord (WSL 3.0.3.0, .wslconfig vmSwitch=WSL_Bridge_Switch,
// networkingMode=Bridged, dhcp=false): the distro's default route is the
// LAN's router, 192.168.9.1, and Windows is at 192.168.9.147/24 on
// vEthernet (WSL_Bridge_Switch). Each mode is checked with its own values.
func TestHostAt(t *testing.T) {
	bridge := []*net.IPNet{ipNet(t, "192.168.9.147/24")}
	nat := []*net.IPNet{ipNet(t, "172.20.0.1/20")}
	both := append(append([]*net.IPNet{}, nat...), bridge...)
	for _, c := range []struct {
		mode, route string
		nets        []*net.IPNet
		want        string
	}{
		{"bridged", "192.168.9.1", bridge, "192.168.9.147"},
		{"bridged", "192.168.9.1", both, "192.168.9.147"},
		// a .wslconfig-only bridged WSL (no wslinfo) is told by the network
		{"", "192.168.9.1", bridge, "192.168.9.147"},
		// the router on no network Windows has on WSL's adapters: nothing
		// tells which is Windows, NAT's adapter being up beside a bridge
		{"bridged", "10.0.0.1", bridge, "10.0.0.1"},
		{"bridged", "10.0.0.1", nat, "10.0.0.1"},
		// nothing to tell by: kept
		{"bridged", "192.168.9.1", nil, "192.168.9.1"},
		// NAT: the next hop is Windows' own address there
		{"nat", "172.20.0.1", nat, "172.20.0.1"},
		{"nat", "172.20.0.1", both, "172.20.0.1"},
		{"nat", "172.20.0.1", nil, "172.20.0.1"},
		{"", "172.20.0.1", nat, "172.20.0.1"},
		// mirrored and consomme: Windows' 127.0.0.1
		{"mirrored", "192.168.9.1", bridge, ""},
		{"consomme", "198.18.0.2", nat, ""},
		{"bridged", "", bridge, ""},
	} {
		if got := HostAt(c.mode, c.route, c.nets); got != c.want {
			t.Errorf("%s via %s on %v: %q, want %q", c.mode, c.route, c.nets, got, c.want)
		}
	}
}

// A WSL command found in a bridged distro reaches Windows at its address on
// the bridge, so a proxy at Windows' 127.0.0.1 is handed on there.
func TestParseProbeBridged(t *testing.T) {
	old := Nets
	t.Cleanup(func() { Nets = old })
	Nets = func() []*net.IPNet { return []*net.IPNet{ipNet(t, "192.168.9.147/24")} }
	out := "bin:/home/me/.local/bin/claude\nroute:default via 192.168.9.1 dev eth0 proto static\nnet:bridged\n"
	tool, ok := parseProbe("Ubuntu-22.04", out)
	if !ok || tool.Host != "192.168.9.147" {
		t.Fatalf("%+v", tool)
	}
	if p := tool.proxy("http://127.0.0.1:7890"); p != "http://192.168.9.147:7890" {
		t.Errorf("proxy: %s", p)
	}
	tool, _ = parseProbe("Ubuntu", "bin:/usr/bin/claude\nroute:default via 172.20.0.1 dev eth0\nnet:nat\n")
	if tool.Host != "172.20.0.1" {
		t.Errorf("nat: %+v", tool)
	}
}
