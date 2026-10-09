package provider

import (
	"strings"
	"testing"
)

// A local server's preset (Ollama, LM Studio, oMLX, MLX-Serve) is
// reached at another port or another computer by its address alone: each API keeps its own
// path under it.
func TestAtAddress(t *testing.T) {
	ollama, lmstudio, omlx := *Preset("ollama"), *Preset("lmstudio"), *Preset("omlx")
	type urls struct{ chat, responses, anthropic string }
	for _, c := range []struct {
		name string
		pr   PresetDef
		addr string
		want urls
		bad  bool
	}{
		// what is refused, and what leaves the preset's own
		{name: "ftp", pr: ollama, addr: "ftp://192.168.1.5:11434", bad: true},
		{name: "no host", pr: ollama, addr: "http://", bad: true},
		{name: "only a port", pr: ollama, addr: ":11434", bad: true},
		{name: "spaces", pr: ollama, addr: "my ollama box", bad: true},
		{name: "space in host", pr: ollama, addr: "http://my host:11434", bad: true},
		{name: "port not a number", pr: ollama, addr: "localhost:abc", bad: true},
		{name: "port out of range", pr: ollama, addr: "localhost:70000", bad: true},
		{name: "empty keeps the preset's", pr: ollama, addr: "  ", want: urls{"http://localhost:11434/v1", "", "http://localhost:11434"}},

		{name: "no scheme", pr: ollama, addr: "localhost:11435", want: urls{"http://localhost:11435/v1", "", "http://localhost:11435"}},
		{name: "another computer", pr: ollama, addr: "192.168.1.5:11434", want: urls{"http://192.168.1.5:11434/v1", "", "http://192.168.1.5:11434"}},
		{name: "host alone", pr: ollama, addr: "myhost", want: urls{"http://myhost/v1", "", "http://myhost"}},
		{name: "pasted with /v1", pr: ollama, addr: "http://localhost:11434/v1", want: urls{"http://localhost:11434/v1", "", "http://localhost:11434"}},
		{name: "path, query and trailing slash dropped", pr: ollama, addr: " http://10.0.0.2:9000/api/?x=1#y ", want: urls{"http://10.0.0.2:9000/v1", "", "http://10.0.0.2:9000"}},
		{name: "https", pr: ollama, addr: "https://ollama.example.com", want: urls{"https://ollama.example.com/v1", "", "https://ollama.example.com"}},
		{name: "ipv6", pr: ollama, addr: "[::1]:11434", want: urls{"http://[::1]:11434/v1", "", "http://[::1]:11434"}},
		{name: "scheme in capitals", pr: ollama, addr: "HTTP://Box:1", want: urls{"http://Box:1/v1", "", "http://Box:1"}},
		{name: "lmstudio has chat only", pr: lmstudio, addr: "localhost:4321", want: urls{"http://localhost:4321/v1", "", ""}},
		{name: "omlx keeps all three paths", pr: omlx, addr: "studio.local:8001", want: urls{"http://studio.local:8001/v1", "http://studio.local:8001/v1", "http://studio.local:8001"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := AtAddress(c.pr, c.addr)
			if c.bad {
				if err == nil || !strings.Contains(err.Error(), "is not an address like http://localhost:11434") {
					t.Fatalf("%q: err %v, want refused", c.addr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := (urls{got.Chat, got.Responses, got.Anthropic}); g != c.want {
				t.Fatalf("%q: got %+v, want %+v", c.addr, g, c.want)
			}
		})
	}
	if Preset("ollama").Chat != "http://localhost:11434/v1" {
		t.Fatal("AtAddress changed the preset itself")
	}
}

// A provider saved from a local preset is moved the same way, and a
// refused address leaves it as it was.
func TestProviderAtAddress(t *testing.T) {
	p, _ := FromPreset("omlx")
	if err := p.AtAddress("192.168.1.9:8000"); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "http://192.168.1.9:8000/v1" || p.Responses != "http://192.168.1.9:8000/v1" || p.Anthropic != "http://192.168.1.9:8000" {
		t.Fatalf("moved: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if err := p.AtAddress("ftp://x"); err == nil || p.Chat != "http://192.168.1.9:8000/v1" {
		t.Fatalf("refused address: err %v, chat %q", err, p.Chat)
	}
}

// The address an editor starts with is the origin of the first URL set.
func TestAddressOf(t *testing.T) {
	for _, c := range []struct {
		urls []string
		want string
	}{
		{nil, ""},
		{[]string{"", "", ""}, ""},
		{[]string{"http://localhost:11434/v1", "", "http://localhost:11434"}, "http://localhost:11434"},
		{[]string{"", "https://box:8000/v1", ""}, "https://box:8000"},
		{[]string{"", "", "http://[::1]:11434"}, "http://[::1]:11434"},
		{[]string{"not a url at all", "http://x:1/v1"}, "http://x:1"},
	} {
		if got := AddressOf(c.urls...); got != c.want {
			t.Errorf("AddressOf(%q) = %q, want %q", c.urls, got, c.want)
		}
	}
}
