package netproxy

import "testing"

// #4147's reporter's own gsettings, Clash Verge's system proxy on under
// GNOME (Ubuntu 24.04), with the lines gsettings prints around them.
const gnomeClash = `org.gnome.system.proxy autoconfig-url ''
org.gnome.system.proxy ignore-hosts ['localhost', '127.0.0.0/8', '::1', '*.corp']
org.gnome.system.proxy mode 'manual'
org.gnome.system.proxy use-same-proxy true
org.gnome.system.proxy.ftp host ''
org.gnome.system.proxy.ftp port 0
org.gnome.system.proxy.http authentication-password ''
org.gnome.system.proxy.http authentication-user ''
org.gnome.system.proxy.http enabled false
org.gnome.system.proxy.http host '127.0.0.1'
org.gnome.system.proxy.http port 7897
org.gnome.system.proxy.http use-authentication false
org.gnome.system.proxy.https host '127.0.0.1'
org.gnome.system.proxy.https port 7897
org.gnome.system.proxy.socks host '127.0.0.1'
org.gnome.system.proxy.socks port 7897
`

func TestParseGsettings(t *testing.T) {
	p := parseGsettings(gnomeClash)
	if p.URL != "http://127.0.0.1:7897" || p.HTTP != "http://127.0.0.1:7897" || p.SOCKS != "socks5://127.0.0.1:7897" {
		t.Fatalf("%+v", p)
	}
	if !bypassed("git.corp", p.Bypass) || bypassed("api.openai.com", p.Bypass) {
		t.Fatalf("bypass: %q", p.Bypass)
	}
	// the system proxy turned off, or a PAC: none
	for _, mode := range []string{"'none'", "'auto'"} {
		if p := parseGsettings(replaceMode(gnomeClash, mode)); p.URL != "" {
			t.Fatalf("mode %s: %+v", mode, p)
		}
	}
	// SOCKS alone
	if p := parseGsettings("org.gnome.system.proxy mode 'manual'\norg.gnome.system.proxy.socks host '10.0.0.2'\norg.gnome.system.proxy.socks port 1080\norg.gnome.system.proxy.http port 0\n"); p.URL != "socks5://10.0.0.2:1080" || p.HTTP != "" {
		t.Fatalf("socks: %+v", p)
	}
	if p := parseGsettings(""); p.URL != "" {
		t.Fatalf("empty: %+v", p)
	}
}

func replaceMode(s, mode string) string {
	out := ""
	for _, l := range splitLines(s) {
		if l == "org.gnome.system.proxy mode 'manual'" {
			l = "org.gnome.system.proxy mode " + mode
		}
		out += l + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// What Clash Verge (sysproxy-rs) writes under KDE: the port after a
// space, SOCKS as socks://.
const kdeClash = `[$Version]
update_info=kioslave.upd:kioslave-5.0

[Proxy Settings]
NoProxyFor=localhost,127.0.0.0/8,::1,.corp
ProxyType=1
httpProxy=http://127.0.0.1 7897
httpsProxy=http://127.0.0.1 7897
socksProxy=socks://127.0.0.1 7897
`

func TestParseKioslaverc(t *testing.T) {
	p := parseKioslaverc(kdeClash)
	if p.URL != "http://127.0.0.1:7897" || p.HTTP != "http://127.0.0.1:7897" || p.SOCKS != "socks5://127.0.0.1:7897" {
		t.Fatalf("%+v", p)
	}
	if !bypassed("git.corp", p.Bypass) {
		t.Fatalf("bypass: %q", p.Bypass)
	}
	// written as host:port, as newer KDE does
	if p := parseKioslaverc("[Proxy Settings]\nProxyType=1\nhttpsProxy=http://10.0.0.2:8080\n"); p.URL != "http://10.0.0.2:8080" {
		t.Fatalf("host:port: %+v", p)
	}
	for _, typ := range []string{"0", "2", "3", "4"} {
		if p := parseKioslaverc(replaceType(kdeClash, typ)); p.URL != "" {
			t.Fatalf("ProxyType=%s: %+v", typ, p)
		}
	}
	// the same keys in another group are not the proxy's
	if p := parseKioslaverc("[Other]\nProxyType=1\nhttpsProxy=http://127.0.0.1 1\n"); p.URL != "" {
		t.Fatalf("other group: %+v", p)
	}
}

func replaceType(s, typ string) string {
	out := ""
	for _, l := range splitLines(s) {
		if l == "ProxyType=1" {
			l = "ProxyType=" + typ
		}
		out += l + "\n"
	}
	return out
}
