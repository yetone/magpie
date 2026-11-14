package netproxy

import (
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// shellSays has the login shell export vars, and nothing else.
func shellSays(t *testing.T, vars map[string]string) {
	t.Helper()
	old := shellVar
	shellVar = func(k string) string { return vars[k] }
	shellPorts.Lock()
	shellPorts.seen = nil
	shellPorts.Unlock()
	t.Cleanup(func() {
		shellVar = old
		shellPorts.Lock()
		shellPorts.seen = nil
		shellPorts.Unlock()
	})
}

// listener is a port something listens on, as a running proxy app's.
func listener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().String()
}

// freePort is a loopback port nothing listens on, as a closed proxy app's.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // a freed port may be handed to anything next: the test stubs the dial
	return addr
}

func proxyFor(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func envVal(env []string, k string) string {
	for _, e := range env {
		if kk, v, ok := strings.Cut(e, "="); ok && kk == k {
			return v
		}
	}
	return ""
}

// jimlee0824v2 on X: the proxy is exported in the shell profile, the
// proxy app's system proxy is off, and magpie opened from the Dock went
// direct until the proxy was set in it by hand. Auto takes the shell's,
// as a terminal's programs do: for magpie's requests, in Settings' line,
// and for the CLIs it starts.
func TestAutoTakesTheShellsProxy(t *testing.T) {
	clearProxyEnv(t)
	addr := listener(t)
	shellSays(t, map[string]string{
		"https_proxy": "http://" + addr,
		"http_proxy":  "http://" + addr,
		"no_proxy":    "corp.example",
	})
	want := "http://" + addr
	if got := proxyFor(t, "https://api.openai.com/v1/responses"); got != want {
		t.Fatalf("https request: proxy %q, want the shell's %q", got, want)
	}
	if got := proxyFor(t, "http://api.example/x"); got != want {
		t.Fatalf("http request: proxy %q, want the shell's %q", got, want)
	}
	if got := proxyFor(t, "https://llm.corp.example/v1"); got != "" {
		t.Fatalf("a host the shell's no_proxy names went through %q", got)
	}
	if got := proxyFor(t, "http://127.0.0.1:3425/v1"); got != "" {
		t.Fatalf("loopback went through %q", got)
	}
	if got, from := Describe(); got != want || from != "environment" {
		t.Fatalf("Describe = %q (%s), want %q (environment)", got, from, want)
	}
	env := EnvWith("", []string{"PATH=/bin"})
	if envVal(env, "HTTPS_PROXY") != want || envVal(env, "HTTP_PROXY") != want {
		t.Fatalf("a CLI magpie starts wasn't given the shell's proxy: %q", env)
	}
	if np := envVal(env, "NO_PROXY"); !strings.Contains(np, "corp.example") || !strings.Contains(np, "127.0.0.1") {
		t.Fatalf("NO_PROXY = %q, want the shell's corp.example beside loopback", np)
	}
}

// ALL_PROXY alone (curl's, which a SOCKS-only profile exports) counts too,
// and HTTPS_PROXY is for https:// requests only, as Go and curl read it.
func TestShellProxyByScheme(t *testing.T) {
	clearProxyEnv(t)
	https, all := listener(t), listener(t)
	shellSays(t, map[string]string{"HTTPS_PROXY": "http://" + https, "ALL_PROXY": "socks5://" + all})
	if got := proxyFor(t, "https://api.example/"); got != "http://"+https {
		t.Fatalf("https request: %q", got)
	}
	if got := proxyFor(t, "http://api.example/"); got != "socks5://"+all {
		t.Fatalf("http request: %q, want ALL_PROXY's", got)
	}
}

// A profile that exports the proxy app's port for good doesn't cut magpie
// off while the app is closed: nothing listening, it goes direct, and
// takes the proxy again once the app is up.
func TestClosedShellProxyGoesDirect(t *testing.T) {
	clearProxyEnv(t)
	addr := freePort(t)
	up := false
	old := dialShell
	dialShell = func(a string) bool { return a == addr && up }
	t.Cleanup(func() { dialShell = old })
	shellSays(t, map[string]string{"https_proxy": "http://" + addr})
	if got := proxyFor(t, "https://api.example/"); got != "" {
		t.Fatalf("a closed proxy app's port was taken: %q", got)
	}
	if got, from := Describe(); got != "" || from != "none" {
		t.Fatalf("Describe = %q (%s), want none", got, from)
	}
	if env := EnvWith("", []string{"PATH=/bin"}); envVal(env, "HTTPS_PROXY") != "" {
		t.Fatalf("a CLI was given a closed proxy: %q", env)
	}
	up = true
	shellPorts.Lock()
	for k, s := range shellPorts.seen {
		s.at = time.Now().Add(-shellDown)
		shellPorts.seen[k] = s
	}
	shellPorts.Unlock()
	if got := proxyFor(t, "https://api.example/"); got != "http://"+addr {
		t.Fatalf("the proxy app came up and wasn't taken: %q", got)
	}
}

// What the user set comes first: magpie's own setting ("direct" too), the
// environment magpie runs with, then the system's; the shell's only when
// none of them names a proxy.
func TestShellProxyComesLast(t *testing.T) {
	shell := ""
	setup := func(t *testing.T) {
		clearProxyEnv(t)
		shell = "http://" + listener(t)
		shellSays(t, map[string]string{"https_proxy": shell})
	}
	t.Run("settings", func(t *testing.T) {
		setup(t)
		if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
			t.Fatal(err)
		}
		if got := proxyFor(t, "https://api.example/"); got != "" {
			t.Fatalf("\"direct\" in Settings went through the shell's %q", got)
		}
		if env := EnvWith("", []string{"PATH=/bin"}); slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "HTTPS_PROXY=") }) {
			t.Fatalf("\"direct\" gave a CLI a proxy: %q", env)
		}
	})
	t.Run("environment", func(t *testing.T) {
		setup(t)
		// ALL_PROXY: Go reads HTTPS_PROXY once per process
		t.Setenv("ALL_PROXY", "http://10.0.0.1:3128")
		if got := proxyFor(t, "https://api.example/"); got != "http://10.0.0.1:3128" {
			t.Fatalf("magpie's own environment lost to the shell's: %q", got)
		}
	})
	t.Run("system", func(t *testing.T) {
		setup(t)
		sysCache.p = Proxy{URL: "http://127.0.0.1:7897", Bypass: []string{"*.corp"}}
		if got := proxyFor(t, "https://api.example/"); got != "http://127.0.0.1:7897" {
			t.Fatalf("the system's proxy lost to the shell's: %q", got)
		}
		if got := proxyFor(t, "https://a.corp/"); got != "" {
			t.Fatalf("a host the system's list keeps direct went through %q", got)
		}
	})
}
