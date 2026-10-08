package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/gateway"
)

// A .wslconfig Notepad saved as "Unicode" (UTF-16LE with a BOM), as
// "Unicode big endian", or UTF-16LE with no BOM, is read as WSL reads it
// (lgtm on Discord: mirrored all along, and told it wasn't).
func TestWSLConfigUTF16(t *testing.T) {
	cfg := "[wsl2] # networking\r\nmemory=8GB\r\nnetworkingMode=mirrored\r\n"
	be := []byte{0xfe, 0xff}
	for _, u := range utf16.Encode([]rune(cfg)) {
		be = append(be, byte(u>>8), byte(u))
	}
	for name, b := range map[string][]byte{
		"utf-8":            []byte(cfg),
		"utf-8 bom":        append([]byte{0xef, 0xbb, 0xbf}, cfg...),
		"utf-16le bom":     utf16le(cfg, true),
		"utf-16le, no bom": utf16le(cfg, false),
		"utf-16be bom":     be,
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		os.WriteFile(filepath.Join(home, ".wslconfig"), b, 0o644)
		if !wslMirrored(wslConfig()) {
			t.Errorf("%s: not mirrored: %q", name, wslConfig())
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if wslConfig() != "" || wslMirrored(wslConfig()) {
		t.Error("no .wslconfig")
	}
}

// The probe takes WSL's networking mode from wslinfo; an empty answer or
// an old wslinfo's usage text says nothing.
func TestParseProbeNet(t *testing.T) {
	for out, want := range map[string]string{
		"home:/home/me\nnet:mirrored\n":                    "mirrored",
		"home:/home/me\nnet:NAT\r\n":                       "nat",
		"home:/home/me\nnet:\n":                            "",
		"home:/home/me\nnet:Usage: wslinfo [Options...]\n": "",
		"home:/home/me\n":                                  "",
	} {
		if d := parseProbe("U", out); d == nil || d.Net != want {
			t.Errorf("%q: %+v", out, d)
		}
	}
	if !strings.Contains(wslProbeScript, "wslinfo --networking-mode") {
		t.Error("probe doesn't ask wslinfo")
	}
}

// What WSL says wins over .wslconfig: mirrored by wslinfo with no
// .wslconfig at all is 127.0.0.1; nat by wslinfo with a .wslconfig saying
// mirrored (not yet applied) is Windows, and the notice says nat. Without
// wslinfo, .wslconfig decides, and the notice says magpie couldn't ask.
func TestWSLNetworkingFromWslinfo(t *testing.T) {
	for _, c := range []struct {
		net, cfg string
		mirrored bool
		notice   string
	}{
		{"mirrored", "", true, ""},
		// consomme relays the distro's 127.0.0.1 to Windows' (#1230), as
		// virtioproxy, its name before WSL 2.9, did
		{"consomme", "", true, ""},
		{"virtioproxy", "", true, ""},
		{"bridged", "", false, "is in bridged networking"},
		{"nat", "[wsl2]\nnetworkingMode=mirrored\n", false, "is in nat networking"},
		// a WSL without wslinfo predates consomme: .wslconfig's isn't taken
		{"", "[wsl2]\nnetworkingMode=Consomme\n", false, "couldn't ask WSL Ubuntu"},
		{"", "[wsl2]\nnetworkingMode=mirrored\n", true, ""},
		{"", "", false, "couldn't ask WSL Ubuntu"},
	} {
		home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
		if c.cfg != "" {
			os.WriteFile(filepath.Join(home, ".wslconfig"), utf16le(c.cfg, true), 0o644)
		}
		probe := "home:/" + filepath.Base(home) + "\ndir:.codex\nroute:default via 192.168.16.1 dev eth0\n"
		if c.net != "" {
			probe += "net:" + c.net + "\n"
		}
		fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{"Ubuntu": probe},
			map[string]string{"Ubuntu": filepath.Dir(home)})
		ds := wslDistros()
		if len(ds) != 1 || ds[0].Mirrored != c.mirrored || ds[0].Net != c.net {
			t.Fatalf("%+v: %+v", c, ds)
		}
		a := wslCodex(ds[0])
		n := a.Notice()
		if c.mirrored {
			if ds[0].base() != gateway.URL() || strings.Contains(n, "mirrored") {
				t.Errorf("%+v: %s / %s", c, ds[0].base(), n)
			}
			continue
		}
		if !strings.Contains(n, c.notice) || !strings.Contains(n, "192.168.16.1") || strings.Contains(n, "isn't in mirrored") {
			t.Errorf("%+v: %s", c, n)
		}
	}
}

// A distro that stopped is probed again once it runs, so WSL restarted in
// another networking mode is seen without restarting magpie.
func TestWSLReprobedAfterStop(t *testing.T) {
	home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
	fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", nil, map[string]string{"Ubuntu": filepath.Dir(home)})
	running, net, probes := true, "nat", 0
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l -q":
			return utf16le("Ubuntu\r\n", true), nil
		case "-l --running -q":
			if running {
				return utf16le("Ubuntu\r\n", true), nil
			}
			return nil, nil
		}
		if len(args) > 2 && args[0] == "-d" {
			probes++
			return []byte("home:/" + filepath.Base(home) + "\ndir:.codex\nnet:" + net + "\n"), nil
		}
		return nil, errors.New("unexpected wsl.exe " + strings.Join(args, " "))
	}
	look := func() distro {
		wsl.Lock()
		wsl.at, wsl.runAt = time.Time{}, time.Time{}
		wsl.Unlock()
		ds := wslDistros()
		if len(ds) != 1 {
			t.Fatalf("%+v", ds)
		}
		return ds[0]
	}
	if d := look(); d.Mirrored || probes != 1 {
		t.Fatalf("%+v, %d probes", d, probes)
	}
	if d := look(); probes != 1 {
		t.Fatalf("probed again while running: %+v", d)
	}
	running = false
	if d := look(); d.Running || probes != 1 {
		t.Fatalf("stopped: %+v, %d probes", d, probes)
	}
	running, net = true, "mirrored"
	if d := look(); !d.Mirrored || probes != 2 {
		t.Fatalf("restarted mirrored: %+v, %d probes", d, probes)
	}
}

// #1230: under consomme networking the distro's default route is Windows'
// own next hop (198.18.0.2 behind a TUN proxy), not Windows, and the
// distro's 127.0.0.1 is relayed to Windows' own. Every agent magpie writes
// there is pointed at 127.0.0.1, and Codex's config says so.
func TestWSLConsommeIsLoopback(t *testing.T) {
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	probe := "home:/" + filepath.Base(home) + "\ndir:.codex\nroute:default via 198.18.0.2 dev eth0 proto kernel\nns:nameserver 10.255.255.254\nnet:consomme\n"
	fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{"Ubuntu": probe},
		map[string]string{"Ubuntu": filepath.Dir(home)})
	ds := wslDistros()
	if len(ds) != 1 || !ds[0].Mirrored || ds[0].Net != "consomme" || ds[0].Gateway != "" || ds[0].base() != gateway.URL() {
		t.Fatalf("%+v", ds)
	}
	for _, k := range wslKinds {
		a := wslAgent(k, ds[0])
		if got := a.Gateway(); got != gateway.URL() {
			t.Errorf("%s: pointed at %s, not %s", k.id, got, gateway.URL())
		}
		if n := a.Notice(); strings.Contains(n, "198.18.0.2") || strings.Contains(n, "not mirrored") {
			t.Errorf("%s: %s", k.id, n)
		}
	}
	a := wslCodex(ds[0])
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `base_url = "`+gateway.URL()+`/v1"`) || strings.Contains(cfg, "198.18.0.2") {
		t.Fatalf("config:\n%s", cfg)
	}
}

// An agent an older magpie pointed at a consomme distro's default route
// (#1230's 198.18.0.2) is offered 127.0.0.1 once the distro is probed again.
func TestWSLConsommeMovesOffRoute(t *testing.T) {
	home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
	probe := "home:/" + filepath.Base(home) + "\ndir:.codex\nroute:default via 198.18.0.2 dev eth0\nnet:consomme\n"
	fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{"Ubuntu": probe},
		map[string]string{"Ubuntu": filepath.Dir(home)})
	// wsl.json as a magpie that took the route for Windows left it
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	if err := os.WriteFile(wslStatePath(), []byte(`{"Ubuntu":{"name":"Ubuntu","home":"/`+filepath.Base(home)+
		`","root":"","has":{"dir:.codex":true},"probe":2,"gateway":"198.18.0.2","net":"consomme"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ds := wslDistros(); len(ds) != 1 || ds[0].Gateway != "" {
		t.Fatalf("%+v", ds)
	}
	old := "http://198.18.0.2:" + gateway.Port()
	if to, name := wslMovedFrom(old + "/v1"); to != gateway.URL() || name != "Ubuntu" {
		t.Fatalf("moved from %s: %q %q", old, to, name)
	}
	if to, _ := wslMovedFrom("http://10.1.2.3:" + gateway.Port()); to != "" {
		t.Fatalf("a host the distro never had moved to %q", to)
	}
}

// consomme relays 127.0.0.1 only while localhostForwarding is on: one
// turned off in .wslconfig is told, and mirrored doesn't heed it.
func TestWSLConsommeLocalhostForwardingOff(t *testing.T) {
	for _, c := range []struct {
		net, cfg string
		told     bool
	}{
		{"consomme", "[wsl2]\nlocalhostForwarding=false\n", true},
		{"consomme", "[wsl2]\nlocalhostforwarding = 0 # off\n", true},
		{"consomme", "[wsl2]\nlocalhostForwarding=true\n", false},
		{"consomme", "[experimental]\nlocalhostForwarding=false\n", false},
		{"consomme", "", false},
		{"mirrored", "[wsl2]\nlocalhostForwarding=false\n", false},
	} {
		home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
		os.WriteFile(filepath.Join(home, ".wslconfig"), []byte(c.cfg), 0o644)
		probe := "home:/" + filepath.Base(home) + "\ndir:.codex\nroute:default via 198.18.0.2 dev eth0\nnet:" + c.net + "\n"
		fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{"Ubuntu": probe},
			map[string]string{"Ubuntu": filepath.Dir(home)})
		ds := wslDistros()
		if len(ds) != 1 || ds[0].base() != gateway.URL() {
			t.Fatalf("%+v: %+v", c, ds)
		}
		if n := wslCodex(ds[0]).Notice(); strings.Contains(n, "localhostForwarding=false") != c.told {
			t.Errorf("%+v: %q", c, n)
		}
	}
}
