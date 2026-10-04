package netproxy

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func TestParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7891
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7892
  SOCKSProxy : 127.0.0.1
}`
	p := parseScutil(out)
	if p.URL != "http://127.0.0.1:7891" || len(p.Bypass) != 2 || p.Bypass[1] != "*.local" {
		t.Fatalf("%+v", p)
	}
	if p := parseScutil("<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 10.0.0.2\n}"); p.URL != "socks5://10.0.0.2:1080" {
		t.Fatalf("socks: %+v", p)
	}
	if p := parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPProxy : x\n}"); p.URL != "" {
		t.Fatalf("off: %+v", p)
	}
}

func TestParseWindows(t *testing.T) {
	for server, want := range map[string]string{
		"127.0.0.1:7890":                     "http://127.0.0.1:7890",
		"http=127.0.0.1:1;https=127.0.0.1:2": "http://127.0.0.1:2",
		"socks=127.0.0.1:1080":               "socks5://127.0.0.1:1080",
		"":                                   "",
	} {
		if got := parseWindows(server, "").URL; got != want {
			t.Errorf("%q: %q, want %q", server, got, want)
		}
	}
	p := parseWindows("127.0.0.1:7890", "*.corp;<local>")
	if !bypassed("git.corp", p.Bypass) || !bypassed("intranet", p.Bypass) || bypassed("chatgpt.com", p.Bypass) {
		t.Fatalf("bypass: %+v", p.Bypass)
	}
}

func TestFor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	sysCache.at, sysCache.p = farFuture, Proxy{URL: "http://127.0.0.1:7890", Bypass: []string{"*.corp"}}
	t.Cleanup(func() { sysCache.at = zero })

	u, _ := url.Parse("https://chatgpt.com/backend-api/codex/responses")
	if p, _ := For(u); p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("system: %v", p)
	}
	corp, _ := url.Parse("https://git.corp/x")
	if p, _ := For(corp); p != nil {
		t.Fatalf("bypass: %v", p)
	}
	local := &http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:3425"}}
	if p, _ := Func(local); p != nil {
		t.Fatalf("loopback: %v", p)
	}

	// magpie's own setting wins; "direct" turns it off
	settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"})
	if p, _ := For(u); p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("setting: %v", p)
	}
	settings.Save(settings.Settings{Proxy: "direct"})
	if p, _ := For(u); p != nil {
		t.Fatalf("direct: %v", p)
	}
	if err := settings.Save(settings.Settings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp proxy accepted")
	}
	if settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}) != nil || filepath.Dir(settings.Path()) != filepath.Join(dir, "magpie") {
		t.Fatal("host:port proxy refused")
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sysCache.at, sysCache.p = farFuture, Proxy{URL: "http://127.0.0.1:7890", Bypass: []string{"*.corp", "<local>"}}
	t.Cleanup(func() { sysCache.at = zero })
	get := func(env []string, k string) string {
		v := ""
		for _, e := range env {
			if key, val, _ := strings.Cut(e, "="); key == k {
				v = val
			}
		}
		return v
	}

	// the system's proxy, where the environment names none
	env := Env([]string{"PATH=/bin", "HOME=/h"})
	if get(env, "HTTPS_PROXY") != "http://127.0.0.1:7890" || get(env, "all_proxy") != "http://127.0.0.1:7890" ||
		get(env, "NO_PROXY") != "localhost,127.0.0.1,::1,.corp" || get(env, "PATH") != "/bin" {
		t.Fatalf("system: %v", env)
	}
	// one the environment names is kept
	env = Env([]string{"https_proxy=http://10.0.0.1:1", "NO_PROXY=x"})
	if len(env) != 2 || get(env, "https_proxy") != "http://10.0.0.1:1" {
		t.Fatalf("environment: %v", env)
	}
	// magpie's own replaces the environment's
	settings.Save(settings.Settings{Proxy: "127.0.0.1:6152"})
	env = Env([]string{"HTTPS_PROXY=http://10.0.0.1:1", "PATH=/bin"})
	if get(env, "HTTPS_PROXY") != "http://127.0.0.1:6152" || get(env, "http_proxy") != "http://127.0.0.1:6152" ||
		get(env, "NO_PROXY") != "localhost,127.0.0.1,::1" || len(env) != 9 {
		t.Fatalf("setting: %v", env)
	}
	// and "direct" leaves none
	settings.Save(settings.Settings{Proxy: "direct"})
	if env = Env([]string{"HTTPS_PROXY=http://10.0.0.1:1", "PATH=/bin"}); len(env) != 1 {
		t.Fatalf("direct: %v", env)
	}
}

var farFuture = time.Now().Add(time.Hour)
var zero time.Time

// A provider's own proxy (#237) wins over the global one, "direct" over
// both, and each proxy is given one transport of its own, kept.
func TestWith(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings.Save(settings.Settings{Proxy: "127.0.0.1:6152"})
	u := &url.URL{Scheme: "https", Host: "api.openai.com"}
	req := func(choice string) *http.Request {
		return (&http.Request{URL: u}).WithContext(With(context.Background(), choice))
	}
	if p, _ := Func(req("")); p == nil || p.String() != "http://127.0.0.1:6152" {
		t.Fatalf("follow: %v", p)
	}
	if p, _ := Func(req("socks5://127.0.0.1:1080")); p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("own: %v", p)
	}
	if p, _ := Func(req("direct")); p != nil {
		t.Fatalf("direct: %v", p)
	}
	local := (&http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:3425"}}).WithContext(With(context.Background(), "127.0.0.1:1"))
	if p, _ := Func(local); p != nil {
		t.Fatalf("loopback: %v", p)
	}

	d := Dispatch(&http.Transport{Proxy: Func}).(*dispatch)
	a, b := d.transport("127.0.0.1:1"), d.transport("direct")
	if a == b || d.transport("127.0.0.1:1") != a || len(d.own) != 2 {
		t.Fatalf("transports: %d kept", len(d.own))
	}
	if p, _ := a.Proxy(req("")); p == nil || p.Host != "127.0.0.1:1" {
		t.Fatalf("own transport: %v", p)
	}
	if p, _ := b.Proxy(req("")); p != nil {
		t.Fatalf("direct transport: %v", p)
	}

	get := func(env []string, k string) string {
		for _, e := range env {
			if key, val, _ := strings.Cut(e, "="); key == k {
				return val
			}
		}
		return ""
	}
	if env := EnvWith("socks5://127.0.0.1:1080", []string{"PATH=/bin"}); get(env, "HTTPS_PROXY") != "socks5://127.0.0.1:1080" {
		t.Fatalf("own env: %v", env)
	}
	if env := EnvWith("direct", []string{"HTTPS_PROXY=http://x:1", "PATH=/bin"}); len(env) != 1 {
		t.Fatalf("direct env: %v", env)
	}
	if env := EnvWith("", []string{"PATH=/bin"}); get(env, "HTTPS_PROXY") != "http://127.0.0.1:6152" {
		t.Fatalf("follow env: %v", env)
	}
}
