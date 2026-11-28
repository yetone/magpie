package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeMem is Nowledge Mem's backend (nmem-server 0.10.121) as magpie uses
// it, keeping its setting in remote_llm.json as Mem does, with what was
// seen from the real one: POST /remote-llm/config saves a provider and
// makes it the active one, keeps api_base and the key when they are left
// out and puts temperature, max_tokens and timeout back to 0.7, 8192 and
// 60; removing the active provider leaves none active; activating one
// that isn't there is refused; {"enabled": false} alone goes to
// /remote-llm/enabled.
type fakeMem struct {
	dir string

	mu   sync.Mutex
	ops  []string
	keys map[string]string
}

type fakeMemProvider struct {
	Type        string  `json:"type"`
	Model       string  `json:"model"`
	APIBase     string  `json:"api_base,omitempty"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	Timeout     float64 `json:"timeout"`
	KeyEnc      string  `json:"api_key_enc,omitempty"`
}

type fakeMemDoc struct {
	Enabled   bool                        `json:"enabled"`
	Active    string                      `json:"active_provider"`
	Providers map[string]*fakeMemProvider `json:"providers"`
}

func (f *fakeMem) read() fakeMemDoc {
	d := fakeMemDoc{}
	if b, err := os.ReadFile(filepath.Join(f.dir, "remote_llm.json")); err == nil {
		json.Unmarshal(b, &d)
	}
	if d.Providers == nil {
		d.Providers = map[string]*fakeMemProvider{}
	}
	return d
}

func (f *fakeMem) write(d fakeMemDoc) {
	os.MkdirAll(f.dir, 0o700)
	b, _ := json.MarshalIndent(d, "", "  ")
	os.WriteFile(filepath.Join(f.dir, "remote_llm.json"), b, 0o600)
}

// add is a provider the user saved in Mem's Settings > AI.
func (f *fakeMem) add(id, kind, model, base string, temp float64) {
	d := f.read()
	d.Providers[id] = &fakeMemProvider{Type: kind, Model: model, APIBase: base, Temperature: temp, MaxTokens: 4096, Timeout: 120, KeyEnc: "enc"}
	d.Enabled, d.Active = true, id
	f.write(d)
}

func (f *fakeMem) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.keys == nil {
			f.keys = map[string]string{}
		}
		fail := func(code int, msg string) {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{"detail": msg})
		}
		var body map[string]any
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&body)
		}
		d := f.read()
		f.ops = append(f.ops, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /remote-llm/status":
			json.NewEncoder(w).Encode(map[string]any{"configured": d.Active != ""})
			return
		case "GET /remote-llm/config":
			ps := map[string]any{}
			for id, p := range d.Providers {
				ps[id] = map[string]any{"provider": id, "provider_type": p.Type, "model": p.Model, "api_base": p.APIBase, "has_api_key": p.KeyEnc != "", "temperature": p.Temperature, "max_tokens": p.MaxTokens, "timeout": p.Timeout}
			}
			json.NewEncoder(w).Encode(map[string]any{"enabled": d.Enabled, "active_provider": d.Active, "providers": ps})
			return
		case "POST /remote-llm/config":
			id, _ := body["provider"].(string)
			if id == "" {
				fail(422, "provider is required")
				return
			}
			p := d.Providers[id]
			if p == nil {
				p = &fakeMemProvider{}
				d.Providers[id] = p
			}
			p.Type, _ = body["provider_type"].(string)
			p.Model, _ = body["model"].(string)
			if b, ok := body["api_base"].(string); ok {
				p.APIBase = b
			}
			if k, ok := body["api_key"].(string); ok {
				p.KeyEnc = "enc"
				f.keys[id] = k
			}
			p.Temperature, p.MaxTokens, p.Timeout = 0.7, 8192, 60
			if v, ok := body["temperature"].(float64); ok {
				p.Temperature = v
			}
			if v, ok := body["max_tokens"].(float64); ok {
				p.MaxTokens = int(v)
			}
			if v, ok := body["timeout"].(float64); ok {
				p.Timeout = v
			}
			d.Enabled, _ = body["enabled"].(bool)
			d.Active = id
		case "POST /remote-llm/activate":
			id, _ := body["provider"].(string)
			if d.Providers[id] == nil {
				fail(400, "Unknown provider: "+id)
				return
			}
			d.Active = id
		case "POST /remote-llm/provider/remove":
			id, _ := body["provider"].(string)
			delete(d.Providers, id)
			delete(f.keys, id)
			if d.Active == id {
				d.Active = ""
			}
		case "POST /remote-llm/enabled":
			d.Enabled, _ = body["enabled"].(bool)
		default:
			fail(404, "Not Found")
			return
		}
		f.write(d)
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
}

// runningMems are the Mems every test but this file's own finds running
// (main_test.go), one a config folder, served in the process.
var runningMems = struct {
	sync.Mutex
	at map[string]*fakeMem
}{at: map[string]*fakeMem{}}

func runningMem(dir string) *memClient {
	runningMems.Lock()
	f := runningMems.at[dir]
	if f == nil {
		f = &fakeMem{dir: dir}
		runningMems.at[dir] = f
	}
	runningMems.Unlock()
	return &memClient{http: &http.Client{Transport: handlerTransport{f.handler()}}, base: "http://localhost"}
}

// serveFakeMem is the Mem at dir running, for this test alone.
func serveFakeMem(t *testing.T, dir string) *fakeMem {
	t.Helper()
	f := &fakeMem{dir: dir}
	was := memLive
	memLive = func(d string) *memClient {
		if d != dir {
			return nil
		}
		return &memClient{http: &http.Client{Transport: handlerTransport{f.handler()}}, base: "http://localhost"}
	}
	t.Cleanup(func() { memLive = was })
	return f
}

func memTestHome(t *testing.T) (home, dir string) {
	t.Helper()
	home, _ = museHome(t) // magpie with deepseek/pro and deepseek/flash
	dir = filepath.Join(t.TempDir(), "mem")
	t.Setenv("NMEM_APP_CONFIG_DIR", dir)
	t.Setenv("NMEM_API_URL", "")
	os.MkdirAll(dir, 0o700)
	return home, dir
}

// Picking a magpie model for Nowledge Mem saves Mem's own magpie provider
// at the gateway's /v1 with Mem's key and that model, as its first page's
// "选择一个 AI" does, and turns its AI on; stepping out picks the
// provider Mem had again and removes magpie's (xiaomaomiloulou on X).
func TestNowledgeMemUsesMagpie(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("openai_compatible", "openai_compatible", "qwen3", "https://example.com/v1", 0.2)
	a := nowledgeMem(home)
	if a.Dir != dir || !a.Detected() {
		t.Fatalf("Mem at %s not found (%s)", dir, a.Dir)
	}
	fm := a.Field("model")
	if got := fm.Get(); got != "qwen3" || a.Wired() {
		t.Fatalf("own: %q wired %v", got, a.Wired())
	}
	for range 2 { // twice: what Mem had is stashed once
		if err := fm.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	d := f.read()
	p := d.Providers["magpie"]
	if p == nil || p.Type != "magpie" || p.Model != "deepseek/pro" || p.APIBase != gatewayV1() || d.Active != "magpie" || !d.Enabled {
		t.Fatalf("connected: %+v %+v", d, p)
	}
	if f.keys["magpie"] != agentKey("nowledge-mem") {
		t.Errorf("key %q", f.keys["magpie"])
	}
	if got := fm.Get(); got != "magpie/deepseek/pro" || !a.Wired() {
		t.Errorf("connected not read back: %q", got)
	}
	if msg := a.Check(); msg != "" {
		t.Errorf("Check: %s", msg)
	}
	if err := fm.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if got := fm.Get(); got != "magpie/deepseek/flash" {
		t.Errorf("second model read back as %q", got)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d = f.read()
	own := d.Providers["openai_compatible"]
	if d.Providers["magpie"] != nil || d.Active != "openai_compatible" || !d.Enabled || own.Model != "qwen3" || own.Temperature != 0.2 {
		t.Errorf("after disconnect: %+v %+v", d, own)
	}
	if a.Wired() || unstashPeek("nowledge-mem.prev:"+dir) != "" {
		t.Error("still wired, or stashed, after disconnect")
	}
}

// A "magpie" provider the user made in Mem themselves, with a key of
// their own and their own temperature, is put back as it was: its model,
// address and parameters, and its key never sent over.
func TestNowledgeMemKeepsTheUsersMagpie(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("magpie", "magpie", "theirs", "http://192.168.1.9:3425/v1", 0.3)
	f.add("openai", "openai", "gpt-5", "", 0.5)
	a := nowledgeMem(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.keys["magpie"]; sent {
		t.Error("the user's own magpie provider was given magpie's key")
	}
	if p := f.read().Providers["magpie"]; p.APIBase != gatewayV1() || p.Temperature != 0.3 {
		t.Errorf("connected: %+v", p)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	p := d.Providers["magpie"]
	if p == nil || p.Model != "theirs" || p.APIBase != "http://192.168.1.9:3425/v1" || p.Temperature != 0.3 || p.MaxTokens != 4096 || d.Active != "openai" {
		t.Errorf("after disconnect: %+v %+v", d, p)
	}
}

// A "magpie" provider the user pointed at a magpie on a server (Alex on
// Discord: the cloud magpie imported into Mem) is theirs: this magpie
// doesn't count Mem as going through it, says nothing changed its config,
// and leaves the provider alone when Mem is stepped out of.
func TestNowledgeMemMagpieOnAServer(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("magpie", "magpie", "deepseek/pro", "https://magpie.example.com/v1", 0.3)
	a := nowledgeMem(home)
	if a.Wired() {
		t.Error("a magpie on a server taken for this one")
	}
	fm := a.Field("model")
	if got := fm.Get(); got != "magpie.example.com/deepseek/pro" {
		t.Errorf("model read as %q", got)
	}
	// picked again as it reads, it is the same model on the user's provider
	if err := fm.Set(fm.Get()); err != nil {
		t.Fatal(err)
	}
	if p := f.read().Providers["magpie"]; p.Model != "deepseek/pro" {
		t.Errorf("model written as %q", p.Model)
	}
	if msg := a.Check(); msg != "" {
		t.Errorf("Check: %s", msg)
	}
	f.mu.Lock()
	f.ops = nil
	f.mu.Unlock()
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	if p := d.Providers["magpie"]; p == nil || p.APIBase != "https://magpie.example.com/v1" || d.Active != "magpie" || !d.Enabled {
		t.Errorf("the user's provider changed: %+v %+v", d, p)
	}
	f.mu.Lock()
	ops := f.ops
	f.mu.Unlock()
	for _, op := range ops {
		if strings.HasPrefix(op, "POST ") {
			t.Errorf("stepping out wrote to Mem: %v", ops)
			break
		}
	}
	// connected here after all, then stepped out: the server's comes back
	if err := fm.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if p := f.read().Providers["magpie"]; p.APIBase != gatewayV1() || !a.Wired() {
		t.Errorf("connected: %+v", p)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if p := f.read().Providers["magpie"]; p.APIBase != "https://magpie.example.com/v1" || p.Model != "deepseek/pro" || a.Wired() {
		t.Errorf("the server's magpie not put back: %+v", p)
	}
}

// Alex on Discord (11-25): Mem connected here, then the magpie on their
// server imported over Mem's own magpie provider. Disconnect took no
// effect, or removed the server's provider, as it wasn't there before
// magpie. Now it forgets what magpie set and leaves the server's provider
// as the user has it, Mem's AI on it, and the row is no longer connected;
// with Mem not running too. And it is never read as this magpie's to
// begin with (11-27: on a model this magpie serves too, deepseek/pro, it
// still read as connected until Disconnect, which Alex never pressed):
// the server's address doesn't answer as this magpie, on a name or this
// network's address alike, so Mem was switched off magpie. 11-28 (still
// on v0.1.4989): the row stayed among those connected, as changed outside
// magpie, and Disconnect did nothing to it. As the Agents page is drawn
// (Follow) what magpie remembers setting goes, and so it does on a
// Disconnect without the page drawn again.
func TestNowledgeMemServerImportedOver(t *testing.T) {
	for _, c := range []struct {
		name, server, model string
		down, drawn         bool
	}{
		{"a model of this magpie's", "https://magpie.example.com/v1", "deepseek/pro", false, true},
		{"a model of this magpie's, page not drawn again", "https://magpie.example.com/v1", "deepseek/pro", false, false},
		{"a model of this magpie's, Mem not running", "https://magpie.example.com/v1", "deepseek/pro", true, true},
		{"a model of this magpie's, Mem not running, page not drawn again", "https://magpie.example.com/v1", "deepseek/pro", true, false},
		{"a model of the server's alone", "https://magpie.example.com/v1", "cloudonly/m1", false, true},
		{"a magpie on this network", "http://192.168.1.20:3425/v1", "deepseek/pro", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, dir := memTestHome(t)
			f := serveFakeMem(t, dir)
			a := nowledgeMem(home)
			if _, err := a.ConnectHow(); err != nil {
				t.Fatal(err)
			}
			if !a.Wired() {
				t.Fatal("not connected")
			}
			f.add("magpie", "magpie", c.model, c.server, 0.3)
			own := strings.TrimSuffix(strings.SplitN(c.server, "://", 2)[1], "/v1") + "/" + c.model
			// never the address told changed, whose Reconnect wrote this
			// magpie over the server's
			if d := a.Drift(); a.Wired() || d != nil && d.Kind != "replaced" {
				t.Errorf("the server's magpie read as this one: wired %v, %+v", a.Wired(), d)
			}
			if got := a.Field("model").Get(); got != own {
				t.Errorf("model read as %q", got)
			}
			if msg := a.Check(); msg != "" {
				t.Errorf("Check: %s", msg)
			}
			if c.down {
				memLive = func(string) *memClient { return nil }
			}
			if c.drawn {
				if err := a.Follow(); err != nil {
					t.Fatal(err)
				}
				if a.Wired() || a.Drift() != nil {
					t.Errorf("shown connected: wired %v, %+v", a.Wired(), a.Drift())
				}
				if unstashPeek("nowledge-mem.prev:"+dir) != "" || appliedOf("nowledge-mem").Fields != nil {
					t.Error("what magpie set Mem up with is kept")
				}
			}
			f.mu.Lock()
			f.ops = nil
			f.mu.Unlock()
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			d := f.read()
			if p := d.Providers["magpie"]; p == nil || p.APIBase != c.server || p.Model != c.model || d.Active != "magpie" || !d.Enabled {
				t.Errorf("the server's provider changed: %+v %+v", d, p)
			}
			f.mu.Lock()
			ops := f.ops
			f.mu.Unlock()
			for _, op := range ops {
				if strings.HasPrefix(op, "POST ") {
					t.Errorf("stepping out wrote to Mem: %v", ops)
					break
				}
			}
			if a.Wired() || a.Drift() != nil {
				t.Errorf("still connected after Disconnect: wired %v, %+v", a.Wired(), a.Drift())
			}
			if got := a.Field("model").Get(); got != own {
				t.Errorf("model read as %q after", got)
			}
		})
	}
}

// Alex on Discord (11-28): the first Agents page after a start read Mem
// as connected, as the server's address hadn't answered yet, so the
// switch stayed on, and Disconnect then found nothing of magpie's and
// did nothing. The page's first draw waits for that answer.
func TestNowledgeMemServerImportedOverFirstRead(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	a := nowledgeMem(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	f.add("magpie", "magpie", "deepseek/pro", "https://magpie.example.com/v1", 0.3)
	probed := coldMagpie(t, map[string]bool{})
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if got := a.Field("model").Get(); got != "magpie.example.com/deepseek/pro" || a.Wired() || a.Drift() != nil {
		t.Errorf("first read: %q wired %v %+v", got, a.Wired(), a.Drift())
	}
	if p := probed(); len(p) != 1 || p[0] != "https://magpie.example.com" {
		t.Errorf("probed %v", p)
	}
}

// A provider of another name the user added at the server's magpie (an
// OpenAI-compatible one), on a model this magpie serves too, is the
// server's: not connected here, picked again as it reads it is the same
// model, and Disconnect leaves it be.
func TestNowledgeMemOtherProviderAtAServer(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("cloud", "openai_compatible", "deepseek/pro", "https://magpie.example.com/v1", 0.3)
	a := nowledgeMem(home)
	fm := a.Field("model")
	if got := fm.Get(); got != "magpie.example.com/deepseek/pro" || a.Wired() || a.Drift() != nil {
		t.Errorf("read as %q, wired %v %+v", got, a.Wired(), a.Drift())
	}
	if err := fm.Set(fm.Get()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.ops = nil
	f.mu.Unlock()
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	if p := d.Providers["cloud"]; p == nil || p.Model != "deepseek/pro" || p.APIBase != "https://magpie.example.com/v1" || d.Active != "cloud" || !d.Enabled || d.Providers["magpie"] != nil {
		t.Errorf("the user's provider changed: %+v %+v", d, p)
	}
	f.mu.Lock()
	ops := f.ops
	f.mu.Unlock()
	for _, op := range ops {
		if strings.HasPrefix(op, "POST ") {
			t.Errorf("Disconnect wrote to Mem: %v", ops)
			break
		}
	}
	// the same provider at this magpie's gateway is one of this magpie's
	// models still, as before
	d.Providers["cloud"].APIBase = gatewayV1()
	f.write(d)
	if got := fm.Get(); got != "deepseek/pro" {
		t.Errorf("at the gateway: %q", got)
	}
}

// Mem connected here, then its magpie provider pointed at a proxy in
// front of this gateway (#1985), which answers as this magpie: still
// connected, and stepping out puts back what Mem had.
func TestNowledgeMemBehindAProxyOfThisMagpie(t *testing.T) {
	const proxy = "https://ai.corp.example/magpie"
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("openai", "openai", "gpt-5", "", 0.5)
	a := nowledgeMem(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	fakeMagpieAt(t, map[string]bool{proxy: true})
	d := f.read()
	d.Providers["magpie"].APIBase = proxy + "/v1"
	f.write(d)
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" || !a.Wired() || a.Drift() != nil {
		t.Errorf("behind the proxy: %q wired %v %+v", got, a.Wired(), a.Drift())
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d = f.read()
	if d.Providers["magpie"] != nil || d.Active != "openai" || a.Wired() {
		t.Errorf("after disconnect: %+v", d)
	}
}

// A Mem whose AI was off, with no provider at all, is left that way.
func TestNowledgeMemFromNothing(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	a := nowledgeMem(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if d := f.read(); !a.Wired() || d.Active != "magpie" || !d.Enabled {
		t.Fatalf("connect from nothing: %+v", d)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if d := f.read(); len(d.Providers) != 0 || d.Active != "" || d.Enabled {
		t.Errorf("left %+v", d)
	}
}

// A provider the user picked in Mem while connected stays picked when
// magpie steps out, and Mem's AI stays on.
func TestNowledgeMemKeepsALaterPick(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	f.add("openai_compatible", "openai_compatible", "qwen3", "https://example.com/v1", 0.2)
	f.add("openai", "openai", "gpt-5", "", 0.5)
	f.add("openai_compatible", "openai_compatible", "qwen3", "https://example.com/v1", 0.2)
	a := nowledgeMem(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	d.Active = "openai" // picked in Mem's Settings > AI
	f.write(d)
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if d := f.read(); d.Active != "openai" || !d.Enabled || d.Providers["magpie"] != nil {
		t.Errorf("after: %+v", d)
	}
}

// A gateway on a new port reaches magpie's provider on the next sync.
func TestNowledgeMemFollowsTheGateway(t *testing.T) {
	home, dir := memTestHome(t)
	f := serveFakeMem(t, dir)
	a := nowledgeMem(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3426")
	if msg := a.Check(); !strings.Contains(msg, "api_base") {
		t.Errorf("Check on a moved gateway: %q", msg)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := f.read().Providers["magpie"]; p.APIBase != "http://127.0.0.1:3426/v1" || p.Model != "deepseek/pro" {
		t.Errorf("after the move: %+v", p)
	}
}

// Mem changes its AI only while it runs; magpie never writes the file,
// whose keys Mem encrypts.
func TestNowledgeMemNeedsItRunning(t *testing.T) {
	home, dir := memTestHome(t)
	was := memLive
	memLive = func(string) *memClient { return nil }
	t.Cleanup(func() { memLive = was })
	a := nowledgeMem(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); !errors.Is(err, errMemDown) {
		t.Errorf("connect with Mem stopped: %v", err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Errorf("disconnect with nothing connected: %v", err)
	}
	if err := a.Sync(); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "remote_llm.json")); err == nil {
		t.Error("remote_llm.json written while Mem is stopped")
	}
	// connected, then Mem quit: read from its file, and disconnect waits
	os.WriteFile(filepath.Join(dir, "remote_llm.json"), []byte(`{"enabled":true,"active_provider":"magpie","providers":{"magpie":{"type":"magpie","model":"deepseek/pro","api_base":"`+gatewayV1()+`"}}}`), 0o600)
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Errorf("read from the file: %q", got)
	}
	if err := a.Field("model").Set(""); !errors.Is(err, errMemDown) {
		t.Errorf("disconnect with Mem stopped: %v", err)
	}
}

// $NMEM_API_URL naming another machine is never asked.
func TestNowledgeMemOnlyOnThisComputer(t *testing.T) {
	_, dir := memTestHome(t)
	// the nmem CLI's NMEM_API_URL is followed when it names this computer:
	// it is an address, not a folder magpie drops when it isn't absolute
	srv := httptest.NewServer((&fakeMem{dir: dir}).handler())
	t.Cleanup(srv.Close)
	t.Setenv("NMEM_API_URL", srv.URL+"/")
	if c := memConnect(dir); c == nil || c.base != srv.URL {
		t.Fatalf("NMEM_API_URL=%s/ not followed: %+v", srv.URL, c)
	}
	for _, u := range []string{"http://192.168.1.2:14242", "http://evil.example", "file:///x"} {
		t.Setenv("NMEM_API_URL", u)
		if memConnect(dir) != nil {
			t.Errorf("%s reached", u)
		}
	}
}
