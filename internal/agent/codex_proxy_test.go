package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// proxyAs has codexProxyFor read env, sys and goos, and proxyProbe answer
// with *works, until the test ends.
func proxyAs(t *testing.T, env map[string]string, sys netproxy.Proxy, goos string, works *bool) {
	t.Helper()
	e, s, o, p := codexProxyEnv, codexSystemProxy, codexProxyOS, proxyProbe
	codexProxyEnv = func(k string) string { return env[k] }
	codexSystemProxy = func() netproxy.Proxy { return sys }
	codexProxyOS = goos
	proxyProbe = func(string, string) bool { return works != nil && *works }
	reachForget()
	t.Cleanup(func() {
		codexProxyEnv, codexSystemProxy, codexProxyOS, proxyProbe = e, s, o, p
		reachForget()
	})
}

// Which proxy Codex sends a request for magpie's 127.0.0.1 through, the way
// its reqwest does (hyper-util's Matcher::from_system) and, with
// respect_system_proxy on, the way codex-rs outbound_proxy does.
func TestCodexProxyFor(t *testing.T) {
	const base = "http://127.0.0.1:3425/v1"
	clash := "http://127.0.0.1:7890"
	plain := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(plain, []byte("model = \"gpt-5.5\"\n"), 0o600)
	respect := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(respect, []byte("model = \"gpt-5.5\"\n\n[features]\nrespect_system_proxy = true\n"), 0o600)
	win := netproxy.Proxy{URL: clash, HTTP: clash, Bypass: []string{"localhost", "127.*", "<local>"}}
	socks := "socks5://127.0.0.1:7891"
	// macOS' entries as scutil reads them: "Web proxy (HTTP)" and "Secure
	// web proxy (HTTPS)" set, or only one of HTTPS and SOCKS
	mac := netproxy.Proxy{URL: "http://127.0.0.1:7892", HTTP: clash, Bypass: []string{"127.0.0.1", "localhost"}}
	macHTTPS := netproxy.Proxy{URL: clash, Bypass: []string{"127.0.0.1"}}
	macSOCKS := netproxy.Proxy{URL: socks, SOCKS: socks, Bypass: []string{"*.local"}}
	cases := []struct {
		name string
		cfg  string
		env  map[string]string
		sys  netproxy.Proxy
		goos string
		want string
	}{
		{"none", plain, nil, netproxy.Proxy{}, "darwin", ""},
		{"HTTP_PROXY", plain, map[string]string{"HTTP_PROXY": clash}, netproxy.Proxy{}, "darwin", clash},
		{"https_proxy only", plain, map[string]string{"HTTPS_PROXY": clash}, netproxy.Proxy{}, "darwin", ""},
		{"ALL_PROXY", plain, map[string]string{"ALL_PROXY": clash}, netproxy.Proxy{}, "linux", clash},
		{"NO_PROXY 127.0.0.1", plain, map[string]string{"HTTP_PROXY": clash, "NO_PROXY": "localhost,127.0.0.1"}, netproxy.Proxy{}, "darwin", ""},
		{"NO_PROXY CIDR", plain, map[string]string{"HTTP_PROXY": clash, "NO_PROXY": "127.0.0.0/8"}, netproxy.Proxy{}, "darwin", ""},
		{"NO_PROXY localhost is not 127.0.0.1", plain, map[string]string{"HTTP_PROXY": clash, "NO_PROXY": "localhost"}, netproxy.Proxy{}, "darwin", clash},
		{"NO_PROXY * is names only", plain, map[string]string{"HTTP_PROXY": clash, "NO_PROXY": "*"}, netproxy.Proxy{}, "darwin", clash},
		// the reporter's: Windows' proxy on, with 127.* in its bypass list,
		// which reqwest doesn't read as 127.0.0.1
		{"Windows 127.*", plain, nil, win, "windows", clash},
		{"Windows 127.0.0.1", plain, nil, netproxy.Proxy{URL: clash, HTTP: clash, Bypass: []string{"127.0.0.1"}}, "windows", ""},
		{"Windows NO_PROXY wins over its list", plain, map[string]string{"NO_PROXY": "example.com"}, netproxy.Proxy{URL: clash, HTTP: clash, Bypass: []string{"127.0.0.1"}}, "windows", clash},
		// a ProxyServer per protocol is no address to reqwest: direct
		{"Windows per protocol", plain, nil, netproxy.Proxy{URL: clash, SOCKS: socks}, "windows", ""},
		{"macOS exceptions not read", plain, nil, mac, "darwin", clash},
		// liu_dyj on X: reqwest takes macOS' HTTP entry alone for an
		// http:// URL; with only HTTPS or SOCKS set Codex goes direct
		{"macOS HTTPS entry alone", plain, nil, macHTTPS, "darwin", ""},
		{"macOS SOCKS entry alone", plain, nil, macSOCKS, "darwin", ""},
		{"macOS SOCKS entry, ALL_PROXY", plain, map[string]string{"ALL_PROXY": socks}, macSOCKS, "darwin", socks},
		{"respect: Windows 127.* goes around", respect, nil, win, "windows", ""},
		{"respect: system proxy", respect, nil, netproxy.Proxy{URL: clash, HTTP: clash, Bypass: []string{"*.local"}}, "darwin", clash},
		{"respect: macOS exceptions", respect, nil, mac, "darwin", ""},
		{"respect: macOS HTTPS entry alone", respect, map[string]string{"HTTP_PROXY": clash}, macHTTPS, "darwin", ""},
		// a SOCKS entry Codex can't use: the environment's then
		{"respect: macOS SOCKS entry, no env", respect, nil, macSOCKS, "darwin", ""},
		{"respect: macOS SOCKS entry, HTTP_PROXY", respect, map[string]string{"HTTP_PROXY": clash}, macSOCKS, "darwin", clash},
		{"respect: macOS SOCKS entry, NO_PROXY", respect, map[string]string{"ALL_PROXY": socks, "NO_PROXY": "127.0.0.1"}, macSOCKS, "darwin", ""},
		// the system says direct, and the environment isn't asked (codex-cli
		// 0.162.1 with HTTP_PROXY set sent nothing to it)
		{"respect: no system proxy, env not read", respect, map[string]string{"HTTP_PROXY": clash}, netproxy.Proxy{}, "darwin", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proxyAs(t, c.env, c.sys, c.goos, nil)
			if got, from := codexProxyFor(c.cfg, base); got != c.want || (got != "" && from == "") {
				t.Fatalf("got %q from %q, want %q", got, from, c.want)
			}
		})
	}
}

// ReneXiong (#2627): the detail named HTTP_PROXY for a value in
// http_proxy, and ALL_PROXY for one in all_proxy, which sent the reader
// looking for a variable that wasn't there. It names the one read.
func TestCodexProxyNamesItsVariable(t *testing.T) {
	const base = "http://127.0.0.1:3425/v1"
	clash := "http://127.0.0.1:7890"
	plain := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(plain, []byte("model = \"gpt-5.5\"\n"), 0o600)
	respect := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(respect, []byte("model = \"gpt-5.5\"\n\n[features]\nrespect_system_proxy = true\n"), 0o600)
	macSOCKS := netproxy.Proxy{URL: "socks5://127.0.0.1:7891", SOCKS: "socks5://127.0.0.1:7891"}
	cases := []struct {
		name string
		cfg  string
		env  map[string]string
		sys  netproxy.Proxy
		from string
	}{
		{"http_proxy", plain, map[string]string{"http_proxy": clash}, netproxy.Proxy{}, "http_proxy"},
		{"HTTP_PROXY first", plain, map[string]string{"HTTP_PROXY": clash, "http_proxy": clash}, netproxy.Proxy{}, "HTTP_PROXY"},
		{"all_proxy", plain, map[string]string{"all_proxy": "socks5://127.0.0.1:7890"}, netproxy.Proxy{}, "all_proxy"},
		{"respect: http_proxy", respect, map[string]string{"http_proxy": clash}, macSOCKS, "http_proxy"},
		{"respect: all_proxy", respect, map[string]string{"all_proxy": clash}, macSOCKS, "all_proxy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proxyAs(t, c.env, c.sys, "darwin", nil)
			p, from := codexProxyFor(c.cfg, base)
			if p == "" || from != c.from {
				t.Fatalf("got %q from %q, want it from %s", p, from, c.from)
			}
			if d := codexProxiedDetail(base, p, from); !strings.Contains(d, "set in "+c.from+",") || !strings.Contains(d, "NO_PROXY where "+c.from+" is set") {
				t.Fatalf("detail: %s", d)
			}
		})
	}
}

// liu_dyj on X: magpie opened from the Dock lacks the NO_PROXY a shell
// profile sets, which the Codex a terminal or the ChatGPT app runs has.
// The login shell's value is Codex's; magpie's own only where the shell
// has none.
func TestCodexEnvTakesLoginShell(t *testing.T) {
	was := loginEnv
	t.Cleanup(func() { loginEnv = was })
	loginEnv = func(k string) string { return map[string]string{"NO_PROXY": "127.0.0.1"}[k] }
	t.Setenv("NO_PROXY", "")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7890")
	if got := codexEnv("NO_PROXY"); got != "127.0.0.1" {
		t.Fatalf("NO_PROXY = %q, want the login shell's", got)
	}
	if got := codexEnv("HTTP_PROXY"); got != "http://127.0.0.1:7890" {
		t.Fatalf("HTTP_PROXY = %q, want magpie's own", got)
	}
	proxyAs(t, nil, netproxy.Proxy{URL: "http://127.0.0.1:7890", HTTP: "http://127.0.0.1:7890"}, "darwin", nil)
	codexProxyEnv = codexEnv
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\n"), 0o600)
	if p, from := codexProxyFor(cfg, "http://127.0.0.1:3425/v1"); p != "" {
		t.Fatalf("NO_PROXY=127.0.0.1 in the shell, still through %q from %q", p, from)
	}
}

// A proxy's own 502 isn't magpie; a proxy that passes the request on is.
func TestRealProxyProbe(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"magpie"}`)
	}))
	defer gw.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream", http.StatusBadGateway)
	}))
	defer dead.Close()
	pass := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(r.RequestURI)
		res, err := http.Get(u.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	defer pass.Close()
	if realProxyProbe(gw.URL+"/v1", dead.URL) {
		t.Fatal("a proxy answering 502 counted as reaching magpie")
	}
	if !realProxyProbe(gw.URL+"/v1", pass.URL) {
		t.Fatal("a proxy passing the request on didn't count")
	}
	// Codex's reqwest has no SOCKS: a socks5:// proxy is asked as an HTTP
	// one at that address. Clash's mixed port passes it on; a SOCKS-only
	// port, which Go would speak SOCKS to, doesn't, and Codex 502s
	if !realProxyProbe(gw.URL+"/v1", "socks5://"+strings.TrimPrefix(pass.URL, "http://")) {
		t.Fatal("an HTTP proxy named socks5:// didn't count, though Codex gets through it")
	}
	if realProxyProbe(gw.URL+"/v1", "socks5://"+socksOnly(t)) {
		t.Fatal("a SOCKS-only proxy counted, though Codex can't speak SOCKS")
	}
}

// socksOnly is the address of a SOCKS5 proxy that speaks nothing else: it
// passes a CONNECT on and drops anything that isn't SOCKS5.
func socksOnly(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				b := make([]byte, 262)
				if _, err := io.ReadFull(c, b[:2]); err != nil || b[0] != 5 {
					return
				}
				if _, err := io.ReadFull(c, b[:b[1]]); err != nil {
					return
				}
				c.Write([]byte{5, 0})
				if _, err := io.ReadFull(c, b[:4]); err != nil || b[3] != 1 {
					return
				}
				if _, err := io.ReadFull(c, b[:6]); err != nil {
					return
				}
				to := net.JoinHostPort(net.IP(b[:4]).String(), strconv.Itoa(int(b[4])<<8|int(b[5])))
				up, err := net.Dial("tcp", to)
				if err != nil {
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				c.SetDeadline(time.Time{})
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String()
}

// Connected to magpie, with the gateway up but Codex's way to it through a
// proxy that answers 502: the row says so, which proxy and where it is set,
// rather than connected (kind "proxied"); once the proxy passes it on, or
// 127.0.0.1 goes around it, connected again.
func TestCodexProxiedDrift(t *testing.T) {
	fakeReach(t, map[string]bool{gateway.URL(): true})
	home, _ := codexAt(t, gateway.URL())
	works := false
	env := map[string]string{"HTTP_PROXY": "http://127.0.0.1:7890"}
	proxyAs(t, env, netproxy.Proxy{}, "darwin", &works)

	d := driftSettled(t, codex(home))
	if d == nil || d.Kind != "proxied" || d.Addr != "http://127.0.0.1:7890" {
		t.Fatalf("drift %+v", d)
	}
	if !strings.Contains(d.Detail, "HTTP_PROXY") || !strings.Contains(d.Detail, "NO_PROXY") || !strings.Contains(d.Detail, "Disconnect") {
		t.Fatalf("detail: %s", d.Detail)
	}
	works = true
	reachForget()
	if d := driftSettled(t, codex(home)); d != nil {
		t.Fatalf("proxy passes it on: %+v", d)
	}
	works = false
	env["NO_PROXY"] = "127.0.0.1"
	reachForget()
	if d := driftSettled(t, codex(home)); d != nil {
		t.Fatalf("127.0.0.1 around the proxy: %+v", d)
	}
}

// #1886: the proxied row's address and its explanation, both shown in the
// GUI, leave out the proxy's user and password, wherever the user wrote
// them; the probe still goes through the proxy whole.
func TestCodexProxiedDriftShowsNoCredentials(t *testing.T) {
	fakeReach(t, map[string]bool{gateway.URL(): true})
	home, _ := codexAt(t, gateway.URL())
	for _, c := range []struct{ proxy, shown string }{
		{"http://alice:s3cret@127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"alice:s3cret@127.0.0.1:7890", "127.0.0.1:7890"},
		{"http://ali%40ce:s3%3Acr%2Fet@127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http://alice@127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"socks5://alice:s3cret@127.0.0.1:7891", "socks5://127.0.0.1:7891"},
		{"socks5h://ali%40ce:s3%40cret@127.0.0.1:7891", "socks5h://127.0.0.1:7891"},
	} {
		proxyAs(t, map[string]string{"ALL_PROXY": c.proxy}, netproxy.Proxy{}, "linux", nil)
		var probed string
		proxyProbe = func(_, p string) bool { probed = p; return false }
		d := driftSettled(t, codex(home))
		if d == nil || d.Kind != "proxied" {
			t.Fatalf("%s: drift %+v", c.proxy, d)
		}
		if d.Addr != c.shown || !strings.Contains(d.Detail, "the proxy at "+c.shown+",") {
			t.Errorf("%s: addr %q, detail %s; want it shown as %s", c.proxy, d.Addr, d.Detail, c.shown)
		}
		for _, secret := range []string{"ali", "s3"} {
			if strings.Contains(d.Addr, secret) || strings.Contains(d.Detail, secret) {
				t.Errorf("%s: shows %q: addr %q, detail %s", c.proxy, secret, d.Addr, d.Detail)
			}
		}
		if probed != c.proxy {
			t.Errorf("%s: probed through %q", c.proxy, probed)
		}
	}
}

// Leon920717 on X: with magpie quit, Codex CLI kept retrying the gateway
// till magpie was open again. The failover base URL a magpie from before
// wrote goes as the magpie serving the gateway quits (EndCodexFailover),
// and the next start's Sync doesn't write it again (TylerBai_loveai on X);
// a base URL the user wrote stays.
func TestCodexFailoverEndsWithMagpie(t *testing.T) {
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	home, read := codexHome(t, string(me), "model = \"gpt-5.5\"\n")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	logins := func(on bool) {
		b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": on,
			"seen": time.Now(), "auth": auth("spare@example.com", "acct-2")}})
		os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
		os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	}
	logins(true)
	cx := codex(home)
	if err := edit.SetTOMLTop(cx.Path, edit.KV{Path: "openai_base_url", Value: here(home).codexURL()}); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.failover": "1"})
	wasKeep := keepCodexDaemon
	t.Cleanup(func() { keepCodexDaemon = wasKeep })
	var asked []string
	keepCodexDaemon = func(_ context.Context, h string, _ time.Time) provider.CodexDaemonCheck {
		asked = append(asked, h)
		return provider.CodexDaemonCheck{}
	}
	if ended, err := EndCodexFailover(); err != nil || !ended {
		t.Fatalf("failover not said ended (%v): the open threads get no relay", err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.5"`) {
		t.Fatalf("magpie quit, Codex still pointed at it:\n%s", cfg)
	}
	// the app-server keeps the base URL it started with: asked to restart
	if len(asked) != 1 || asked[0] != filepath.Dir(cx.Path) {
		t.Fatalf("app-server not brought up to date on quit: %q", asked)
	}
	asked = nil
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("written again on the next start:\n%s", cfg)
	}

	// the user's own base URL, with no failover on, stays
	logins(false)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	base := gateway.URL() + gateway.CodexPath
	if err := edit.SetTOMLTop(cx.Path, edit.KV{Path: "openai_base_url", Value: base}); err != nil {
		t.Fatal(err)
	}
	if ended, err := EndCodexFailover(); err != nil || ended {
		t.Fatalf("the user's own base URL said ended (%v): a relay would take the port for nothing", err)
	}
	if cfg := read(); !strings.Contains(cfg, `openai_base_url = "`+base+`"`) {
		t.Fatalf("the user's base URL was taken:\n%s", cfg)
	}
	if len(asked) != 0 {
		t.Fatalf("app-server restarted with nothing changed: %q", asked)
	}
}

// CC Switch with "unify Codex session history" on (xuxing0727 on X) runs
// the official sign-in as its "custom" mirror, so failover went on that
// table's base_url rather than openai_base_url: one a magpie from before
// wrote goes as the magpie serving the gateway quits.
func TestCodexMirrorFailoverEndsWithMagpie(t *testing.T) {
	// CC Switch's own bytes (src-tauri services/provider/mod.rs)
	const unified = "model_provider = \"custom\"\nmodel = \"gpt-5.5\"\n\n" +
		"[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n"
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	home, read := codexHome(t, string(me), unified)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": true,
		"seen": time.Now(), "auth": auth("spare@example.com", "acct-2")}})
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	wasKeep := keepCodexDaemon
	t.Cleanup(func() { keepCodexDaemon = wasKeep })
	keepCodexDaemon = func(context.Context, string, time.Time) provider.CodexDaemonCheck { return provider.CodexDaemonCheck{} }

	cx := codex(home)
	if err := edit.SetTOMLKey(cx.Path, "model_providers.custom", "base_url", here(home).codexURL()); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.mirror_failover": "custom"})
	if _, err := EndCodexFailover(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "base_url") || !strings.Contains(cfg, `model_provider = "custom"`) || !strings.Contains(cfg, "supports_websockets = true") {
		t.Fatalf("magpie quit, CC Switch's mirror still pointed at it:\n%s", cfg)
	}
}

// At quit, a relay is left at the gateway's own address for the Codex
// processes running then (idkatbro on X: an open thread got 503 until
// magpie was opened again); none when no Codex runs, and none for a
// gateway on another computer's address, which a relay here can't take.
func TestRelayOpenCodexLeftForRunningCodex(t *testing.T) {
	wasStart, wasPIDs := startRelay, codexPIDs
	t.Cleanup(func() { startRelay, codexPIDs = wasStart, wasPIDs })
	type call struct {
		addr string
		pids []int
	}
	var started []call
	startRelay = func(addr string, pids []int) error {
		started = append(started, call{addr, pids})
		return nil
	}
	running := []int{41, 42}
	codexPIDs = func(context.Context) []int { return running }

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3499")
	if err := RelayOpenCodex(); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0].addr != "127.0.0.1:3499" || len(started[0].pids) != 2 {
		t.Fatalf("no relay for the open Codex: %+v", started)
	}
	// a gateway on every interface is reached on loopback, and relayed
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3498")
	if err := RelayOpenCodex(); err != nil || len(started) != 2 || started[1].addr != "127.0.0.1:3498" {
		t.Fatalf("%v %+v", err, started)
	}
	running = nil
	if err := RelayOpenCodex(); err != nil || len(started) != 2 {
		t.Fatalf("a relay with no Codex running: %v %+v", err, started)
	}
	running = []int{41}
	t.Setenv("MAGPIE_ADDR", "192.0.2.7:3425")
	if err := RelayOpenCodex(); err != nil || len(started) != 2 {
		t.Fatalf("a relay for another computer's address: %v %+v", err, started)
	}
}
