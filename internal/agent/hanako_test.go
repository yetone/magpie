package agent

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
		`"models":[{"id":"relay/glm-4.6","name":"GLM-4.6 · Relay","context":204800,"maxOutput":131072,"image":true,"reasoning":true,"xhigh":true}]}`
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
			`"models":[{"id":"relay/glm-4.6","name":"GLM-4.6 · Relay","context":204800,"maxOutput":131072,"image":true,"reasoning":true,"xhigh":true}]}`, 0o600)
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
	calls, mu := fakeHanako(t, dir, "tok", func(c hanakoCall) (int, string) {
		if c.Path == "/api/models" {
			return 200, `{"models":[{"id":"relay/glm-4.6","provider":"magpie"},{"id":"gpt-5","provider":"openai"}]}`
		}
		return 200, `{"ok":true}`
	})
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

// One of OpenHanako's own providers named as one of magpie's (its own
// DeepSeek, and magpie's deepseek): its model isn't magpie's, so the row
// isn't connected on it, and Disconnect from magpie puts it back and is
// done, rather than leaving the row connected on the model it went back to
// (Hu9956, #835).
func TestHanakoOwnProviderNamedAsMagpies(t *testing.T) {
	home, dir := hanakoTestHome(t)
	catalogPath := filepath.Join(dir, "provider-catalog.json")
	// the user's own relay provider, which magpie has one of too
	hanakoWrite(t, catalogPath, `{"catalogVersion":2,"providers":{"relay":{"base_url":"https://x/v1","api":"openai-completions","api_key":"k","models":["glm-4.6"]}}}`, 0o600)
	hanakoWrite(t, filepath.Join(dir, "user", "preferences.json"), `{"primaryAgent":"hana"}`, 0o644)
	cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
	hanakoWrite(t, cfg, "models:\n  chat:\n    id: glm-4.6\n    provider: relay\n", 0o644)

	a := hanako(home)
	if a.Wired() {
		t.Fatal("its own relay/glm-4.6 reads as magpie's")
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatalf("not wired after connect: %v", a.Values())
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if chat, raw := hanakoChatOf(t, cfg); chat["id"] != "glm-4.6" || chat["provider"] != "relay" {
		t.Fatalf("disconnected config:\n%s", raw)
	}
	if _, ok := hanakoCurrent(dir); ok {
		t.Fatal("magpie's provider is still there")
	}
	if a.Wired() {
		t.Fatalf("still connected after Disconnect: %v", a.Values())
	}
}

// hanakoSeal seals s as OpenHanako 1.0 seals its catalog
// (shared/credential-envelope.ts): AES-256-GCM under its keychain's key,
// the header as additional data, nonce and body+tag in base64.
func hanakoSeal(t *testing.T, s string) string {
	t.Helper()
	const header = "HANA-SECRET-1.AES-256-GCM"
	block, _ := aes.NewCipher(bytes.Repeat([]byte{7}, 32))
	gcm, _ := cipher.NewGCM(block)
	nonce := bytes.Repeat([]byte{1}, 12)
	body := gcm.Seal(nil, nonce, []byte(s), []byte(header))
	return header + "." + base64.StdEncoding.EncodeToString(nonce) + "." + base64.StdEncoding.EncodeToString(body) + "\n"
}

// Since 1.0 OpenHanako seals its catalog: magpie never writes over it, keeps
// its own definition current in its plugin file, and says that adding or
// taking magpie out needs OpenHanako open.
func TestHanakoSealed(t *testing.T) {
	home, dir := hanakoTestHome(t)
	catalogPath := filepath.Join(dir, "provider-catalog.json")
	sealed := hanakoSeal(t, `{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"},"magpie":{"api_key":"magpie-hanako"}}}`)
	hanakoWrite(t, catalogPath, sealed, 0o600)
	plugin := filepath.Join(dir, "provider-plugins", "magpie", "providers", "magpie.json")
	// as OpenHanako 1.0 moved it there, from an older magpie on another port
	hanakoWrite(t, plugin, `{"id":"magpie","displayName":"magpie","authType":"api-key","defaultBaseUrl":"http://127.0.0.1:1/v1",`+
		`"defaultApi":"openai-completions","models":[{"id":"old"}]}`, 0o600)
	hanakoWrite(t, filepath.Join(dir, "provider-plugins", "magpie", "manifest.json"), `{"id":"magpie","type":"provider-plugin","schemaVersion":1,"provider":"magpie"}`, 0o600)
	cfg := filepath.Join(dir, "agents", "hana", "config.yaml")
	cfgYAML := "models:\n  chat:\n    id: relay/glm-4.6\n    provider: magpie\n"
	hanakoWrite(t, cfg, cfgYAML, 0o644)
	unchanged := func(what string) {
		t.Helper()
		if b, _ := os.ReadFile(catalogPath); string(b) != sealed {
			t.Fatalf("%s: the sealed catalog was written:\n%s", what, b)
		}
	}

	a := hanako(home)
	if c := a.Check(); !strings.Contains(c, "base_url (magpie.json)") || strings.Contains(c, "api_key") {
		t.Fatalf("check: %q", c)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	unchanged("sync")
	p := hanakoJSON(t, plugin)
	ms, _ := json.Marshal(p["models"])
	want := map[string]any{}
	json.Unmarshal([]byte(hanakoWant(t)), &want)
	wantMs, _ := json.Marshal(want["models"])
	if p["defaultBaseUrl"] != gatewayV1() || p["defaultApi"] != "openai-completions" || p["id"] != "magpie" || p["authType"] != "api-key" || !sameJSON(string(ms), json.RawMessage(wantMs)) {
		t.Fatalf("plugin: %v", p)
	}
	if st, _ := os.Stat(plugin); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// the key it can't read is unknown, not gone: nothing to say, nothing to write
	if c := a.Check(); c != "" {
		t.Fatalf("check after sync: %q", c)
	}
	before, _ := os.ReadFile(plugin)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(plugin); string(after) != string(before) {
		t.Fatalf("sync rewrote a current plugin:\n%s", after)
	}

	// taking magpie out needs OpenHanako: nothing changes meanwhile
	f := a.Field("model")
	if err := f.Set(""); err == nil || !strings.Contains(err.Error(), "open OpenHanako") {
		t.Fatalf("reset: %v", err)
	}
	unchanged("reset")
	if b, _ := os.ReadFile(cfg); string(b) != cfgYAML {
		t.Fatalf("reset changed the config:\n%s", b)
	}
	if _, err := os.Stat(plugin); err != nil {
		t.Fatal("reset took the plugin")
	}

	// nor can magpie be added with no plugin to keep it in
	os.RemoveAll(filepath.Join(dir, "provider-plugins"))
	hanakoWrite(t, cfg, "models:\n  chat:\n    id: gpt-5\n    provider: openai\n", 0o644)
	if err := f.Set("magpie/relay/glm-4.6"); err == nil || !strings.Contains(err.Error(), "open OpenHanako") {
		t.Fatalf("add: %v", err)
	}
	unchanged("add")
	if chat, _ := hanakoChatOf(t, cfg); chat["provider"] != "openai" {
		t.Fatalf("add changed the model: %v", chat)
	}

	// while it runs, its API does it all
	calls, mu := fakeHanako(t, dir, "tok", func(hanakoCall) (int, string) { return 200, `{"ok":true}` })
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	var paths []string
	for _, c := range *calls {
		if c.Method == "PUT" {
			paths = append(paths, c.Path)
		}
	}
	mu.Unlock()
	if strings.Join(paths, " ") != "/api/config /api/agents/hana/config" {
		t.Fatalf("live puts: %v", paths)
	}
	unchanged("live")
}

// fakeHanako10 answers as OpenHanako 1.0.0-beta's server does: PUT
// /api/agents/:id/config takes models.chat only as a model it has, given
// as provider/id ({id, provider} or "provider/id"), else 400 "models.chat
// requires provider/id" (#1040); GET /api/models lists its models.
func fakeHanako10(t *testing.T, dir string, models ...string) (*[]hanakoCall, *sync.Mutex) {
	t.Helper()
	var list []map[string]string
	for _, m := range models {
		p, id, _ := strings.Cut(m, "/")
		list = append(list, map[string]string{"id": id, "name": id, "provider": p})
	}
	listJSON, _ := json.Marshal(map[string]any{"models": list, "current": nil})
	return fakeHanako(t, dir, "tok", func(c hanakoCall) (int, string) {
		switch {
		case c.Method == "GET" && c.Path == "/api/models":
			return 200, string(listJSON)
		case c.Method == "PUT" && strings.HasPrefix(c.Path, "/api/agents/"):
			var b struct {
				Models map[string]any `json:"models"`
			}
			json.Unmarshal([]byte(c.Body), &b)
			if chat, there := b.Models["chat"]; there {
				var p, id string
				switch v := chat.(type) {
				case map[string]any:
					id, _ = v["id"].(string)
					p, _ = v["provider"].(string)
				case string:
					p, id, _ = strings.Cut(v, "/")
				}
				if id == "" || p == "" {
					return 400, `{"error":"models.chat requires provider/id"}`
				}
				if !slices.Contains(models, p+"/"+id) {
					return 400, `{"error":"model ` + p + `/` + id + ` not found"}`
				}
			}
		}
		return 200, `{"ok":true}`
	})
}

// #1040: taking magpie out of a running OpenHanako 1.0 puts the agent on
// one of OpenHanako's own models, as provider/id, whatever magpie stashed:
// nothing (the agent was put on magpie in OpenHanako), an id alone, or a
// model whose provider has gone. With no model of its own to go to it says
// so, and leaves magpie's provider and the stash as they were.
func TestHanakoLiveBackNeedsProviderID(t *testing.T) {
	for _, tc := range []struct {
		name, stashed string
		models        []string
		want, err     string
	}{
		{name: "nothing stashed", models: []string{"magpie/relay/glm-4.6", "openai/gpt-5", "zai/glm-4.6"}, want: `{"id":"gpt-5","provider":"openai"}`},
		{name: "an id alone", stashed: "glm-4.6", models: []string{"magpie/relay/glm-4.6", "openai/gpt-5", "zai/glm-4.6"}, want: `{"id":"glm-4.6","provider":"zai"}`},
		{name: "provider/id", stashed: "openrouter/anthropic/claude", models: []string{"openai/gpt-5", "openrouter/anthropic/claude"}, want: `{"id":"anthropic/claude","provider":"openrouter"}`},
		{name: "provider gone", stashed: "gone/x", models: []string{"magpie/relay/glm-4.6", "openai/gpt-5"}, want: `{"id":"gpt-5","provider":"openai"}`},
		{name: "only magpie's", stashed: "gone/x", models: []string{"magpie/relay/glm-4.6"}, err: "no model of its own"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := hanakoTestHome(t)
			hanakoWrite(t, filepath.Join(dir, "provider-catalog.json"),
				`{"catalogVersion":2,"providers":{"openai":{"api_key":"sk-x"},"magpie":{"api_key":"magpie-hanako"}}}`, 0o600)
			hanakoWrite(t, filepath.Join(dir, "agents", "xiaojing", "config.yaml"), "models:\n  chat:\n    id: relay/glm-4.6\n    provider: magpie\n", 0o644)
			a := hanako(home)
			k := "hanako:" + dir + ":xiaojing:chat"
			if tc.stashed != "" {
				stash(map[string]string{k: tc.stashed})
			}
			calls, mu := fakeHanako10(t, dir, tc.models...)
			err := a.Field("model").Set("")
			mu.Lock()
			var puts []hanakoCall
			for _, c := range *calls {
				if c.Method == "PUT" {
					puts = append(puts, c)
				}
			}
			mu.Unlock()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err: %v", err)
				}
				if len(puts) != 0 {
					t.Fatalf("changed OpenHanako anyway: %+v", puts)
				}
				if stashLoad()[k] != tc.stashed {
					t.Fatal("stash lost")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(puts) != 2 || puts[0].Path != "/api/agents/xiaojing/config" || puts[1].Path != "/api/config" ||
				!sameJSON(puts[0].Body, json.RawMessage(`{"models":{"chat":`+tc.want+`}}`)) ||
				!sameJSON(puts[1].Body, json.RawMessage(`{"providers":{"magpie":null}}`)) {
				t.Fatalf("puts: %+v", puts)
			}
			if _, there := stashLoad()[k]; there {
				t.Fatal("stash kept")
			}
		})
	}
}
