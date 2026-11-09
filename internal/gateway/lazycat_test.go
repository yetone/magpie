package gateway

import (
	"net"
	"slices"
	"testing"
)

// lazyCatNet holds what a LazyCat box's network looks like to magpie for a
// test: the interfaces' names, their addresses, the route table and
// host.lzcapp's answer; nothing is looked up.
func lazyCatNet(t *testing.T, names []string, addrs map[string][]string, table string, lzc ...string) {
	t.Helper()
	t.Cleanup(func(ifn func() []string, ips func(string) []net.IP, rt func() string, lz func() []net.IP, hi func() []net.IP, in func() bool) func() {
		return func() {
			ifaceNames, ifaceIPs, routeTable, lzcHostIPs, hostIPs, inThisContainer = ifn, ips, rt, lz, hi, in
		}
	}(ifaceNames, ifaceIPs, routeTable, lzcHostIPs, hostIPs, inThisContainer))
	parse := func(ss []string) []net.IP {
		var out []net.IP
		for _, s := range ss {
			out = append(out, net.ParseIP(s))
		}
		return out
	}
	ifaceNames = func() []string { return names }
	ifaceIPs = func(name string) []net.IP { return parse(addrs[name]) }
	routeTable = func() string { return table }
	lzcHostIPs = func() []net.IP { return parse(lzc) }
	hostIPs = func() []net.IP {
		var out []net.IP
		for _, n := range names {
			out = append(out, parse(addrs[n])...)
		}
		return out
	}
	inThisContainer = func() bool { return true }
}

const (
	// the box: its LAN link enp2s0 holds the default route, beside the
	// apps' bridges
	boxRoutes = "Iface\tDestination\tGateway\tFlags\nenp2s0\t00000000\t0100A8C0\t0003\nenp2s0\t0000A8C0\t00000000\t0001\nlzc-br0\t00001CAC\t00000000\t0001\n"
	// an app's container on the box's bridge: eth0 to 172.28.3.65
	bridgeRoutes = "Iface\tDestination\tGateway\tFlags\neth0\t00000000\t41031CAC\t0003\neth0\t00001CAC\t00000000\t0001\n"
)

// mtoolstec on X: magpie in a LazyCat app offered the app's domain, which
// only devices with LazyCat's client reach, and the box's bridge
// (172.28.3.65), which nothing off the box reaches; other devices on the
// home network wanted http://192.168.0.100:3425/v1. On the box's network
// (network_mode: host) the box's LAN address is offered first, then the
// domain; a MAGPIE_PUBLIC_URL of the user's own stays first. On a bridge
// network the box's address can't be known: the domain alone, and no
// bridge address for other containers.
func TestLazyCatOffersTheBoxAddress(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("LAZYCAT_APP_DOMAIN", "magpie.snyht3.heiyu.space")
	const lc = "https://magpie.snyht3.heiyu.space"
	box := Scheme() + "://192.168.0.100:3425"

	// host network: the box's interfaces, docker-style bridges among them
	lazyCatNet(t, []string{"lo", "enp2s0", "lzc-br0", "veth1a2b"}, map[string][]string{
		"lo": {"127.0.0.1"}, "enp2s0": {"192.168.0.100", "fe80::1"}, "lzc-br0": {"172.28.0.1"},
	}, boxRoutes, "172.28.0.1")
	if got, want := LANURLs(), []string{box, lc}; !slices.Equal(got, want) {
		t.Errorf("host network: %v, want %v", got, want)
	}
	if !LazyCatBox() {
		t.Error("host network: not the box's")
	}
	if got := ContainerHostURL(); got != "" {
		t.Errorf("host network: other containers offered %q", got)
	}
	// the box's network found by host.lzcapp alone, with no bridge named
	// as Docker's
	lazyCatNet(t, []string{"lo", "enp2s0", "lzc0"}, map[string][]string{
		"enp2s0": {"192.168.0.100"}, "lzc0": {"198.18.0.1"},
	}, boxRoutes, "198.18.0.1")
	if got, want := LANURLs(), []string{box, lc}; !slices.Equal(got, want) {
		t.Errorf("host.lzcapp on this network: %v, want %v", got, want)
	}
	// host.lzcapp on the default route's interface is not the LAN's
	lazyCatNet(t, []string{"lo", "enp2s0", "veth0"}, map[string][]string{
		"enp2s0": {"192.168.0.100", "172.28.0.1"},
	}, boxRoutes, "172.28.0.1")
	if got, want := LANURLs(), []string{box, lc}; !slices.Equal(got, want) {
		t.Errorf("host.lzcapp on the LAN link: %v, want %v", got, want)
	}
	// the user's own address first
	t.Setenv("MAGPIE_PUBLIC_URL", "https://ai.example.com")
	if got, want := LANURLs(), []string{"https://ai.example.com", box, lc}; !slices.Equal(got, want) {
		t.Errorf("MAGPIE_PUBLIC_URL: %v, want %v", got, want)
	}
	// the package's MAGPIE_PUBLIC_URL, the domain, is said once
	t.Setenv("MAGPIE_PUBLIC_URL", lc+"/")
	if got, want := LANURLs(), []string{box, lc}; !slices.Equal(got, want) {
		t.Errorf("MAGPIE_PUBLIC_URL the domain: %v, want %v", got, want)
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	// MAGPIE_ADDR pinned to one address: that one alone is served
	t.Setenv("MAGPIE_ADDR", "192.168.0.100:3425")
	if got, want := LANURLs(), []string{lc}; !slices.Equal(got, want) {
		t.Errorf("pinned: %v, want %v", got, want)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")

	// bridge network: the container's eth0 alone, host.lzcapp the bridge's
	lazyCatNet(t, []string{"lo", "eth0"}, map[string][]string{
		"eth0": {"172.28.3.66"},
	}, bridgeRoutes, "172.28.3.65")
	if got, want := LANURLs(), []string{lc}; !slices.Equal(got, want) {
		t.Errorf("bridge network: %v, want %v", got, want)
	}
	if LazyCatBox() {
		t.Error("bridge network: said to be the box's")
	}
	if got := ContainerHostURL(); got != "" {
		t.Errorf("bridge network: other containers offered %q", got)
	}
	if LazyCatURL() != lc {
		t.Errorf("LazyCatURL: %q", LazyCatURL())
	}

	// outside LazyCat nothing changes: a NAS's host network offers its own
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	lazyCatNet(t, []string{"lo", "enp2s0", "docker0"}, map[string][]string{
		"enp2s0": {"192.168.0.100"},
	}, boxRoutes)
	if LazyCatBox() || LazyCatURL() != "" {
		t.Error("outside LazyCat: said to be one")
	}
}

func TestDefaultIfaces(t *testing.T) {
	for _, c := range []struct {
		table string
		want  []string
	}{
		{boxRoutes, []string{"enp2s0"}},
		{bridgeRoutes, []string{"eth0"}},
		{"Iface\tDestination\tGateway\tFlags\nwlan0\t00000000\t0101A8C0\t0003\neth0\t00000000\t0100A8C0\t0003\nwlan0\t00000000\t0101A8C0\t0003\n", []string{"wlan0", "eth0"}},
		{"Iface\tDestination\tGateway\tFlags\neth0\t0000A8C0\t00000000\t0001\n", nil},
		{"", nil},
	} {
		if got := defaultIfaces(c.table); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.table, got, c.want)
		}
	}
}

// A lookup's answer is kept; a failure is asked again only after retry;
// outside LazyCat nothing is asked.
func TestCachedLookupOutsideLazyCat(t *testing.T) {
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	c := &cachedLookup{name: "host.lzcapp.invalid", retry: 0}
	if got := c.get(); got != nil || !c.at.IsZero() {
		t.Errorf("outside LazyCat: %v, asked at %v", got, c.at)
	}
}
