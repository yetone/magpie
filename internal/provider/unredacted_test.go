package provider

import (
	"context"
	"net"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// "Send requests unmasked" is the user's switch for each provider
// (AcMilan on Discord): set, a provider goes unmasked at whatever address,
// a name of the user's own DNS or a vendor's; unset, it is masked. A
// signed-in account and a plugin's provider take it too (isee1_1), and so
// does another magpie, which then masks by its own settings
// (CrazyMouse_TT on X). Nothing is
// looked up to decide: the lookup fails the test if it is.
func TestSkipsRedaction(t *testing.T) {
	t.Cleanup(netproxy.SetLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		t.Errorf("SkipsRedaction looked up %s", host)
		return nil, &net.DNSError{Err: "no lookups here", Name: host}
	}))
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"ollama", Provider{Unredacted: true, Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434"}, true},
		{"lan", Provider{Unredacted: true, Chat: "http://192.168.1.20:8000/v1"}, true},
		{"a lan name", Provider{Unredacted: true, Chat: "http://llm.corp.lan:8000/v1"}, true},
		{"a lan name, off", Provider{Chat: "http://llm.corp.lan:8000/v1"}, false},
		{"a vendor, set by the user", Provider{Unredacted: true, Chat: "https://api.deepseek.com/v1"}, true},
		{"a public ip, set by the user", Provider{Unredacted: true, Chat: "http://8.8.8.8/v1"}, true},
		{"through its own proxy", Provider{Unredacted: true, Chat: "http://llm.corp.lan:8000/v1", Proxy: "socks5://127.0.0.1:1080"}, true},
		{"not set", Provider{Chat: "http://localhost:11434/v1"}, false},
		{"no address", Provider{Unredacted: true}, false},
		{"an account", Provider{Unredacted: true, Account: &Account{Agent: "codex"}}, true},
		{"an account, off", Provider{Account: &Account{Agent: "codex"}}, false},
		{"a plugin's provider", Provider{Unredacted: true, Chat: "plugin://translate", Account: &Account{Agent: "translate"}}, true},
		{"another magpie, which masks by its own settings", Provider{Unredacted: true, Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, true},
		{"another magpie, left masked", Provider{Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, false},
	}
	for _, tc := range cases {
		if got := tc.p.SkipsRedaction(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// LocalHost, which decides only whether the editor cautions, takes
// localhost, .local, loopback, private and link-local IPs, and a name
// that resolves only to those; a public name or IP, one resolving to a
// public address among private ones, or one that doesn't resolve, isn't.
func TestLocalHost(t *testing.T) {
	fakeDNS(t, map[string][]string{
		"llm.corp.lan":   {"192.168.1.20"},
		"gpu.home.arpa":  {"10.0.0.7", "fd00::7"},
		"public.example": {"203.0.113.7"},
		"mixed.example":  {"10.0.0.2", "203.0.113.7"},
	})
	for host, want := range map[string]bool{
		"localhost": true, "gpu-box.local": true, "127.0.0.1": true, "::1": true, "192.168.1.20": true, "169.254.10.2": true,
		"llm.corp.lan": true, "gpu.home.arpa": true,
		"public.example": false, "mixed.example": false, "gone.lan": false, "8.8.8.8": false,
	} {
		if got := LocalHost(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

// fakeDNS has names resolve to m's addresses and no other name resolve,
// so no test looks a name up on the network.
func fakeDNS(t *testing.T, m map[string][]string) {
	t.Helper()
	t.Cleanup(netproxy.SetLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []net.IPAddr
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}))
}
