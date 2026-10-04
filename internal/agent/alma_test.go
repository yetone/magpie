package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

// Alma's requests say nothing of Alma (they go out as the AI SDK's), so its
// provider carries a key of Alma's: one wired before there was one gets it
// on the next Sync, with nothing else sent, and only once.
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
