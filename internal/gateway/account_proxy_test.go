package gateway

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// chatgptAt stands in for the ChatGPT backend, or for a proxy in front of
// it: it notes each request's path and account (chatgpt-account-id), and
// answers a turn for acct-1 and acct-2 with their allowance used up, so a
// turn moves on through every account, and acct-3's with "pong".
type chatgptAt struct {
	mu   sync.Mutex
	name string
	got  []string // "<path> <account>"
}

func (c *chatgptAt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	acct := r.Header.Get("chatgpt-account-id")
	c.mu.Lock()
	c.got = append(c.got, r.URL.Path+" "+acct)
	c.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/responses"):
		if acct != "acct-3" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong via `+c.name+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	case strings.HasSuffix(r.URL.Path, "/wham/usage"):
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_after_seconds":600}}}`)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[]}`)
	}
}

// saw is the accounts sent a request to a path ending in suffix.
func (c *chatgptAt) saw(suffix string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, g := range c.got {
		if path, acct, _ := strings.Cut(g, " "); strings.HasSuffix(path, suffix) {
			out = append(out, acct)
		}
	}
	return out
}

func (c *chatgptAt) reset() {
	c.mu.Lock()
	c.got = nil
	c.mu.Unlock()
}

// Each of a subscription's accounts goes through its own proxy (gakki:
// 是否可以为不同的codex账号设置不同的代理): of three Codex accounts, one
// through the proxy it names, one direct though Codex and Settings both
// name one, and one, following Codex's, through Codex's — a turn that
// moves over all three, and each account's usage, alike. An account set
// back to following goes through Codex's proxy, and with Codex's cleared,
// through the global one; ftp:// is refused.
func TestAccountProxy(t *testing.T) {
	codexSignedIn(t, "spare@example.com", "third@example.com")
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	own, codexs, global, vendor := &chatgptAt{name: "own"}, &chatgptAt{name: "codex's"}, &chatgptAt{name: "global"}, &chatgptAt{name: "vendor"}
	var servers []*httptest.Server
	for _, h := range []http.Handler{own, codexs, global, vendor} {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		servers = append(servers, s)
	}
	ownProxy, codexProxy, globalProxy, backend := servers[0], servers[1], servers[2], servers[3]
	all := []*chatgptAt{own, codexs, global, vendor}
	resetAll := func() {
		for _, x := range all {
			x.reset()
		}
	}

	// the backend at a name no proxy stands in front of (loopback never
	// goes through one): dialled directly, chatgpt.test is the vendor
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); strings.HasSuffix(host, ".test") {
			addr = backend.Listener.Addr().String()
		}
		return d.DialContext(ctx, network, addr)
	}
	transport := func() *http.Transport {
		return &http.Transport{Proxy: netproxy.Func, DialContext: dial, ResponseHeaderTimeout: 10 * time.Second}
	}
	defaultTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = netproxy.Dispatch(transport())
	t.Cleanup(func() { http.DefaultClient.Transport = defaultTransport })
	was := provider.CodexBase
	provider.CodexBase = "http://chatgpt.test/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	st := settings.Load()
	st.Proxy = globalProxy.URL
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	bad := *p
	bad.AccountProxies = map[string]string{"spare@example.com": "ftp://10.0.0.1:21"}
	if err := provider.Save(bad); err == nil {
		t.Fatal("an account's ftp:// proxy was taken")
	}
	p.Proxy = codexProxy.URL
	p.AccountProxies = map[string]string{"Me@Example.com": " " + ownProxy.URL + " ", "spare@example.com": "direct", "third@example.com": ""}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("codex"); p.Proxy != codexProxy.URL || len(p.AccountProxies) != 2 ||
		p.AccountProxies["me@example.com"] != ownProxy.URL || p.AccountProxies["spare@example.com"] != "direct" {
		t.Fatalf("kept %q %v", p.Proxy, p.AccountProxies)
	}
	b, _ := os.ReadFile(provider.Path())
	if !strings.Contains(string(b), `"accountProxies"`) || strings.Contains(string(b), "third@example.com") {
		t.Fatalf("providers.json: %s", b)
	}

	// a turn over the three: acct-1 through its own proxy, acct-2 direct
	// to the backend, acct-3 through Codex's
	s := New()
	s.client = &http.Client{Transport: netproxy.Dispatch(transport())}
	turn := func() (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := turn()
	if code != 200 || !strings.Contains(body, "pong via codex's") {
		t.Fatalf("turn: %d %s", code, body)
	}
	want := func(what, suffix string, by map[*chatgptAt][]string) {
		t.Helper()
		for _, x := range all {
			if got := sorted(x.saw(suffix)...); !slices.Equal(got, by[x]) {
				t.Fatalf("%s: %s was sent %v, want %v", what, x.name, got, by[x])
			}
		}
	}
	want("turn", "/responses", map[*chatgptAt][]string{own: {"acct-1"}, vendor: {"acct-2"}, codexs: {"acct-3"}})

	// each account's usage goes the same ways
	usage := func() {
		t.Helper()
		resetAll()
		for _, u := range []string{"me@example.com", "spare@example.com", "third@example.com"} {
			provider.StaleAllowance("codex", u)
		}
		for u, q := range provider.LoginUsage(context.Background(), "codex") {
			if q.Error != "" {
				t.Fatalf("%s's usage: %s", u, q.Error)
			}
		}
	}
	usage()
	want("usage", "/wham/usage", map[*chatgptAt][]string{own: {"acct-1"}, vendor: {"acct-2"}, codexs: {"acct-3"}})

	// acct-1 back to following Codex's proxy; then Codex's cleared, the
	// accounts following it follow the global one
	p, _ = provider.Find("codex")
	delete(p.AccountProxies, "me@example.com")
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	usage()
	want("acct-1 following Codex's", "/wham/usage", map[*chatgptAt][]string{vendor: {"acct-2"}, codexs: sorted("acct-1", "acct-3")})
	p, _ = provider.Find("codex")
	p.Proxy = ""
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	usage()
	want("Codex following the global", "/wham/usage", map[*chatgptAt][]string{vendor: {"acct-2"}, global: sorted("acct-1", "acct-3")})
}

func sorted(s ...string) []string { slices.Sort(s); return s }

// A Claude Code run for one of its accounts (the bridge's, a warm-up's)
// gets that account's own proxy in its *_PROXY, or else Claude's.
func TestClaudeAccountProxyEnv(t *testing.T) {
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(provider.Path(), []byte(`{"providers":[{"id":"claude","key":"","proxy":"http://10.0.0.9:7890","accountProxies":{"a@example.com":"socks5://10.0.0.8:1080","b@example.com":"direct"}}]}`), 0o600)
	env := func(user string) string {
		for _, kv := range netproxy.EnvWith(claudeProxy(provider.ViaLogin(context.Background(), "claude", user)), []string{"PATH=/bin"}) {
			if v, ok := strings.CutPrefix(kv, "HTTPS_PROXY="); ok {
				return v
			}
		}
		return ""
	}
	for user, want := range map[string]string{"A@example.com": "socks5://10.0.0.8:1080", "b@example.com": "", "c@example.com": "http://10.0.0.9:7890"} {
		if got := env(user); got != want {
			t.Errorf("%s: HTTPS_PROXY %q, want %q", user, got, want)
		}
	}
}

// Each of a provider's keys goes through its own proxy (Beyfish_Wang on
// X: 不同 key 走不同的代理): of three keys, the first through the proxy it
// names, the second direct though the provider and Settings both name one,
// and the third, following the provider's, through the provider's — a turn
// moved over all three by the first two's 429s. A key removed takes its
// proxy with it, and a key's ftp:// proxy is refused.
func TestKeyProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	type seen struct {
		mu   sync.Mutex
		keys []string
	}
	// each stands in for the vendor, or for a proxy in front of it: it
	// notes the key, and answers k-3 alone
	var own, provs, global, vendor seen
	var servers []*httptest.Server
	for _, x := range []*seen{&own, &provs, &global, &vendor} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.ReadAll(r.Body)
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			x.mu.Lock()
			x.keys = append(x.keys, key)
			x.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if key != "k-3" {
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"rate limited"}}`)
				return
			}
			io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
		}))
		t.Cleanup(s.Close)
		servers = append(servers, s)
	}
	ownProxy, provProxy, globalProxy, backend := servers[0], servers[1], servers[2], servers[3]
	// the vendor at a name no proxy stands in front of (loopback never goes
	// through one): dialled directly, vendor.test is the backend
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); strings.HasSuffix(host, ".test") {
			addr = backend.Listener.Addr().String()
		}
		return d.DialContext(ctx, network, addr)
	}
	st := settings.Load()
	st.Proxy = globalProxy.URL
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://vendor.test/v1", Models: []string{"m1"}, Key: "k-1",
		Keys: []provider.KeyAccount{{Key: "k-2"}, {Key: "k-3"}}, Proxy: provProxy.URL}
	p.AccountProxies = map[string]string{provider.KeyID("k-2"): "ftp://10.0.0.1:21"}
	if err := provider.Save(p); err == nil {
		t.Fatal("a key's ftp:// proxy was taken")
	}
	p.AccountProxies = map[string]string{provider.KeyID("k-1"): " " + ownProxy.URL + " ", provider.KeyID("k-2"): "direct", provider.KeyID("k-3"): "", "gone": ownProxy.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	got, _ := provider.Find("relay")
	if want := map[string]string{provider.KeyID("k-1"): ownProxy.URL, provider.KeyID("k-2"): "direct"}; !maps.Equal(got.AccountProxies, want) {
		t.Fatalf("kept %v, want %v", got.AccountProxies, want)
	}

	s := New()
	s.client = &http.Client{Transport: netproxy.Dispatch(&http.Transport{Proxy: netproxy.Func, DialContext: dial, ResponseHeaderTimeout: 10 * time.Second})}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-k-3") {
		t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		name string
		x    *seen
		want []string
	}{{"own", &own, []string{"k-1"}}, {"vendor", &vendor, []string{"k-2"}}, {"provider's", &provs, []string{"k-3"}}, {"global", &global, nil}} {
		if !slices.Equal(c.x.keys, c.want) {
			t.Fatalf("%s proxy was sent %v, want %v", c.name, c.x.keys, c.want)
		}
	}

	// the first key removed: its proxy goes with it
	if err := provider.RemoveKey("relay", provider.KeyID("k-1")); err != nil {
		t.Fatal(err)
	}
	if got, _ = provider.Find("relay"); !maps.Equal(got.AccountProxies, map[string]string{provider.KeyID("k-2"): "direct"}) {
		t.Fatalf("after removing k-1: %v", got.AccountProxies)
	}
}
