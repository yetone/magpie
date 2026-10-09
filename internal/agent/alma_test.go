package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// fakeAlma is Alma's API as its spec has it: providers and settings in
// memory, and every write counted.
type fakeAlma struct {
	mu        sync.Mutex
	providers []map[string]any
	settings  map[string]any
	writes    []string
	keys      []string // the API keys sent
	n         int
}

func (f *fakeAlma) provider(id string) map[string]any {
	for _, p := range f.providers {
		if p["id"] == id {
			return p
		}
	}
	return nil
}

func (f *fakeAlma) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api, providers, id, models
	switch {
	case r.URL.Path == "/api/health":
		reply(200, map[string]any{"status": "ok"})
	case r.URL.Path == "/api/settings" && r.Method == "GET":
		reply(200, f.settings)
	case r.URL.Path == "/api/settings" && r.Method == "PUT":
		f.settings = body
		reply(200, body)
	case r.URL.Path == "/api/models":
		var out []map[string]any
		for _, p := range f.providers {
			ms, _ := p["models"].([]any)
			for _, m := range ms {
				if o, ok := m.(map[string]any); ok {
					m = o["id"]
				}
				out = append(out, map[string]any{"id": p["id"].(string) + ":" + m.(string), "name": m, "provider": p["name"], "providerId": p["id"]})
			}
		}
		reply(200, out)
	case r.URL.Path == "/api/providers" && r.Method == "GET":
		reply(200, f.providers)
	case r.URL.Path == "/api/providers" && r.Method == "POST":
		f.n++
		f.keys = append(f.keys, fmt.Sprint(body["apiKey"]))
		body["id"] = fmt.Sprintf("p%d", f.n)
		body["models"], body["availableModels"] = []any{}, []any{}
		f.providers = append(f.providers, body)
		reply(201, body)
	case len(parts) == 3 && r.Method == "PUT":
		p := f.provider(parts[2])
		if k, ok := body["apiKey"]; ok {
			f.keys = append(f.keys, fmt.Sprint(k))
		}
		for k, v := range body {
			p[k] = v
		}
		reply(200, p)
	case len(parts) == 3 && r.Method == "DELETE":
		for i, p := range f.providers {
			if p["id"] == parts[2] {
				f.providers = append(f.providers[:i], f.providers[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case len(parts) == 4 && parts[3] == "models" && r.Method == "PUT":
		p := f.provider(parts[2])
		// as Alma: models kept as given, availableModels without what
		// capabilities were sent; a model's capabilityOverrides those sent,
		// none if null, and the ones it had if none were
		p["models"] = body["models"]
		if av, ok := body["availableModels"].([]any); ok {
			had := map[any]map[string]any{}
			old, _ := p["availableModels"].([]any)
			for _, m := range old {
				if o, ok := m.(map[string]any); ok {
					had[o["id"]] = o
				}
			}
			for _, m := range av {
				o := m.(map[string]any)
				delete(o, "capabilities")
				if c, sent := o["capabilityOverrides"]; !sent {
					if prev, ok := had[o["id"]]["capabilityOverrides"]; ok {
						o["capabilityOverrides"] = prev
					}
				} else if c == nil {
					delete(o, "capabilityOverrides")
				}
			}
			p["availableModels"] = av
		}
		reply(200, p)
	default:
		http.NotFound(w, r)
	}
}

// startAlma serves a fake Alma, with a provider of the user's and settings
// magpie doesn't know, and points magpie at it.
func startAlma(t *testing.T) *fakeAlma {
	t.Helper()
	f := &fakeAlma{
		providers: []map[string]any{{"id": "own", "name": "My OpenAI", "type": "openai", "baseURL": "https://api.openai.com/v1",
			"enabled": true, "apiKey": "encrypted", "models": []any{"gpt-4o"},
			"availableModels": []any{map[string]any{"id": "gpt-4o", "name": "GPT-4o", "capabilities": map[string]any{"vision": true}}}}},
		settings: map[string]any{
			"general": map[string]any{"theme": "dark", "language": "en"},
			"chat":    map[string]any{"defaultModel": "own:gpt-4o", "temperature": 0.7, "modelUsageHistory": map[string]any{"own:gpt-4o": 1.0}},
			"memory":  map[string]any{"enabled": true},
		},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := almaAPI
	almaAPI = srv.URL
	t.Cleanup(func() { almaAPI = old })
	return f
}

// repoint is something else changing a provider's baseURL in Alma: how
// many it changed.
func (f *fakeAlma) repoint(from, to string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.providers {
		if u, _ := p["baseURL"].(string); strings.Contains(u, from) {
			p["baseURL"] = strings.ReplaceAll(u, from, to)
			n++
		}
	}
	return n
}

func (f *fakeAlma) takeWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}

func TestAlma(t *testing.T) {
	syncHome(t)
	f := startAlma(t)
	a := alma()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detected() {
		t.Fatal("not detected")
	}
	fl := a.Field("model")
	if got := fl.Get(); got != "own:gpt-4o" {
		t.Fatalf("get: %q", got)
	}
	// nothing of magpie's in Alma: Sync adds nothing
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("sync without magpie's provider wrote %v", w)
	}

	var ref string
	for _, o := range fl.Options(a.Values()) {
		if o.Ref != "" {
			ref = o.Ref
			break
		}
	}
	if ref == "" {
		t.Fatal("no magpie model offered")
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	var mine []map[string]any
	for _, p := range f.providers {
		if p["name"] == "magpie" {
			mine = append(mine, p)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("magpie providers: %v", f.providers)
	}
	p := mine[0]
	ms, _ := p["models"].([]any)
	if p["type"] != "openai" || p["baseURL"] != gatewayV1() || len(f.keys) != 1 || f.keys[0] != "magpie-alma" || p["enabled"] != true || len(ms) == 0 {
		t.Fatalf("magpie provider: %v", p)
	}
	av, _ := p["availableModels"].([]any)
	if ms[0] != ref || len(av) != len(ms) || av[0].(map[string]any)["id"] != ref || av[0].(map[string]any)["name"] == "" {
		t.Fatalf("models: %v %v", ms, av)
	}
	chat := f.settings["chat"].(map[string]any)
	if chat["defaultModel"] != p["id"].(string)+":"+ref || chat["temperature"] != 0.7 || chat["modelUsageHistory"] == nil ||
		f.settings["general"].(map[string]any)["theme"] != "dark" || f.settings["memory"] == nil {
		t.Fatalf("settings: %v", f.settings)
	}
	if got := fl.Get(); got != "magpie/"+ref {
		t.Fatalf("get: %q", got)
	}
	// magpie draws with nothing: Alma's image generation is left be
	if f.settings["imageGen"] != nil {
		t.Fatalf("image generation set with nothing to draw: %v", f.settings["imageGen"])
	}
	// the user's own provider is as it was
	if own := f.provider("own"); own["baseURL"] != "https://api.openai.com/v1" || len(own["models"].([]any)) != 1 || own["name"] != "My OpenAI" {
		t.Fatalf("own provider: %v", own)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	f.takeWrites()

	// nothing changed: nothing written
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("wrote %v", w)
	}

	// a model gone from the catalog: Sync puts the list right
	p["models"] = []any{"old"}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 1 || w[0] != "PUT /api/providers/"+p["id"].(string)+"/models" {
		t.Fatalf("sync wrote %v", w)
	}
	if ms := p["models"].([]any); len(ms) == 0 || ms[0] != ref {
		t.Fatalf("synced models: %v", ms)
	}

	// Alma's own model again: magpie's provider stays
	if err := a.Apply("model", "own:gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if chat := f.settings["chat"].(map[string]any); chat["defaultModel"] != "own:gpt-4o" || f.provider(p["id"].(string)) == nil {
		t.Fatalf("settings: %v providers: %v", f.settings, f.providers)
	}
	// the gateway moved: choosing magpie's model points it back
	p["baseURL"] = "http://127.0.0.1:9/v1"
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if len(f.providers) != 2 || p["baseURL"] != gatewayV1() {
		t.Fatalf("providers: %v", f.providers)
	}

	// back to Alma's default: magpie's provider comes out, the user's stays
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.providers) != 1 || f.providers[0]["id"] != "own" || f.settings["chat"].(map[string]any)["defaultModel"] != "" {
		t.Fatalf("providers: %v settings: %v", f.providers, f.settings)
	}
}

// A provider whose models Alma lists as the models themselves, not their
// ids, is read as their ids: one such provider left every one of Alma's
// unread, Alma shown as not set with "json: cannot unmarshal object into
// Go struct field almaProvider.models of type string".
func TestAlmaModelObjects(t *testing.T) {
	syncHome(t)
	f := startAlma(t)
	a := alma()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.providers = append(f.providers, map[string]any{"id": "relay", "name": "Relay Test", "type": "custom", "baseURL": "https://relay.example/v1",
		"enabled": true, "models": []any{map[string]any{"id": "gpt-5.5", "name": "gpt-5.5", "enabled": true, "isManual": true,
			"capabilityOverrides": map[string]any{"reasoning": true, "reasoningLevels": []any{"medium", "xhigh"}}}}})
	fl := a.Field("model")
	var ref string
	for _, o := range fl.Options(a.Values()) {
		if o.Ref != "" {
			ref = o.Ref
			break
		}
	}
	if ref == "" {
		t.Fatal("no magpie model offered")
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if got := fl.Get(); got != "magpie/"+ref {
		t.Fatalf("get: %q", got)
	}
	// magpie's own listed as objects too, the same ones: nothing to write
	var mine map[string]any
	for _, p := range f.providers {
		if p["name"] == "magpie" {
			mine = p
		}
	}
	var objs []any
	for _, id := range mine["models"].([]any) {
		objs = append(objs, map[string]any{"id": id, "enabled": true})
	}
	mine["models"] = objs
	f.takeWrites()
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("sync wrote %v", w)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	if r := f.provider("relay"); r["models"].([]any)[0].(map[string]any)["id"] != "gpt-5.5" {
		t.Fatalf("relay provider: %v", r)
	}
}

// Alma not running is no error for Sync, and no drift: it only can't be set.
func TestAlmaDown(t *testing.T) {
	syncHome(t)
	startAlma(t)
	a := alma()
	os.MkdirAll(a.Dir, 0o755)
	var ref string
	for _, o := range a.Field("model").Options(nil) {
		if o.Ref != "" {
			ref = o.Ref
			break
		}
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	srv := almaAPI
	almaAPI = "http://127.0.0.1:1"
	defer func() { almaAPI = srv }()
	if err := a.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := a.Field("model").Get(); got != "magpie/"+ref {
		t.Fatalf("get while down: %q", got)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift while down: %+v", d)
	}
	if err := a.Field("model").Set("magpie/" + ref); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Fatalf("set while down: %v", err)
	}
	if len(a.Field("model").Options(nil)) == 0 {
		t.Fatal("magpie's models not offered while Alma is down")
	}
}

// Under test no Alma is reached unless a fake one is set up.
func TestAlmaNoneUnderTest(t *testing.T) {
	if almaAPI != "" {
		t.Fatalf("almaAPI under test: %q", almaAPI)
	}
}

// A look at what Alma is on reuses what Alma said a moment ago; a change
// sent to Alma, and what a change reads first, always ask it again.
func TestAlmaLook(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets++
		}
		rw.Write([]byte(`{"chat":{"defaultModel":"own:gpt-4o"}}`))
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldFor := almaAPI, almaReadFor
	almaAPI, almaReadFor = srv.URL, time.Minute
	t.Cleanup(func() { almaAPI, almaReadFor = oldAPI, oldFor })

	for range 3 {
		if _, err := almaSettingsSeen(); err != nil {
			t.Fatal(err)
		}
	}
	if gets != 1 {
		t.Fatalf("three looks asked Alma %d times", gets)
	}
	if _, err := almaSettings(); err != nil || gets != 2 {
		t.Fatalf("a read before a change must ask Alma: %d, %v", gets, err)
	}
	if err := almaDo("PUT", "/api/settings", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	almaSettingsSeen()
	if gets != 3 {
		t.Fatalf("a look after a change must ask Alma again: %d", gets)
	}
}

// alma-server, Alma without a desktop, keeps its data in
// ~/.local/share/alma on Linux (its README; $XDG_DATA_HOME/alma, or
// ALMA_DATA_DIR) and answers on Alma's port: magpie finds it there, and its
// magpie provider gets Alma's key on the next Sync, so its requests read as
// Alma's rather than the AI SDK's (Lutra.x on Discord). A Mac's Alma is
// looked for where it was.
func TestAlmaServerKeyed(t *testing.T) {
	home := syncHome(t)
	t.Setenv("ALMA_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	old := almaOS
	t.Cleanup(func() { almaOS = old })
	almaOS = "linux"
	data := filepath.Join(home, ".local", "share", "alma")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	f := startAlma(t)
	f.providers = append(f.providers, map[string]any{"id": "mg", "name": "magpie", "type": "openai", "baseURL": gatewayV1(),
		"enabled": true, "apiKey": "magpie", "models": []any{}, "availableModels": []any{}})
	a := alma()
	if a.Dir != data {
		t.Fatalf("Alma's folder: %q, want alma-server's %q", a.Dir, data)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := f.provider("mg"); p["apiKey"] != "magpie-alma" {
		t.Fatalf("alma-server's magpie provider after Sync: %v", p)
	}

	// $XDG_DATA_HOME and ALMA_DATA_DIR, as alma-server reads them
	xdg := filepath.Join(home, "xdg")
	os.MkdirAll(filepath.Join(xdg, "alma"), 0o755)
	t.Setenv("XDG_DATA_HOME", xdg)
	if d := almaDir("linux"); d != filepath.Join(xdg, "alma") {
		t.Fatalf("with XDG_DATA_HOME: %q", d)
	}
	own := filepath.Join(home, "alma-data")
	os.MkdirAll(own, 0o755)
	t.Setenv("ALMA_DATA_DIR", own)
	if d := almaDir("linux"); d != own {
		t.Fatalf("with ALMA_DATA_DIR: %q", d)
	}
	// the desktop's folder first, and only it on a Mac or Windows
	cfg, _ := os.UserConfigDir()
	desk := filepath.Join(cfg, "alma")
	os.MkdirAll(desk, 0o755)
	if d := almaDir("linux"); d != desk {
		t.Fatalf("with the desktop's folder too: %q", d)
	}
	os.RemoveAll(desk)
	for _, goos := range []string{"darwin", "windows"} {
		if d := almaDir(goos); d != desk {
			t.Fatalf("%s: %q, want only the desktop's %q", goos, d, desk)
		}
	}
}

// Alma's requests say nothing of Alma (they go out as the AI SDK's), so its
// provider carries a key of Alma's: one wired before there was one gets it
// on the next Sync, with nothing else sent, and only once.
// Wiring Alma makes the model magpie draws with Alma's image generation
// model, listed on magpie's provider as one that makes images, when Alma
// has none picked (Sorghum on Discord); one the user picked is theirs.
func TestAlmaImageGen(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "img", Name: "Img", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	const drawn = "img/gpt-image-1"
	if d := gateway.Drawer(); d != drawn {
		t.Fatalf("magpie draws with %q", d)
	}
	f := startAlma(t)
	a := alma()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	image := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return almaImage(f.settings)
	}
	setImage := func(v string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.settings["imageGen"] = map[string]any{"model": v, "aspectRatio": "16:9"}
	}
	// Alma's defaults: Auto
	setImage("")
	if err := a.Apply("model", "magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	for _, q := range f.providers {
		if q["name"] == "magpie" {
			p = q
		}
	}
	pid := p["id"].(string)
	if got := image(); got != pid+":"+drawn {
		t.Fatalf("image generation: %q, want %q", got, pid+":"+drawn)
	}
	if ig := f.settings["imageGen"].(map[string]any); ig["aspectRatio"] != "16:9" ||
		f.settings["chat"].(map[string]any)["defaultModel"] != pid+":relay/glm-4.6" || f.settings["memory"] == nil {
		t.Fatalf("settings: %v", f.settings)
	}
	listed := false
	for _, m := range p["availableModels"].([]any) {
		if o := m.(map[string]any); o["id"] == drawn {
			c, _ := o["capabilityOverrides"].(map[string]any)
			listed = c["imageOutput"] == true
		}
	}
	if ms := p["models"].([]any); !listed || ms[len(ms)-1] != drawn {
		t.Fatalf("magpie's models: %v %v", p["models"], p["availableModels"])
	}

	// the user's own pick, theirs or magpie's, stays: choosing a model
	// again or Sync leaves it
	for _, v := range []string{"own:gpt-image-1", pid + ":relay/glm-4.6"} {
		setImage(v)
		if err := a.Apply("model", "magpie/relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := image(); got != v {
			t.Fatalf("the user's %q became %q", v, got)
		}
	}
	// Auto, gone back to after magpie set it: Sync leaves it
	setImage("")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := image(); got != "" {
		t.Fatalf("Sync filled Auto with %q", got)
	}
	// a drawer magpie gave it before and offers no longer: Sync puts the
	// one it draws with now
	setImage(pid + ":old/gpt-image-0")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := image(); got != pid+":"+drawn {
		t.Fatalf("stale drawer: %q", got)
	}
	// back to Alma's own: its image generation goes back to Auto
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := image(); got != "" || len(f.providers) != 1 {
		t.Fatalf("image generation %q, providers %v", got, f.providers)
	}
}

func TestAlmaKey(t *testing.T) {
	syncHome(t)
	f := startAlma(t)
	a := alma()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.providers = append(f.providers, map[string]any{"id": "mg", "name": "magpie", "type": "openai", "baseURL": gatewayV1(),
		"enabled": true, "apiKey": "magpie", "models": []any{}, "availableModels": []any{}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := f.provider("mg"); p["apiKey"] != "magpie-alma" || p["baseURL"] != gatewayV1() {
		t.Fatalf("provider after Sync: %v", p)
	}
	if len(f.keys) != 1 || f.keys[0] != "magpie-alma" {
		t.Fatalf("keys sent: %v", f.keys)
	}
	f.takeWrites()
	f.keys = nil
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Fatalf("key sent again: %v", f.keys)
	}
	// a key Alma keeps some other way is left be
	f.provider("mg")["apiKey"] = "encrypted"
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Fatalf("encrypted key replaced: %v", f.keys)
	}
}
