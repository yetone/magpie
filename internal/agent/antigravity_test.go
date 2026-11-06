package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// antigravityUser is ~/.antigravity/settings.json with a gateway of the
// user's own (Google's example, on genai), a default model, a model list
// and a key magpie never writes.
const antigravityUser = `{
  "gateway": {
    "enabled": true,
    "url": "https://gateway.cymbal.com",
    "apiKey": "GATEWAY_API_KEY",
    "wireProtocol": "genai",
    "customHeaders": {"X-Cost-Center": "Platform-Engineering"}
  },
  "caCertPath": "/etc/ssl/certs/cymbal-root-ca.pem",
  "models": {
    "defaultModelId": "gemini-3.8-flash",
    "list": [{"modelId": "gemini-3.8-flash"}]
  }
}
`

func antigravityHome(t *testing.T) string {
	t.Helper()
	home := syncHome(t)
	t.Setenv("AGY_LLM_GATEWAY_URL", "")
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

func antigravityFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	return m
}

// Antigravity's app takes a gateway from ~/.antigravity/settings.json
// (Google's Antigravity Enterprise Gateway, ryougi_hachi on X): magpie
// writes its own, on the openai wire protocol with a key naming the app,
// and the model it starts on; the user's own gateway, default model and
// model list come back when it is turned off, and what else the file holds
// is never touched.
func TestAntigravityGateway(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "antigravity" && x.Name == "Antigravity" }) {
		t.Fatal("Antigravity is not among the agents")
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(antigravityUser), 0o600)
	a := antigravity(home)
	f := a.Field("model")
	if f == nil {
		t.Fatal("no model field")
	}
	if got := f.Get(); got != "gemini-3.8-flash" {
		t.Fatalf("get: %q", got)
	}
	if a.Notice() != "" {
		t.Errorf("a notice while on the user's own gateway: %q", a.Notice())
	}

	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got != "magpie/anth/claude-sonnet-5" {
		t.Fatalf("get after set: %q", got)
	}
	file := antigravityFile(t, path)
	var gw map[string]any
	json.Unmarshal(file["gateway"], &gw)
	want := map[string]any{"enabled": true, "url": gateway.URL(), "apiKey": agentKey("antigravity"), "wireProtocol": "openai"}
	if !sameJSON(string(file["gateway"]), want) {
		t.Errorf("gateway: %s, want %v", file["gateway"], want)
	}
	if !strings.HasSuffix(gw["apiKey"].(string), "-antigravity") {
		t.Errorf("the key doesn't name the app: %v", gw["apiKey"])
	}
	if !sameJSON(string(file["models"]), map[string]any{"defaultModelId": "anth/claude-sonnet-5", "listSource": gateway.URL() + "/v1/models?format=antigravity"}) {
		t.Errorf("models: %s (the user's list would narrow the picker, and with no catalog every model's window is 200K)", file["models"])
	}
	if string(file["caCertPath"]) != `"/etc/ssl/certs/cymbal-root-ca.pem"` {
		t.Errorf("caCertPath: %s", file["caCertPath"])
	}
	if a.Notice() == "" {
		t.Error("no notice to restart the app")
	}
	opts := f.Options(map[string]string{"model": f.Get()})
	if !slices.ContainsFunc(opts, func(o Option) bool { return o.Value == "magpie/anth/claude-sonnet-5" }) {
		t.Errorf("magpie's model not offered: %v", opts)
	}

	// another magpie model, then off: the user's as they were
	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(antigravityUser)) {
		t.Errorf("not the user's file again:\n%s", readFile(path))
	}
	if got := f.Get(); got != "gemini-3.8-flash" {
		t.Errorf("get after off: %q", got)
	}
	if a.Notice() != "" {
		t.Errorf("a notice after off: %q", a.Notice())
	}
}

// With no file of the user's, turning magpie on and off leaves nothing of
// it: no gateway, no models.
func TestAntigravityGatewayFromNothing(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	f := antigravity(home).Field("model")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got != "magpie/anth/claude-sonnet-5" {
		t.Fatalf("get: %q", got)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), map[string]any{}) {
		t.Errorf("left behind:\n%s", readFile(path))
	}
}

// Picking a model of the user's own while on magpie gives their gateway
// back and sets that model.
func TestAntigravityOwnModelBack(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(antigravityUser), 0o600)
	f := antigravity(home).Field("model")
	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("gemini-3.1-pro-preview"); err != nil {
		t.Fatal(err)
	}
	file := antigravityFile(t, path)
	var user map[string]json.RawMessage
	json.Unmarshal([]byte(antigravityUser), &user)
	if !sameJSON(string(file["gateway"]), user["gateway"]) {
		t.Errorf("gateway: %s", file["gateway"])
	}
	if got := f.Get(); got != "gemini-3.1-pro-preview" {
		t.Errorf("get: %q", got)
	}
	if _, ok := stashShared()["antigravity:"+path+":defaultModelId"]; ok {
		t.Error("the stashed model was kept after another was picked")
	}
}

// A sync brings the gateway's address and key up to date and keeps what
// the user added to the block; a gateway that isn't magpie's is left alone.
func TestAntigravitySync(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	a := antigravity(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(antigravityUser), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(antigravityUser)) {
		t.Fatalf("the user's gateway synced:\n%s", readFile(path))
	}
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	b := strings.Replace(readFile(path), gateway.URL(), "http://127.0.0.1:1", 1)
	b = strings.Replace(b, `"wireProtocol"`, `"customHeaders": {"X-Mine": "1"}, "wireProtocol"`, 1)
	os.WriteFile(path, []byte(b), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	file := antigravityFile(t, path)
	var gw map[string]any
	json.Unmarshal(file["gateway"], &gw)
	if gw["url"] != gateway.URL() {
		t.Errorf("url: %v", gw["url"])
	}
	if h, _ := gw["customHeaders"].(map[string]any); h["X-Mine"] != "1" {
		t.Errorf("the user's headers lost: %s", file["gateway"])
	}
	// set again (a moved port, Reconnect): the headers stay too
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	file = antigravityFile(t, path)
	gw = nil
	json.Unmarshal(file["gateway"], &gw)
	if h, _ := gw["customHeaders"].(map[string]any); h["X-Mine"] != "1" {
		t.Errorf("the user's headers lost on a set again: %s", file["gateway"])
	}
}

// An organisation's admin_settings.json, or AGY_LLM_GATEWAY_URL, wins over
// the user's file, so the row says it doesn't reach magpie.
func TestAntigravityAdminWins(t *testing.T) {
	home := antigravityHome(t)
	a := antigravity(home)
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %q", c)
	}
	admin := filepath.Join(t.TempDir(), "admin_settings.json")
	was := antigravityAdminPath
	antigravityAdminPath = func() string { return admin }
	t.Cleanup(func() { antigravityAdminPath = was })
	os.WriteFile(admin, []byte(`{"gateway":{"url":"https://gateway.cymbal.com"}}`), 0o644)
	if c := a.Check(); !strings.Contains(c, "admin_settings.json") {
		t.Errorf("check with an admin gateway: %q", c)
	}
	os.Remove(admin)
	t.Setenv("AGY_LLM_GATEWAY_URL", "https://gateway.cymbal.com")
	if c := a.Check(); !strings.Contains(c, "AGY_LLM_GATEWAY_URL") {
		t.Errorf("check with the variable: %q", c)
	}
}

// ~/.antigravity is Antigravity IDE's and agy's too, so it isn't the app:
// the app's bundle, 2.15 or later, or the file its own backend writes is.
func TestAntigravityDetected(t *testing.T) {
	home := antigravityHome(t)
	a := antigravity(home)
	os.MkdirAll(filepath.Join(home, ".antigravity", "extensions"), 0o755)
	if a.Detected() {
		t.Fatal("Antigravity IDE's folder taken for the app")
	}
	if runtime.GOOS == "darwin" {
		plist := func(v string) {
			c := filepath.Join(home, "Applications", "Antigravity.app", "Contents")
			os.MkdirAll(c, 0o755)
			os.WriteFile(filepath.Join(c, "Info.plist"), []byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.google.antigravity</string><key>CFBundleShortVersionString</key><string>`+v+`</string></dict></plist>`), 0o644)
		}
		plist("1.11.3")
		if a.Detected() {
			t.Fatal("an app before 2.15, which takes no gateway, taken for it")
		}
		plist("2.28.2")
		if !a.Detected() {
			t.Fatal("Antigravity 2.28.2 not found")
		}
		os.RemoveAll(filepath.Join(home, "Applications"))
	}
	hub := filepath.Join(home, ".gemini", "antigravity")
	os.MkdirAll(hub, 0o755)
	os.WriteFile(filepath.Join(hub, "agyhub_summaries_proto.pb"), nil, 0o644)
	if !a.Detected() {
		t.Fatal("not found by its backend's file")
	}
}

// Antigravity's picker lists the gateway's models by id, with no level of
// its own to pick (MDX-Tom, #4838): its effort is picked in its row among
// the levels the models it lists have, the default first, kept in magpie
// for the gateway to ask, and offered only while it is on magpie.
func TestAntigravityEffort(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(antigravityUser), 0o600)
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://think.test/v1", Key: "key", Models: []string{"gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://think.test/v1", []catalog.Model{{ID: "gemini-3.8-flash", Efforts: []string{"low", "high"}}}); err != nil {
		t.Fatal(err)
	}
	a := antigravity(home)
	f := a.Field("effort")
	if f == nil {
		t.Fatal("no effort field")
	}
	values := func() []string {
		var out []string
		for _, o := range f.Options(map[string]string{}) {
			out = append(out, o.Value)
		}
		return out
	}
	if got := values(); got != nil {
		t.Fatalf("offered %q while on the user's own gateway", got)
	}
	if err := a.Field("model").Set("magpie/think/gemini-3.8-flash"); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{"", "low", "high"}) {
		t.Fatalf("offered %q", got)
	}
	if err := f.Set("high"); err != nil {
		t.Fatal(err)
	}
	if got, kept := f.Get(), provider.AgentEffort("antigravity"); got != "high" || kept != "high" {
		t.Fatalf("picked high: shown %q, kept %q", got, kept)
	}
	if err := f.Set("hard"); err == nil {
		t.Fatal("a word that isn't a level was kept")
	}
	if !sameJSON(string(antigravityFile(t, path)["gateway"]), map[string]any{"enabled": true, "url": gateway.URL(), "apiKey": agentKey("antigravity"), "wireProtocol": "openai"}) {
		t.Errorf("the pick touched the app's file: %s", readFile(path))
	}
	if err := f.Set(""); err != nil || provider.AgentEffort("antigravity") != "" {
		t.Fatalf("back to the default: %v, kept %q", err, provider.AgentEffort("antigravity"))
	}
}

// Antigravity's app reads the gateway's /v1/models by id alone and gives
// a model its catalog doesn't know a window of 200K tokens (YujunT46308 on
// X); its models.listSource, a catalog read first, takes one per model. So
// magpie points it at the gateway's own, and a catalog of the user's comes
// back with their gateway.
func TestAntigravityCatalogSource(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	own := strings.Replace(antigravityUser, `"list": [`, `"listSource": "https://models.cymbal.com/catalog.json", "list": [`, 1)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(own), 0o600)
	a := antigravity(home)
	f := a.Field("model")
	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	src := func() string {
		var m map[string]any
		json.Unmarshal(antigravityFile(t, path)["models"], &m)
		s, _ := m["listSource"].(string)
		return s
	}
	if got, want := src(), gateway.URL()+"/v1/models?format=antigravity"; got != want {
		t.Fatalf("listSource %q, want %q", got, want)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(own)) {
		t.Errorf("the user's catalog not back:\n%s", readFile(path))
	}
}

// A file magpie wired before it wrote a catalog gets one on the next sync,
// at the gateway's address as it is now; a catalog of the user's found
// there is kept for when magpie steps out.
func TestAntigravitySyncAddsCatalog(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	a := antigravity(home)
	f := a.Field("model")
	if err := f.Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	// as an older magpie left it: no listSource, and one of the user's
	old := `{"gateway":{"enabled":true,"url":"http://127.0.0.1:1","apiKey":"` + agentKey("antigravity") + `","wireProtocol":"openai"},` +
		`"models":{"defaultModelId":"anth/claude-sonnet-5","listSource":"https://models.cymbal.com/catalog.json"}}`
	os.WriteFile(path, []byte(old), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(antigravityFile(t, path)["models"], &m)
	if got, want := m["listSource"], gateway.URL()+"/v1/models?format=antigravity"; got != want {
		t.Fatalf("listSource after sync %v, want %v", got, want)
	}
	// a second sync changes nothing and keeps the stash
	before := readFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != before {
		t.Errorf("a second sync rewrote the file:\n%s", readFile(path))
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(antigravityFile(t, path)["models"], &m)
	if m["listSource"] != "https://models.cymbal.com/catalog.json" {
		t.Errorf("the user's catalog lost: %s", readFile(path))
	}
}
