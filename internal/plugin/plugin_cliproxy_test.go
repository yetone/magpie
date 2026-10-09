package plugin

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

// 𝕏 on Discord: the Grok plugin's sign-in said "grok login gave no link
// to open". The plugin runs `grok login`, and the host has magpie's proxy
// only as MAGPIE_*_PROXY, so the CLI went out with none where x.ai is
// reached only through one. A program a plugin starts has it, as the
// built-in's `grok login` had it.
func TestPluginCLIHasTheProxy(t *testing.T) {
	sandbox(t)
	const proxy = "http://127.0.0.1:7890"
	t.Setenv("HTTPS_PROXY", proxy)
	t.Setenv("HTTP_PROXY", proxy)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/cliproxy/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	a, err := Authorize(ctx, "cliproxy", 0, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"inherited", "copied"} {
		if got := u.Query().Get(k); got != proxy {
			t.Errorf("a CLI started with the %s environment has HTTPS_PROXY %q, want %q", k, got, proxy)
		}
	}
}

// #1363 (iamyhzhao): Grok, moved onto its plugin, read as signed out
// after nearly every restart of the computer, until moved back to the
// built-in and on again. magpie, started at login, started the plugin
// host before the proxy app had set the system's proxy, and the host
// keeps the proxy it was started with: `grok models`, which renews the
// sign-in, went out with none from then on. A host whose proxy is no
// longer magpie's is replaced at the next call.
func TestPluginHostTakesANewProxy(t *testing.T) {
	sandbox(t)
	prev := proxyLookEvery
	proxyLookEvery = 0
	t.Cleanup(func() { proxyLookEvery = prev })
	// at login: no proxy yet in the environment
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		t.Setenv(k, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/cliproxy/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ask := func() string {
		a, err := Authorize(ctx, "cliproxy", 0, nil, NewAccount)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(a.URL)
		if err != nil {
			t.Fatal(err)
		}
		return u.Query().Get("inherited")
	}
	before := ask()
	// then the proxy app sets one
	const proxy = "http://127.0.0.1:7891"
	if before == proxy {
		t.Fatalf("the host had %s before it was set", proxy)
	}
	t.Setenv("HTTPS_PROXY", proxy)
	t.Setenv("HTTP_PROXY", proxy)
	if got := ask(); got != proxy {
		t.Errorf("after the proxy was set, a CLI the plugin starts has HTTPS_PROXY %q, want %q", got, proxy)
	}
	// and a proxy unchanged keeps the host
	now := func() *host { hostMu.Lock(); defer hostMu.Unlock(); return current }
	h := now()
	ask()
	if now() != h {
		t.Error("the host was replaced with the proxy unchanged")
	}
}
