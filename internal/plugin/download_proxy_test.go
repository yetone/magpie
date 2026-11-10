package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/source"
)

// JonahFu on X: GitHub is reached only through a proxy, the vendors
// without one. Plugin downloads (the list, npm's registry, GitHub, the
// dist branch, Bun, bun add and git) go through the proxy Settings sets for
// them, while the host — the plugins' requests to their vendors — keeps
// magpie's own. Left unset, downloads follow the global proxy as before.
func TestPluginDownloadsTakeTheirOwnProxy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}

	// a proxy: it answers every request sent through it, and keeps the
	// address each asked for
	var mu sync.Mutex
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.String())
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(proxy.Close)
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}

	// magpie's own transport, as the app installs it, put back after
	tr := http.DefaultTransport.(*http.Transport)
	wasProxy, wasClient := tr.Proxy, http.DefaultClient.Transport
	t.Cleanup(func() { tr.Proxy, http.DefaultClient.Transport = wasProxy, wasClient })
	netproxy.Install()

	// API requests direct, plugin downloads through the proxy
	if err := settings.Save(settings.Settings{Proxy: "direct", PluginProxy: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const list = "http://plugins.example.invalid/registry.json"
	if b, err := fetchJSONFrom(ctx, list, 1<<10, source.DoOfficial); err != nil || !strings.Contains(string(b), "ok") {
		t.Fatalf("the plugins list through plugin downloads' proxy: %q %v", b, err)
	}
	const reg = "http://npm.example.invalid/@me%2fx"
	if b, err := fetchRegistry(ctx, reg, "tok", 1<<10); err != nil || !strings.Contains(string(b), "ok") {
		t.Fatalf("a registry of the user's through plugin downloads' proxy: %q %v", b, err)
	}
	const bunZip = "http://github.example.invalid/bun.zip"
	if b, err := getURLOfficial(ctx, bunZip, 1<<10); err != nil || !strings.Contains(string(b), "ok") {
		t.Fatalf("Bun's download through plugin downloads' proxy: %q %v", b, err)
	}
	if got := seen(); !slices.Contains(got, list) || !slices.Contains(got, reg) || !slices.Contains(got, bunZip) {
		t.Fatalf("the proxy was asked %v", got)
	}

	// bun add and git get the proxy; the host (bun run) doesn't
	proxyOf := func(env []string) string {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "HTTPS_PROXY="); ok {
				return v
			}
		}
		return ""
	}
	if p := proxyOf(bunCommand(ctx, "bun", dir, "add", "--ignore-scripts", "x").Env); p != proxy.URL {
		t.Fatalf("bun add's HTTPS_PROXY %q, want %q", p, proxy.URL)
	}
	if p := proxyOf(dlEnv()); p != proxy.URL {
		t.Fatalf("git's HTTPS_PROXY %q, want %q", p, proxy.URL)
	}
	if p := proxyOf(bunCommand(ctx, "bun", dir, "run", "host.js").Env); p != "" {
		t.Fatalf("the host, whose requests are API requests, got plugin downloads' proxy %q", p)
	}

	// plugin downloads direct, API requests through the proxy: the host
	// keeps the proxy, and a download goes around it
	if err := settings.Save(settings.Settings{Proxy: proxy.URL, PluginProxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if p := proxyOf(bunCommand(ctx, "bun", dir, "run", "host.js").Env); p != proxy.URL {
		t.Fatalf("the host's HTTPS_PROXY %q, want magpie's %q", p, proxy.URL)
	}
	if p := proxyOf(bunCommand(ctx, "bun", dir, "add", "x").Env); p != "" {
		t.Fatalf("bun add went through %q with plugin downloads set direct", p)
	}
	n := len(seen())
	if _, err := fetchJSONFrom(ctx, "http://other.example.invalid/registry.json", 1<<10, source.DoOfficial); err == nil {
		t.Fatal("a download set direct was answered")
	}
	if len(seen()) != n {
		t.Fatalf("a download set direct went through the proxy: %v", seen())
	}

	// unset, downloads follow the global proxy, as they always did
	if err := settings.Save(settings.Settings{Proxy: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	const later = "http://plugins.example.invalid/later.json"
	if _, err := fetchJSONFrom(ctx, later, 1<<10, source.DoOfficial); err != nil || !slices.Contains(seen(), later) {
		t.Fatalf("an unset plugin downloads' proxy didn't follow the global one: %v %v", err, seen())
	}
	if p := proxyOf(bunCommand(ctx, "bun", dir, "add", "x").Env); p != proxy.URL {
		t.Fatalf("bun add's HTTPS_PROXY %q, want the global %q", p, proxy.URL)
	}
}
