package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"gopkg.in/yaml.v3"
)

// hanakoTestHome is a sandbox HOME with magpie's catalog (relay/glm-4.6,
// which reasons up to xhigh and takes images) and a HANA_HOME apart from it.
func hanakoTestHome(t *testing.T) (home, dir string) {
	t.Helper()
	home = syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6",`+
		`"reasoning_options":[{"type":"effort","values":["low","high","xhigh"]}],`+
		`"modalities":{"input":["text","image"]},"limit":{"context":204800,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	dir = filepath.Join(t.TempDir(), "hana")
	t.Setenv("HANA_HOME", dir)
	return home, dir
}

func hanakoWrite(t *testing.T, path, s string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

func hanakoJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

func hanakoChatOf(t *testing.T, path string) (map[string]any, string) {
	t.Helper()
	b, _ := os.ReadFile(path)
	var c map[string]any
	yaml.Unmarshal(b, &c)
	m, _ := c["models"].(map[string]any)
	chat, _ := m["chat"].(map[string]any)
	return chat, string(b)
}

// what magpie's provider says for the sandbox's catalog, as JSON
func hanakoWant(t *testing.T) string {
	t.Helper()
	return `{"display_name":"magpie","base_url":"` + gatewayV1() + `","api":"openai-completions","api_key":"magpie-hanako",` +
		`"models":[{"id":"relay/glm-4.6","name":"glm-4.6 · Relay","context":204800,"maxOutput":131072,"image":true,"reasoning":true,"xhigh":true}]}`
}

func TestHanakoHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HANA_HOME", "")
	if d := hanakoHome(home); d != filepath.Join(home, ".hanako") {
		t.Fatal(d)
	}
	t.Setenv("HANA_HOME", "~/hana")
	if d := hanakoHome(home); d != filepath.Join(home, "hana") {
		t.Fatal(d)
	}
	t.Setenv("HANA_HOME", filepath.Join(home, "x"))
	if a := hanako(home); a.Dir != filepath.Join(home, "x") || a.Path != filepath.Join(home, "x", "provider-catalog.json") || a.Detected() {
		t.Fatalf("%+v", a)
	}
	os.MkdirAll(filepath.Join(home, "x"), 0o755)
	if !hanako(home).Detected() {
		t.Fatal("not detected")
	}
}

// OpenHanako not running: magpie writes its files, keeping all else in them.
func TestHanakoFiles(t *testing.T) {
	home, dir := hanakoTestHome(t)
	catalogPath := filepath.Join(dir, "provider-catalog.json")
	hanakoWrite(t, catalogPath, `{
  "catalogVersion": 2,
  "providers": {
    "openai": { "api_key": "sk-x" },
    "mine": { "base_url": "https://x/v1", "api": "openai-completions", "api_key": "k", "models": ["m1", { "id": "m2", "name": "M2" }] }
  },
  "capabilities": { "x": 1 },
  "meta": { "deletedProviders": ["magpie", "old"] }
}
`, 0o600)
	// a plugin left from an older magpie, and one of the user's
	stale := filepath.Join(dir, "provider-plugins", "magpie", "providers", "magpie.json")
	hanakoWrite(t, stale, `{"id":"magpie","defaultBaseUrl":"http://127.0.0.1:1/v1","models":[{"id":"gone"}]}`, 0o600)
	hanakoWrite(t, filepath.Join(dir, "provider-plugins", "other", "providers", "other.json"), `{"id":"other","models":[{"id":"o1"}]}`, 0o600)
	hanakoWrite(t, filepath.Join(dir, "user", "preferences.json"), `{"primaryAgent":"hana"}`, 0o644)
	cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
	hanakoWrite(t, cfg, "# mine\nagent:\n  name: Hana\nmodels:\n  chat:\n    id: gpt-5\n    provider: openai\n  utility: x\n", 0o644)
	first := filepath.Join(dir, "agents", "aaa", "config.yaml")
	hanakoWrite(t, first, "models:\n  chat: {id: m1, provider: mine}\n", 0o644)

	a := hanako(home)
	f := a.Field("model")
	if f.Get() != "openai/gpt-5" {
		t.Fatalf("get: %q", f.Get())
	}
	vals := map[string]string{}
	for _, o := range f.Options(a.Values()) {
		vals[o.Value] = o.Group
	}
	for _, v := range []string{"mine/m1", "mine/m2", "other/o1", "openai/gpt-5", "magpie/relay/glm-4.6"} {
		if _, ok := vals[v]; !ok {
			t.Errorf("options lack %s: %v", v, vals)
		}
	}
	if _, ok := vals["magpie/gone"]; ok {
		t.Errorf("options list magpie's own: %v", vals)
	}
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	c := hanakoJSON(t, catalogPath)
	got, _ := json.Marshal(c["providers"].(map[string]any)["magpie"])
	if !sameJSON(string(got), json.RawMessage(hanakoWant(t))) {
		t.Fatalf("magpie's provider:\n%s\nwant\n%s", got, hanakoWant(t))
	}
	ps := c["providers"].(map[string]any)
	if c["catalogVersion"] != float64(2) || ps["openai"].(map[string]any)["api_key"] != "sk-x" || ps["mine"] == nil ||
		c["capabilities"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("catalog: %v", c)
	}
	if d, _ := json.Marshal(c["meta"].(map[string]any)["deletedProviders"]); string(d) != `["old"]` {
		t.Fatalf("deletedProviders: %s", d)
	}
	if st, _ := os.Stat(catalogPath); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Fatalf("mode %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(stale))); !os.IsNotExist(err) {
		t.Fatal("the old plugin is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-plugins", "other")); err != nil {
		t.Fatal("the user's plugin went")
	}
	chat, raw := hanakoChatOf(t, cfg)
	if chat["id"] != "relay/glm-4.6" || chat["provider"] != "magpie" || !strings.Contains(raw, "# mine") ||
		!strings.Contains(raw, "utility: x") || !strings.Contains(raw, "name: Hana") {
		t.Fatalf("config:\n%s", raw)
	}
	if b, _ := os.ReadFile(first); string(b) != "models:\n  chat: {id: m1, provider: mine}\n" {
		t.Fatalf("another agent changed:\n%s", b)
	}
	if f.Get() != "magpie/relay/glm-4.6" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	// another magpie model keeps what was stashed first
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}

	// once OpenHanako has moved the definition into a plugin of its own,
	// what magpie wrote is still read from there
	hanakoWrite(t, filepath.Join(dir, "provider-plugins", "magpie", "providers", "magpie.json"),
		`{"id":"magpie","displayName":"magpie","authType":"api-key","defaultBaseUrl":"`+gatewayV1()+`","defaultApi":"openai-completions",`+
			`"models":[{"id":"relay/glm-4.6","name":"glm-4.6 · Relay","context":204800,"maxOutput":131072,"image":true,"reasoning":true,"xhigh":true}]}`, 0o600)
	hanakoWrite(t, catalogPath, `{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"},"magpie":{"api_key":"magpie-hanako"}}}`, 0o600)
	before, _ := os.ReadFile(catalogPath)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(catalogPath); string(after) != string(before) || a.Check() != "" {
		t.Fatalf("sync rewrote a current provider:\n%s\ncheck %q", after, a.Check())
	}
	// its key changed behind magpie's back
	hanakoWrite(t, catalogPath, `{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"},"magpie":{"api_key":"sk-other"}}}`, 0o600)
	if !strings.Contains(a.Check(), "api_key") {
		t.Fatalf("check: %q", a.Check())
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	got, _ = json.Marshal(hanakoJSON(t, catalogPath)["providers"].(map[string]any)["magpie"])
	if !sameJSON(string(got), json.RawMessage(hanakoWant(t))) || a.Check() != "" {
		t.Fatalf("sync: %s", got)
	}

	// reset: the model the agent had comes back, magpie's provider goes
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c = hanakoJSON(t, catalogPath)
	if _, ok := c["providers"].(map[string]any)["magpie"]; ok || c["providers"].(map[string]any)["openai"] == nil {
		t.Fatalf("reset catalog: %v", c)
	}
	// marked deleted, or OpenHanako's start-up brings it back from models.json
	if d, _ := json.Marshal(c["meta"].(map[string]any)["deletedProviders"]); string(d) != `["magpie"]` {
		t.Fatalf("reset deletedProviders: %s", d)
	}
	if _, ok := hanakoCurrent(dir); ok {
		t.Fatal("magpie's provider is still there")
	}
	if chat, raw := hanakoChatOf(t, cfg); chat["id"] != "gpt-5" || chat["provider"] != "openai" || !strings.Contains(raw, "# mine") {
		t.Fatalf("reset config:\n%s", raw)
	}
	// and again: nothing of magpie's left to take out
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if chat, _ := hanakoChatOf(t, cfg); chat["id"] != "gpt-5" {
		t.Fatalf("second reset: %v", chat)
	}

	// one of its own models
	if err := f.Set("mine/m2"); err != nil {
		t.Fatal(err)
	}
	if chat, _ := hanakoChatOf(t, cfg); chat["id"] != "m2" || chat["provider"] != "mine" {
		t.Fatalf("own: %v", chat)
	}
}

// A fresh HANA_HOME: the catalog magpie starts is one OpenHanako takes,
// and the agent is the first there is when none is primary.
func TestHanakoFresh(t *testing.T) {
	home, dir := hanakoTestHome(t)
	f := hanako(home).Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err == nil {
		t.Fatal("set with no agent")
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-catalog.json")); !os.IsNotExist(err) {
		t.Fatal("wrote a catalog with no agent to use it")
	}
	hanakoWrite(t, filepath.Join(dir, "agents", "b", "config.yaml"), "agent:\n  name: B\n", 0o644)
	hanakoWrite(t, filepath.Join(dir, "agents", "a", "config.yaml"), "agent:\n  name: A\n", 0o644)
	hanakoWrite(t, filepath.Join(dir, "user", "preferences.json"), `{"primaryAgent":"gone"}`, 0o644)
	if err := hanako(home).Sync(); err != nil { // not wired: nothing to keep current
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-catalog.json")); !os.IsNotExist(err) {
		t.Fatal("sync wired an OpenHanako magpie wasn't in")
	}
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provider-catalog.json")
	c := hanakoJSON(t, path)
	if c["catalogVersion"] != float64(2) || len(c) != 2 {
		t.Fatalf("catalog: %v", c)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Fatalf("mode %v", st.Mode())
	}
	if chat, raw := hanakoChatOf(t, filepath.Join(dir, "agents", "a", "config.yaml")); chat["id"] != "relay/glm-4.6" || chat["provider"] != "magpie" {
		t.Fatalf("a:\n%s", raw)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "agents", "b", "config.yaml")); string(b) != "agent:\n  name: B\n" {
		t.Fatalf("b:\n%s", b)
	}
	// nothing was stashed: the model goes, as does the provider
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if chat, raw := hanakoChatOf(t, filepath.Join(dir, "agents", "a", "config.yaml")); chat != nil || !strings.Contains(raw, "name: A") {
		t.Fatalf("reset:\n%s", raw)
	}
	if c := hanakoJSON(t, path); len(c["providers"].(map[string]any)) != 0 || c["meta"].(map[string]any)["deletedProviders"].([]any)[0] != "magpie" {
		t.Fatalf("reset catalog: %v", c)
	}
	// wired again: no longer deleted
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if c := hanakoJSON(t, path); len(c["meta"].(map[string]any)["deletedProviders"].([]any)) != 0 {
		t.Fatalf("rewired catalog: %v", c)
	}

	// only a plugin left, no catalog: the plugin goes, no catalog is started
	os.Remove(path)
	hanakoWrite(t, filepath.Join(dir, "provider-plugins", "magpie", "providers", "magpie.json"), `{"id":"magpie"}`, 0o600)
	if err := hanakoRemove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("started a catalog")
	}
	if _, ok := hanakoCurrent(dir); ok {
		t.Fatal("plugin still there")
	}
}

type hanakoCall struct{ Method, Path, Auth, Body string }

// fakeHanako is OpenHanako's API as far as magpie uses it, recording
// what it is sent; token is the one it takes.
func fakeHanako(t *testing.T, dir, token string, answer func(hanakoCall) (int, string)) (*[]hanakoCall, *sync.Mutex) {
	t.Helper()
	var calls []hanakoCall
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := hanakoCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if c.Auth != "Bearer "+token {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"forbidden","reason":"bad token"}`)
			return
		}
		if c.Method == "GET" && c.Path == "/api/server/identity" {
			io.WriteString(w, `{"serverId":"srv-1","serverNodeId":"srv-1"}`)
			return
		}
		code, body := answer(c)
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	hanakoWrite(t, filepath.Join(dir, "server-info.json"), `{"pid":1,"port":`+u.Port()+`,"host":"127.0.0.1","token":"tok","version":"0.450.0"}`, 0o600)
	return &calls, &mu
}

// OpenHanako running: every change goes through its API, with its token,
// and magpie writes none of its files.
func TestHanakoLive(t *testing.T) {
	home, dir := hanakoTestHome(t)
	catalogPath := filepath.Join(dir, "provider-catalog.json")
	catalogJSON := `{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"}}}`
	hanakoWrite(t, catalogPath, catalogJSON, 0o600)
	cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
	cfgYAML := "models:\n  chat:\n    id: gpt-5\n    provider: openai\n"
	hanakoWrite(t, cfg, cfgYAML, 0o644)
	calls, mu := fakeHanako(t, dir, "tok", func(hanakoCall) (int, string) { return 200, `{"ok":true}` })
	puts := func() []hanakoCall {
		mu.Lock()
		defer mu.Unlock()
		var out []hanakoCall
		for _, c := range *calls {
			if c.Auth != "Bearer tok" {
				t.Errorf("%s %s without the token: %q", c.Method, c.Path, c.Auth)
			}
			if c.Method == "PUT" {
				out = append(out, c)
			}
		}
		*calls = nil
		return out
	}

	a := hanako(home)
	f := a.Field("model")
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	p := puts()
	if len(p) != 2 || p[0].Path != "/api/config" || p[1].Path != "/api/agents/hana/config" {
		t.Fatalf("puts: %+v", p)
	}
	if !sameJSON(p[0].Body, json.RawMessage(`{"providers":{"magpie":`+hanakoWant(t)+`}}`)) {
		t.Fatalf("provider: %s", p[0].Body)
	}
	if !sameJSON(p[1].Body, json.RawMessage(`{"models":{"chat":{"id":"relay/glm-4.6","provider":"magpie"}}}`)) {
		t.Fatalf("model: %s", p[1].Body)
	}
	if b, _ := os.ReadFile(catalogPath); string(b) != catalogJSON {
		t.Fatalf("catalog written: %s", b)
	}
	if b, _ := os.ReadFile(cfg); string(b) != cfgYAML {
		t.Fatalf("config written: %s", b)
	}

	// as OpenHanako would have saved it
	hanakoWrite(t, catalogPath, `{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"},"magpie":{"api_key":"magpie-hanako"}}}`, 0o600)
	hanakoWrite(t, filepath.Join(dir, "provider-plugins", "magpie", "providers", "magpie.json"),
		`{"id":"magpie","displayName":"magpie","defaultBaseUrl":"`+gatewayV1()+`","defaultApi":"openai-completions","models":[{"id":"old"}]}`, 0o600)
	hanakoWrite(t, cfg, "models:\n  chat:\n    id: relay/glm-4.6\n    provider: magpie\n", 0o644)

	// a catalog change goes through it too
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := puts(); len(p) != 1 || p[0].Path != "/api/config" || !sameJSON(p[0].Body, json.RawMessage(`{"providers":{"magpie":`+hanakoWant(t)+`}}`)) {
		t.Fatalf("sync: %+v", p)
	}

	// reset: the model it had, then magpie's provider out
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	p = puts()
	if len(p) != 2 || p[0].Path != "/api/agents/hana/config" || p[1].Path != "/api/config" ||
		!sameJSON(p[0].Body, json.RawMessage(`{"models":{"chat":{"id":"gpt-5","provider":"openai"}}}`)) ||
		!sameJSON(p[1].Body, json.RawMessage(`{"providers":{"magpie":null}}`)) {
		t.Fatalf("reset: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-plugins", "magpie")); err != nil {
		t.Fatal("magpie removed the plugin behind OpenHanako's back")
	}
}

// What OpenHanako turns down is an error, not a write behind its back.
func TestHanakoLiveRefused(t *testing.T) {
	home, dir := hanakoTestHome(t)
	hanakoWrite(t, filepath.Join(dir, "agents", "hana", "config.yaml"), "agent:\n  name: Hana\n", 0o644)
	fakeHanako(t, dir, "tok", func(hanakoCall) (int, string) { return 400, `{"error":"base_url is not allowed"}` })
	err := hanako(home).Field("model").Set("magpie/relay/glm-4.6")
	if err == nil || !strings.Contains(err.Error(), "base_url is not allowed") {
		t.Fatalf("err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-catalog.json")); !os.IsNotExist(err) {
		t.Fatal("wrote the catalog anyway")
	}
}

// server-info.json left by a crash, or another home's server on its port:
// OpenHanako isn't running, and the files are written.
func TestHanakoStale(t *testing.T) {
	home, dir := hanakoTestHome(t)
	cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
	hanakoWrite(t, cfg, "agent:\n  name: Hana\n", 0o644)
	calls, _ := fakeHanako(t, dir, "another", func(hanakoCall) (int, string) { return 200, `{}` })
	if err := hanako(home).Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if c.Method != "GET" {
			t.Fatalf("sent %s %s to a server that isn't its", c.Method, c.Path)
		}
	}
	if chat, raw := hanakoChatOf(t, cfg); chat["provider"] != "magpie" {
		t.Fatalf("config:\n%s", raw)
	}

	hanakoWrite(t, filepath.Join(dir, "server-info.json"), `{"port":1,"token":"tok"}`, 0o600)
	if err := hanako(home).Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := hanakoCurrent(dir); ok {
		t.Fatal("provider still there")
	}
}

// The key magpie gives OpenHanako is the one the gateway knows it by.
func TestHanakoKey(t *testing.T) {
	if k := hanakoProvider().APIKey; k != gateway.TokenFor("hanako") {
		t.Fatal(k)
	}
}
