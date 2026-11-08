package netguard

import (
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"192.0.2.1": false, "198.51.100.1": false, "203.0.113.1": false, "240.0.0.1": false,
		"198.18.0.0": false, "198.19.255.255": false, "::ffff:198.18.0.7": false, // fake-IP: FakeIP's, not public
		"::1": false, "fc00::1": false, "fe80::1": false, "2001::1": false, "2001:db8::1": false,
		"64:ff9b::7f00:1": false, "2002:c612:7::": false,
		"8.8.8.8": true, "93.184.216.34": true, "198.17.255.255": true, "198.20.0.0": true,
		"2606:4700::1111": true, "64:ff9b::808:808": true,
	} {
		if got := Public(netip.MustParseAddr(s)); got != want {
			t.Errorf("Public(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestFakeIP(t *testing.T) {
	for s, want := range map[string]bool{
		"198.18.0.0": true, "198.18.0.7": true, "198.19.255.255": true, "::ffff:198.18.0.7": true,
		"198.17.255.255": false, "198.20.0.0": false, "127.0.0.1": false, "10.0.0.1": false,
		"fd27:712::c612:7": false, "2002:c612:7::": false,
	} {
		if got := FakeIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("FakeIP(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestLoonPaired(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	for _, c := range []struct {
		name    string
		a       string
		answers []netip.Addr
		want    bool
	}{
		{"paired with a fake A", "fd27:712::c612:139", addrs("198.18.1.57", "fd27:712::c612:139"), true},
		{"paired with a real A", "fd27:712::c600:1061", addrs("fd27:712::c600:1061", "198.0.16.97"), true},
		{"paired, the A IPv4-mapped", "fd27:712::c600:1061", addrs("::ffff:198.0.16.97"), true},
		{"unpaired", "fd27:712::c600:1061", addrs("fd27:712::c600:1061"), false},
		{"another A", "fd27:712::c600:1061", addrs("8.8.8.8"), false},
		{"outside Loon's /96", "fd27:712:1::c600:1061", addrs("198.0.16.97"), false},
		{"an IPv4 address", "198.0.16.97", addrs("198.0.16.97"), false},
	} {
		if got := LoonPaired(netip.MustParseAddr(c.a), c.answers); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFakePaired(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	for _, c := range []struct {
		name    string
		a       string
		answers []netip.Addr
		want    bool
	}{
		{"Clash Verge Rev's default range6", "fdfe:dcba:9876::7", addrs("198.18.0.7", "fdfe:dcba:9876::7"), true},
		{"its filled-in 2001:2::/64", "2001:2::7", addrs("2001:2::7", "198.18.0.7"), true},
		{"a sing-box fc00::/18", "fc00::7", addrs("198.19.0.7"), true},
		{"the A IPv4-mapped", "fdfe:dcba:9876::7", addrs("::ffff:198.18.0.7"), true},
		{"beside a real A", "fdfe:dcba:9876::7", addrs("93.184.216.34"), false},
		{"beside a private A", "fdfe:dcba:9876::7", addrs("10.0.0.1"), false},
		{"alone", "fdfe:dcba:9876::7", addrs("fdfe:dcba:9876::7"), false},
		{"loopback", "::1", addrs("198.18.0.7"), false},
		{"link-local", "fe80::1", addrs("198.18.0.7"), false},
		{"outside 2001:2::/48", "2001:3::7", addrs("198.18.0.7"), false},
		{"an IPv4 address", "10.0.0.1", addrs("198.18.0.7"), false},
		{"the fake A itself", "198.18.0.7", addrs("198.18.0.7"), false},
	} {
		if got := FakePaired(netip.MustParseAddr(c.a), c.answers); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
