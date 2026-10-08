package provider

import "testing"

// Only a provider the user set to go unmasked whose every address is on
// this machine or the local network skips redaction; one at a vendor's
// address, a signed-in account or another magpie never does.
func TestSkipsRedaction(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"ollama", Provider{Unredacted: true, Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434"}, true},
		{"loopback ip", Provider{Unredacted: true, Chat: "http://127.0.0.1:1234/v1"}, true},
		{"ipv6 loopback", Provider{Unredacted: true, Chat: "http://[::1]:8000/v1"}, true},
		{"lan", Provider{Unredacted: true, Chat: "http://192.168.1.20:8000/v1"}, true},
		{"mdns", Provider{Unredacted: true, Responses: "http://gpu-box.local:8000/v1"}, true},
		{"not set", Provider{Chat: "http://localhost:11434/v1"}, false},
		{"a vendor", Provider{Unredacted: true, Chat: "https://api.deepseek.com/v1"}, false},
		{"one address a vendor's", Provider{Unredacted: true, Chat: "http://localhost:11434/v1", Anthropic: "https://ollama.com"}, false},
		{"a public ip", Provider{Unredacted: true, Chat: "http://8.8.8.8/v1"}, false},
		{"no address", Provider{Unredacted: true}, false},
		{"an account", Provider{Unredacted: true, Chat: "http://localhost:1/v1", Account: &Account{Agent: "codex"}}, false},
		{"another magpie", Provider{Unredacted: true, Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, false},
	}
	for _, tc := range cases {
		if got := tc.p.SkipsRedaction(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
