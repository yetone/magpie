package netproxy

import (
	"net"
	"strconv"
	"strings"
)

// A Linux desktop keeps its proxy where its own apps read it, not in an
// app's environment: GNOME (and Cinnamon, MATE, Budgie, which take its
// settings) in gsettings, KDE in kioslaverc. Clash Verge and its kind set
// both when their system proxy is turned on, and an app opened from the
// desktop's menu has no *_PROXY, so magpie's Auto found none and went
// direct (#4147).

// parseGsettings reads `gsettings list-recursively org.gnome.system.proxy`:
// a proxy only with mode 'manual' (a PAC's 'auto' is not read). HTTPS wins
// over HTTP over SOCKS for URL, as on macOS.
func parseGsettings(out string) Proxy {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(f) == 3 && strings.HasPrefix(f[0], "org.gnome.system.proxy") {
			kv[strings.TrimPrefix(f[0], "org.gnome.system.proxy")+" "+f[1]] = strings.TrimSpace(f[2])
		}
	}
	if unquote(kv[" mode"]) != "manual" {
		return Proxy{}
	}
	entry := func(schema, scheme string) string {
		host := unquote(kv["."+schema+" host"])
		port, _ := strconv.Atoi(kv["."+schema+" port"])
		if host == "" || port <= 0 {
			return ""
		}
		return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
	}
	var bypass []string
	if l := strings.TrimSpace(kv[" ignore-hosts"]); strings.HasPrefix(l, "[") {
		for _, h := range strings.Split(strings.Trim(l, "[]"), ",") {
			if h = unquote(h); h != "" {
				bypass = append(bypass, h)
			}
		}
	}
	return pick(entry("https", "http"), entry("http", "http"), entry("socks", "socks5"), bypass)
}

// parseKioslaverc reads KDE's kioslaverc: a proxy only with ProxyType=1,
// set by hand (2 is a PAC, 3 finds one on the network, 4 is *_PROXY,
// which magpie reads anyway). KDE writes an address with its port after a
// space ("http://127.0.0.1 7897"), and a SOCKS one as socks://.
func parseKioslaverc(conf string) Proxy {
	kv := map[string]string{}
	in := false
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "[Proxy Settings]"
			continue
		}
		if k, v, ok := strings.Cut(line, "="); in && ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if kv["ProxyType"] != "1" {
		return Proxy{}
	}
	entry := func(key, scheme string) string {
		v := kv[key]
		if v == "" {
			return ""
		}
		if host, port, ok := strings.Cut(v, " "); ok {
			v = host + ":" + strings.TrimSpace(port)
		}
		if i := strings.Index(v, "://"); i >= 0 {
			v = v[i+3:]
		}
		if h, p, err := net.SplitHostPort(v); err != nil || h == "" || p == "" || p == "0" {
			return ""
		}
		return scheme + "://" + v
	}
	var bypass []string
	if kv["ReversedException"] != "true" { // "only these go through the proxy": not read
		for _, h := range strings.Split(kv["NoProxyFor"], ",") {
			if h = strings.TrimSpace(h); h != "" {
				bypass = append(bypass, h)
			}
		}
	}
	return pick(entry("httpsProxy", "http"), entry("httpProxy", "http"), entry("socksProxy", "socks5"), bypass)
}

// pick is the Proxy of a desktop's entries: the first of https, http and
// socks set is URL; HTTP and SOCKS are each entry alone.
func pick(https, http, socks string, bypass []string) Proxy {
	p := Proxy{HTTP: http, SOCKS: socks}
	for _, u := range []string{https, http, socks} {
		if u != "" {
			p.URL, p.Bypass = u, bypass
			return p
		}
	}
	return Proxy{}
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `'"`) }
