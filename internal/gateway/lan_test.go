package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Sharing gates remote access; each authorized remote call carries the same
// named identity as its local counterpart, so usage is attributed by key.
func TestLANGuard(t *testing.T) {
	fresh(t)
	var got, id string
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + "|" + r.Header.Get("x-api-key")
		id = access.Caller(r.Context()).KeyID
	}))
	call := func(from string, hdr ...string) int {
		got, id = "", ""
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	secret, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	keyID := keys[0].ID
	t.Setenv("MAGPIE_ADDR", "")
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusForbidden {
		t.Fatal("unshared remote", c)
	}
	if c := call("127.0.0.1:5000", "Authorization", "Bearer anything"); c != 200 {
		t.Fatal("loopback", c)
	}
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if addr := listenAddr(); addr != "0.0.0.0:"+Port() {
		t.Fatal(addr)
	}
	for _, hdr := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "wrong"}} {
		if c := call("192.168.1.9:5000", hdr...); c != http.StatusUnauthorized {
			t.Fatal("unauthorized remote", hdr, c)
		}
	}
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != 200 || id != keyID || got != "Bearer magpie|magpie" {
		t.Fatal(c, id, got)
	}
	if c := call("127.0.0.1:5000", "Authorization", "Bearer "+secret); c != 200 || id != keyID {
		t.Fatal("local named key", c, id)
	}
	access.Update("off-key", access.Change{Key: keyID})
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusUnauthorized {
		t.Fatal("disabled key", c)
	}
	access.Update("on-key", access.Change{Key: keyID})
	access.Update("remove-key", access.Change{Key: keyID})
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusUnauthorized {
		t.Fatal("removed key", c)
	}
	s.LAN = false
	settings.Save(s)
	// MAGPIE_ADDR on the network, not shared (the Docker image): another
	// machine, a container's host among them, needs an enabled key too
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	for _, hdr := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "wrong"}} {
		for _, from := range []string{"192.168.1.9:5000", "172.17.0.1:40312", "[fd00::1]:5000"} {
			if c := call(from, hdr...); c != http.StatusUnauthorized || id != "" {
				t.Fatal("MAGPIE_ADDR on the network, not shared:", from, hdr, c)
			}
		}
	}
	secret, err = access.Update("add-key", access.Change{Name: "Host"})
	if err != nil {
		t.Fatal(err)
	}
	if c := call("172.17.0.1:40312", "Authorization", "Bearer "+secret); c != 200 || id == "" || got != "Bearer magpie|" {
		t.Fatal("MAGPIE_ADDR on the network, with a key:", c, id, got)
	}
	if c := call("127.0.0.1:5000", "Authorization", "Bearer anything"); c != 200 {
		t.Fatal("loopback, MAGPIE_ADDR on the network:", c)
	}
}

// A gateway on every interface is reached here on loopback.
func TestURLOfWildcard(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	if u := URL(); u != "http://127.0.0.1:3499" {
		t.Fatal(u)
	}
	t.Setenv("MAGPIE_ADDR", "")
	if u := URL(); u != "http://127.0.0.1:3425" {
		t.Fatal(u)
	}
}

// An agent whose User-Agent is only the AI SDK's (Alma) is known by
// the token it was given; the others still by their User-Agent.
func TestAgentOf(t *testing.T) {
	for _, c := range []struct{ auth, key, ua, want string }{
		{"Bearer " + TokenFor("alma"), "", "ai-sdk/openai/2.0.52 ai-sdk/provider-utils/3.0.12 runtime/node.js/v22", "alma"},
		// alma-server's (Alma 0.4's @ai-sdk/openai 4.0.15 under Node 22,
		// whose navigator.userAgent the AI SDK appends): Alma's by the key
		// magpie gives its provider, the AI SDK's without it (Lutra.x)
		{"Bearer " + TokenFor("alma"), "", "ai-sdk/openai/4.0.15 ai-sdk/provider-utils/5.0.10 runtime/node.js/22", "alma"},
		{"Bearer " + Token, "", "ai-sdk/openai/4.0.15 ai-sdk/provider-utils/5.0.10 runtime/node.js/22", "ai-sdk"},
		{"", TokenFor("alma"), "ai-sdk/anthropic/2.0.1", "alma"},
		{"Bearer " + TokenFor("qoder"), "", "Bun/1.4.2", "qoder"},
		{"Bearer " + TokenFor("hanako"), "", "OpenAI/JS 6.0.0", "hanako"},
		{"Bearer " + Token, "", "claude-cli/2.1.0 (external, cli)", "claude-cli"},
		{"Bearer " + Token, "", "ai-sdk/openai/2.0.52", "ai-sdk"},
		{"Bearer " + Token + "-", "", "codex_cli_rs/0.40.0", "codex_cli_rs"},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if c.key != "" {
			r.Header.Set("x-api-key", c.key)
		}
		r.Header.Set("User-Agent", c.ua)
		if got := agentOf(r); got != c.want {
			t.Errorf("%q %q %q: %q, want %q", c.auth, c.key, c.ua, got, c.want)
		}
	}
}

// In a container magpie's own addresses are the container's (Docker's
// bridge, 172.17.x), which a NAS's other devices can't reach: set,
// MAGPIE_PUBLIC_URL is the address shared, and its host the one printed
// for the web page.
func TestPublicURLShared(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	for _, c := range []struct{ env, want, host string }{
		{"http://192.168.1.20:3425", "http://192.168.1.20:3425", "192.168.1.20"},
		{"https://nas.example.com/magpie/", "https://nas.example.com/magpie", "nas.example.com"},
		{"nas.local:13425", "http://nas.local:13425", "nas.local"},
		{"http://[fd00::20]:13425", "http://[fd00::20]:13425", "fd00::20"},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.env)
		if got := LANURLs(); len(got) != 1 || got[0] != c.want {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: %v, want %s", c.env, got, c.want)
		}
		if got := PublicHost(); got != c.host {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: host %q, want %s", c.env, got, c.host)
		}
		if ContainerAddrs() {
			t.Errorf("MAGPIE_PUBLIC_URL=%q still says the addresses are a container's", c.env)
		}
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if PublicHost() != "" {
		t.Error("unset, a host:", PublicHost())
	}
}

// A container is known by Docker's or Podman's marker file, or a runtime
// in PID 1's cgroup; a plain system has none of them.
func TestInContainer(t *testing.T) {
	mk := func(file, body string) string {
		root := t.TempDir()
		if file != "" {
			p := filepath.Join(root, file)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(body), 0o644)
		}
		return root
	}
	for _, c := range []struct {
		file, body string
		want       bool
	}{
		{"", "", false},
		{"proc/1/cgroup", "0::/init.scope\n", false},
		{".dockerenv", "", true},
		{"run/.containerenv", "", true},
		{"proc/1/cgroup", "12:pids:/docker/4f1c…\n", true},
		{"proc/1/cgroup", "0::/kubepods/besteffort/pod1\n", true},
	} {
		if got := inContainer(mk(c.file, c.body)); got != c.want {
			t.Errorf("%s %q: %v", c.file, c.body, got)
		}
	}
}

// A container on the host's network (network_mode: host, as NAS panels
// offer) sees the host's interfaces, Docker's bridges among them, and its
// addresses are the host's: they aren't said to be a container's own. A
// container on a bridge network has its eth0 alone (jiezhengj on Discord:
// magpie in Docker on a NAS).
func TestHostNetwork(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  bool
	}{
		{[]string{"lo", "eth0"}, false},
		{[]string{"lo", "eth0", "eth1"}, false},
		{[]string{"lo", "eth0", "docker0", "br-3f1c2a9b0d4e", "veth1a2b3c4"}, true},
		{[]string{"lo", "ovs_eth0", "docker0"}, true},
		{[]string{"lo", "enp3s0", "cni-podman0"}, true},
		{[]string{"lo", "eth0", "podman0"}, true},
	} {
		if got := hostNetwork(c.names); got != c.want {
			t.Errorf("%v: %v, want %v", c.names, got, c.want)
		}
	}
	was := ifaceNames
	t.Cleanup(func() { ifaceNames = was })
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	ifaceNames = func() []string { return []string{"lo", "eth0", "docker0"} }
	if ContainerAddrs() {
		t.Error("on the host's network, the addresses are said to be a container's")
	}
}

// Another computer lists the models with the key it was given whatever
// else its client sends beside it: an empty or placeholder Authorization
// with the key in x-api-key, a stale token there, "bearer" in lower case
// (悠悠哥 on Discord: a client's model list from magpie shared on a NAS
// failed, while from magpie on 127.0.0.1, which takes any token, it came).
// The request comes as it would from the network: another address, the
// NAS's address as its Host.
func TestLANModelsWithKeyBesideAnotherToken(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	t.Setenv("MAGPIE_ADDR", "")
	keys, secrets := newCaller(t, "PC")
	key := secrets[0]
	var who string
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who = access.Caller(r.Context()).KeyID
		New().Handler().ServeHTTP(w, r)
	}))
	list := func(from, path string, hdr ...string) (int, string) {
		who = ""
		r := httptest.NewRequest("GET", "http://192.168.0.200:3425"+path, nil)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, hdr := range [][]string{
		{"x-api-key", key, "anthropic-version", "2023-06-01"},
		{"x-api-key", key, "Authorization", "Bearer " + key, "anthropic-version", "2023-06-01"},
		{"x-api-key", key, "Authorization", "Bearer "},
		{"x-api-key", key, "Authorization", "Bearer"},
		{"x-api-key", key, "Authorization", "Bearer undefined"},
		{"x-api-key", key, "Authorization", "Bearer magpie"},
		{"Authorization", "bearer " + key},
		{"Authorization", "Bearer magpie", "x-goog-api-key", key},
	} {
		for _, path := range []string{"/v1/models", "/models", "/v1/models?limit=1000"} {
			c, b := list("192.168.0.9:50123", path, hdr...)
			if c != 200 || !strings.Contains(b, `"id":"fake/m1"`) {
				t.Fatalf("%s %q: %d %s", path, hdr, c, b)
			}
			if who != keys[0].ID {
				t.Fatalf("%s %q: counted as %q, not the key", path, hdr, who)
			}
		}
	}
	// a key the gateway doesn't have is refused wherever it is sent
	for _, hdr := range [][]string{
		{"x-api-key", "sk-magpie-wrong", "Authorization", "Bearer magpie"},
		{"Authorization", "Bearer "},
		{"x-api-key", key + "x"},
	} {
		if c, _ := list("192.168.0.9:50123", "/v1/models", hdr...); c != http.StatusUnauthorized {
			t.Fatalf("%q got %d", hdr, c)
		}
	}
	// this computer is counted by the key beside a token too
	if c, _ := list("127.0.0.1:50123", "/v1/models", "Authorization", "Bearer anything", "x-api-key", key); c != 200 || who != keys[0].ID {
		t.Fatalf("loopback: %d, counted as %q", c, who)
	}
}

// ZCode's custom provider asks no model list; its models are typed one a
// line (悠悠哥 on Discord: from magpie on a NAS nothing filled it, while a
// magpie on this computer writes its models into ZCode's own config).
// /v1/models?format=text gives the ids one a line to paste there, opened
// in a browser on another computer with the key as ?key=; without a key it
// is refused as the list is.
func TestLANModelsAsText(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	t.Setenv("MAGPIE_ADDR", "")
	_, secrets := newCaller(t, "PC")
	h := lanGuard(New().Handler())
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://192.168.0.200:3425"+path, nil)
		r.RemoteAddr = "192.168.0.9:50123"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/v1/models?format=text&key=" + secrets[0])
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSuffix(w.Body.String(), "\n"), "\n")
	if !slices.Contains(lines, "fake/m1") || slices.ContainsFunc(lines, func(l string) bool { return l == "" || strings.ContainsAny(l, "{\" ") }) {
		t.Fatalf("lines %q", lines)
	}
	// the JSON list's ids, in its order
	j := get("/v1/models?key=" + secrets[0])
	var list struct{ Data []struct{ ID string } }
	if err := json.Unmarshal(j.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, lines) {
		t.Fatalf("text %q, json %q", lines, ids)
	}
	if w := get("/v1/models?format=text"); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", w.Code)
	}
}

// Every route the gateway serves — the model calls, count_tokens, System
// One, the Responses websocket, images, retrieval, videos, Gemini, the MCP
// sign-ins, Codex's backend and catalog, the quotas, the route and the
// rest — refuses another machine without an enabled gateway key, when
// MAGPIE_ADDR puts the gateway on the network and it isn't shared (the
// Docker image, whose host reaches it from Docker's bridge). Through the
// whole server stack, lanGuard in front of Handler.
func TestNetworkGatewayGuardsEveryRoute(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	if err := settings.Save(settings.Settings{LAN: false}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	// a Claude subscription run is live: its callback token lets in its
	// own callback alone (TestOnlyALiveRunsCallbackSkipsTheKey)
	live := randomToken()
	addCallback(live)
	t.Cleanup(func() { dropCallback(live) })
	routes := [][2]string{
		{"GET", "/"}, {"GET", "/v1"}, {"GET", "/v1/models"}, {"GET", "/models"}, {"GET", "/v1/models/x"},
		{"GET", "/muse-code/models"}, {"GET", "/api/hello"},
		{"GET", "/api/version"}, {"GET", "/api/tags"}, {"POST", "/api/show"},
		{"GET", "/v1/magpie/quotas"}, {"GET", "/v1/magpie/quotas/history"},
		{"GET", provider.RemoteCardsPath}, {"POST", provider.RemoteRefreshPath},
		{"POST", provider.RemoteCodexResetPath + "?user=me@example.com"},
		{"GET", "/v1/magpie/route?session=s"}, {"GET", "/v1/magpie/concurrency"}, {"GET", "/v1/magpie/limit"},
		{"POST", "/v1/chat/completions"}, {"POST", "/chat/completions"},
		{"POST", "/v1/responses"}, {"POST", "/responses"}, {"GET", "/v1/responses"}, {"GET", "/responses"},
		{"POST", "/v1/messages"}, {"POST", "/messages"}, {"POST", "/v1/messages/count_tokens"},
		{"POST", "/v1/systemone"},
		{"POST", "/v1/images/generations"}, {"POST", "/v1/images/edits"},
		{"POST", "/v1/embeddings"}, {"POST", "/v1/rerank"},
		{"POST", "/v1/audio/transcriptions"}, {"POST", "/v1/audio/translations"}, {"POST", "/v1/audio/speech"},
		{"POST", "/v1/videos"}, {"GET", "/v1/videos/v1"}, {"GET", "/v1/videos/v1/content"},
		{"POST", "/_magpie/claude-mcp/tok"}, {"POST", "/mcp/neon"},
		{"POST", CodexPath + "/responses"}, {"GET", CodexCatalogPath},
		{"GET", "/v1beta/models"}, {"POST", "/v1beta/models/m:generateContent"},
		{"GET", "/nowhere"},
	}
	call := func(method, path, from string, hdr ...string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.RemoteAddr = from
		if path == "/v1/responses" || path == "/responses" {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, rt := range routes {
		for _, from := range []string{"172.17.0.1:40312", "192.168.1.9:5000"} {
			for _, hdr := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "anything"}, {"x-goog-api-key", "magpie"}, {"Authorization", "Bearer " + live}} {
				resetAuthThrottle() // test each route's own guard, not the flood limiter that would 429 later tries
				if c, b := call(rt[0], rt[1], from, hdr...); c != http.StatusUnauthorized || !strings.Contains(b, "gateway key") {
					t.Errorf("%s %s from %s with %v: %d %.120s", rt[0], rt[1], from, hdr, c, b)
				}
			}
		}
	}
	// a gateway key gets past the guard; loopback still takes any token
	_, secrets := newCaller(t, "Host")
	settings.Save(settings.Settings{LAN: false}) // newCaller shares magpie
	if c, b := call("GET", "/v1/models", "172.17.0.1:40312", "Authorization", "Bearer "+secrets[0]); c != 200 {
		t.Fatal("with the key:", c, b)
	}
	if c, b := call("GET", "/v1/magpie/quotas", "172.17.0.1:40312", "x-api-key", secrets[0]); c != 200 {
		t.Fatal("quotas with the key:", c, b)
	}
	if c, b := call("GET", "/v1/models", "127.0.0.1:5000", "Authorization", "Bearer magpie"); c != 200 {
		t.Fatal("loopback:", c, b)
	}
}

// TestDefaultGateway reads the bridge's gateway from a container's
// /proc/net/route, as Docker's default bridge writes it (jiezhengj's NAS:
// other containers reach the host at 172.17.0.1).
func TestDefaultGateway(t *testing.T) {
	const docker = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	for _, c := range []struct{ name, table, want string }{
		{"docker's default bridge", docker, "172.17.0.1"},
		{"a compose network", "Iface\tDestination\tGateway\tFlags\neth0\t000013AC\t00000000\t0001\neth0\t00000000\t010013AC\t0003\n", "172.19.0.1"},
		{"no default route", "Iface\tDestination\tGateway\tFlags\neth0\t000011AC\t00000000\t0001\n", ""},
		{"a default route without a gateway", "Iface\tDestination\tGateway\tFlags\neth0\t00000000\t00000000\t0001\n", ""},
		{"no table (not Linux)", "", ""},
	} {
		if got := defaultGateway(c.table); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// in a container it is offered on the gateway's port; on the host's
	// network, where the addresses are the host's own, it is not
	t.Cleanup(func(rt func() string, ifn func() []string, in func() bool) func() {
		return func() { routeTable, ifaceNames, inThisContainer = rt, ifn, in }
	}(routeTable, ifaceNames, inThisContainer))
	routeTable = func() string { return docker }
	ifaceNames = func() []string { return []string{"lo", "eth0"} }
	inThisContainer = func() bool { return true }
	if got, want := ContainerHostURL(), Scheme()+"://172.17.0.1:"+Port(); got != want {
		t.Errorf("in a container: %q, want %q", got, want)
	}
	ifaceNames = func() []string { return []string{"lo", "eth0", "docker0"} }
	if got := ContainerHostURL(); got != "" {
		t.Errorf("on the host's network: %q, want none", got)
	}
	ifaceNames = func() []string { return []string{"lo", "eth0"} }
	inThisContainer = func() bool { return false }
	if got := ContainerHostURL(); got != "" {
		t.Errorf("not in a container: %q, want none", got)
	}
}
