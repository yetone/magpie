package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeKun is a Kun runtime's model-connections admin API
// (kun/src/server/routes/model-connections.ts, 0.4.5) as magpie uses it,
// keeping its connections in model-connections.v2.json as Kun's registry
// does: a change names the revision it was made against and is refused
// with 409 revision_conflict on another, a connection's id is its name's
// (magpie-2 when magpie is taken), and a model is selected only from its
// connection's list. The connection Kun's default names is never deleted
// on its own; it goes in a provider-config transaction that clears the
// default too (services/provider-configuration-operations.ts).
type fakeKun struct {
	dir string

	mu    sync.Mutex
	ops   []string
	made  []map[string]any
	creds map[string]string
	// previews are provider-config transactions previewed, by id
	previews map[string][]any
}

type fakeKunDoc struct {
	SchemaVersion     int                       `json:"schemaVersion"`
	Revision          int                       `json:"revision"`
	Profiles          map[string]map[string]any `json:"profiles"`
	DefaultProviderID string                    `json:"defaultProviderId,omitempty"`
	DefaultAccountID  string                    `json:"defaultAccountId,omitempty"`
	DefaultModel      string                    `json:"defaultModel,omitempty"`
}

func (f *fakeKun) read() fakeKunDoc {
	d := fakeKunDoc{SchemaVersion: 2, Profiles: map[string]map[string]any{}}
	b, err := os.ReadFile(filepath.Join(f.dir, kunRegistry))
	if err == nil {
		json.Unmarshal(b, &d)
	}
	if d.Profiles == nil {
		d.Profiles = map[string]map[string]any{}
	}
	return d
}

func (f *fakeKun) write(d fakeKunDoc) {
	os.MkdirAll(f.dir, 0o700)
	b, _ := json.MarshalIndent(d, "", "  ")
	os.WriteFile(filepath.Join(f.dir, kunRegistry), b, 0o600)
}

// add puts a connection of the user's own in, as Kun's Settings does.
func (f *fakeKun) add(id, base string, models []string, def bool) {
	d := f.read()
	d.Profiles[id] = map[string]any{"id": id, "accountId": "default", "name": id, "kind": "http", "baseUrl": base, "models": models, "selectedModel": models[0]}
	if def {
		d.DefaultProviderID, d.DefaultAccountID, d.DefaultModel = id, "default", models[0]
	}
	d.Revision++
	f.write(d)
}

func (f *fakeKun) snapshot(d fakeKunDoc) map[string]any {
	var ps []map[string]any
	ids := make([]string, 0, len(d.Profiles))
	for id := range d.Profiles {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := map[string]any{"configured": true, "enabled": true, "credentialStatus": "configured"}
		for k, v := range d.Profiles[id] {
			p[k] = v
		}
		ps = append(ps, p)
	}
	return map[string]any{"schemaVersion": 1, "revision": d.Revision, "providers": ps,
		"defaultProviderId": d.DefaultProviderID, "defaultAccountId": d.DefaultAccountID, "defaultModel": d.DefaultModel}
}

func (f *fakeKun) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fail := func(code int, c, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{"code": c, "message": msg})
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			fail(401, "unauthorized", "missing or invalid runtime token")
			return
		}
		d := f.read()
		var body map[string]any
		if r.Body != nil { // a client's GET through handlerTransport has none
			json.NewDecoder(r.Body).Decode(&body)
		}
		rev := func() bool {
			want := fmt.Sprint(body["expectedRevision"])
			if q := r.URL.Query().Get("expected_revision"); q != "" {
				want = q
			}
			if want != strconv.Itoa(d.Revision) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(map[string]any{"code": "revision_conflict", "message": "stale", "snapshot": f.snapshot(d)})
				return false
			}
			return true
		}
		strs := func(v any) []string {
			var out []string
			for _, x := range v.([]any) {
				out = append(out, x.(string))
			}
			return out
		}
		path := r.URL.Path
		f.ops = append(f.ops, r.Method+" "+path)
		switch {
		case r.Method == "GET" && path == "/v1/model-connections":
		case r.Method == "POST" && path == "/v1/model-connections/connect":
			if !rev() {
				return
			}
			models := strs(body["models"])
			sel, _ := body["selectedModel"].(string)
			if len(models) > 500 || sel != "" && !slices.Contains(models, sel) {
				fail(400, "invalid_request", "selected model is not present in the provider model list")
				return
			}
			id, _ := body["id"].(string)
			if id == "" {
				id, _ = body["name"].(string)
			}
			for base, n := id, 2; d.Profiles[id] != nil; n++ {
				id = fmt.Sprintf("%s-%d", base, n)
			}
			f.made = append(f.made, body)
			if f.creds == nil {
				f.creds = map[string]string{}
			}
			f.creds[id], _ = body["credential"].(string)
			d.Profiles[id] = map[string]any{"id": id, "accountId": "default", "name": body["name"], "kind": body["kind"], "authType": body["authType"],
				"baseUrl": body["baseUrl"], "endpointFormat": body["endpointFormat"], "models": models, "selectedModel": sel}
			if body["select"] == true {
				d.DefaultProviderID, d.DefaultAccountID, d.DefaultModel = id, "default", sel
			}
			d.Revision += 2 // the credential's transaction, then the commit
			f.write(d)
			w.WriteHeader(201)
		case r.Method == "POST" && path == "/v1/model-connections/select":
			if !rev() {
				return
			}
			id, _ := body["providerId"].(string)
			m, _ := body["model"].(string)
			p := d.Profiles[id]
			if p == nil {
				fail(404, "not_found", "provider not found")
				return
			}
			if ms := p["models"].([]any); len(ms) > 0 && !slices.Contains(ms, any(m)) {
				fail(400, "invalid_request", "model is not available for this provider")
				return
			}
			p["selectedModel"] = m
			d.DefaultProviderID, d.DefaultAccountID, d.DefaultModel = id, "default", m
			d.Revision++
			f.write(d)
		case r.Method == "POST" && path == "/v1/provider-config/transactions/preview":
			if !rev() {
				return
			}
			if f.previews == nil {
				f.previews = map[string][]any{}
			}
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.previews)+1)
			f.previews[id], _ = body["operations"].([]any)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previewId": id, "expectedRevision": d.Revision, "operations": body["operations"]})
			return
		case r.Method == "POST" && path == "/v1/provider-config/transactions/commit":
			if !rev() {
				return
			}
			ops, ok := f.previews[fmt.Sprint(body["previewId"])]
			if !ok || body["idempotencyKey"] == "" {
				fail(400, "invalid_request", "Configuration preview expired; review the changes again")
				return
			}
			delete(f.previews, fmt.Sprint(body["previewId"]))
			for _, o := range ops {
				op := o.(map[string]any)
				switch op["kind"] {
				case "set-default-selection":
					if op["selection"] != nil {
						fail(400, "invalid_request", "not faked")
						return
					}
					d.DefaultProviderID, d.DefaultAccountID, d.DefaultModel = "", "", ""
				case "remove-connection":
					id := fmt.Sprint(op["connectionId"])
					if d.Profiles[id] == nil {
						fail(400, "invalid_request", "connection not found")
						return
					}
					delete(d.Profiles, id)
				default:
					fail(400, "invalid_request", "not faked")
					return
				}
			}
			if d.DefaultProviderID != "" && d.Profiles[d.DefaultProviderID] == nil {
				fail(400, "invalid_request", "Deleted connection still has references: default.providerId")
				return
			}
			d.Revision++
			f.write(d)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"committedRevision": d.Revision, "applied": true})
			return
		case strings.HasPrefix(path, "/v1/model-connections/"):
			id := strings.TrimPrefix(path, "/v1/model-connections/")
			p := d.Profiles[id]
			if p == nil {
				fail(404, "not_found", "provider not found")
				return
			}
			if !rev() {
				return
			}
			switch r.Method {
			case "PATCH":
				if ms, ok := body["models"]; ok {
					p["models"] = ms
				}
			case "DELETE":
				if d.DefaultProviderID == id {
					fail(400, "invalid_request", "Deleted connection still has references: default.providerId")
					return
				}
				delete(d.Profiles, id)
			default:
				fail(405, "method_not_allowed", "")
				return
			}
			d.Revision++
			f.write(d)
		default:
			fail(404, "not_found", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f.snapshot(f.read()))
	})
}

// serveFakeKun runs a Kun runtime for the data folder dir on a loopback
// port, found as Kun's CLI finds it: runtime.json's baseUrl and token.
func serveFakeKun(t *testing.T, dir string) *fakeKun {
	t.Helper()
	f := &fakeKun{dir: dir}
	f.write(f.read())
	token := "rt-" + t.Name()
	srv := httptest.NewServer(f.handler(token))
	t.Cleanup(srv.Close)
	b, _ := json.Marshal(map[string]any{"version": 2, "pid": os.Getpid(), "host": "127.0.0.1", "baseUrl": srv.URL, "runtimeToken": token, "insecure": false})
	os.WriteFile(filepath.Join(dir, "runtime.json"), b, 0o600)
	t.Setenv("KUN_DATA_DIR", dir)
	was := kunLive
	kunLive = kunConnect
	t.Cleanup(func() { kunLive = was })
	return f
}

// runningKuns are the Kun runtimes every test but kun_test.go's own finds
// running (main_test.go), one a data folder, served in the process: Kun
// takes magpie only while it runs.
var runningKuns = struct {
	sync.Mutex
	at map[string]*fakeKun
}{at: map[string]*fakeKun{}}

func runningKun(dir string) *kunClient {
	runningKuns.Lock()
	f := runningKuns.at[dir]
	if f == nil {
		f = &fakeKun{dir: dir}
		runningKuns.at[dir] = f
	}
	runningKuns.Unlock()
	return &kunClient{http: &http.Client{Transport: handlerTransport{f.handler("")}}, base: "http://localhost"}
}

func kunTestHome(t *testing.T) (home, dir string) {
	t.Helper()
	home, _ = museHome(t) // magpie with deepseek/pro and deepseek/flash
	t.Setenv("KUN_DATA_DIR", "")
	dir = filepath.Join(home, ".kun", "data")
	os.MkdirAll(dir, 0o700)
	return home, dir
}

// Picking a magpie model for Kun adds magpie as a provider connection
// through the running Kun's own admin API, as its Settings > Providers
// does: an HTTP one at the gateway's /v1, spoken to as chat completions,
// with Kun's own key, the models picked for Kun, and the pick as Kun's
// default. Kun's default before is picked again when magpie steps out,
// and the connection removed (zhongxingyuyes on X).
func TestKunAddsMagpieAsAProvider(t *testing.T) {
	home, dir := kunTestHome(t)
	f := serveFakeKun(t, dir)
	f.add("deepseek", "https://api.deepseek.com", []string{"deepseek-chat", "deepseek-reasoner"}, true)
	a := kun(home)
	if a.Dir != dir || !a.Detected() {
		t.Fatalf("Kun at %s not found (%s)", dir, a.Dir)
	}
	fm := a.Field("model")
	if got := fm.Get(); got != "deepseek-chat" || a.Wired() {
		t.Fatalf("own: %q wired %v", got, a.Wired())
	}
	for range 2 { // twice: one connection, Kun's own default stashed once
		if err := fm.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.made) != 1 {
		t.Fatalf("made %d connections: %v", len(f.made), f.made)
	}
	c := f.made[0]
	for k, v := range map[string]any{"id": "magpie", "name": "magpie", "kind": "http", "authType": "api-key", "baseUrl": gatewayV1(), "endpointFormat": "chat_completions", "credential": agentKey("kun"), "selectedModel": "deepseek/pro", "probe": false, "select": true} {
		if c[k] != v {
			t.Errorf("connect %s = %v, want %v", k, c[k], v)
		}
	}
	if ms, _ := c["models"].([]any); !slices.Contains(ms, any("deepseek/pro")) {
		t.Errorf("models %v", c["models"])
	}
	if got := fm.Get(); got != "magpie/deepseek/pro" || !a.Wired() {
		t.Errorf("connected not read back: %q", got)
	}
	if msg := a.Check(); msg != "" {
		t.Errorf("Check: %s", msg)
	}
	// another of magpie's models is a select on the same connection
	if err := fm.Set("magpie/deepseek/flash"); err != nil || len(f.made) != 1 {
		t.Fatalf("second model: %v, %d made", err, len(f.made))
	}
	if got := fm.Get(); got != "magpie/deepseek/flash" {
		t.Errorf("second model read back as %q", got)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	if d.Profiles["magpie"] != nil || d.DefaultProviderID != "deepseek" || d.DefaultModel != "deepseek-chat" {
		t.Errorf("after disconnect: %+v", d)
	}
	if a.Wired() || unstashPeek(kunStash(dir, "id")) != "" || unstashPeek(kunStash(dir, "prev")) != "" {
		t.Error("still wired, or stashed, after disconnect")
	}
}

// A Kun with no provider of its own: magpie's connection goes in and
// comes out, and Kun is left as it was.
func TestKunFromNothing(t *testing.T) {
	home, dir := kunTestHome(t)
	f := serveFakeKun(t, dir)
	a := kun(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() || len(f.made) != 1 {
		t.Fatalf("connect from nothing: wired %v, %d made", a.Wired(), len(f.made))
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if d := f.read(); len(d.Profiles) != 0 || d.DefaultProviderID != "" {
		t.Errorf("left %+v", d)
	}
}

// A connection named magpie of the user's own takes magpie-2 for
// magpie's, which is still magpie's (its id was stashed); one the user
// made at the gateway by hand is magpie's too; the user's own elsewhere is
// never touched.
func TestKunKnowsItsConnection(t *testing.T) {
	home, dir := kunTestHome(t)
	f := serveFakeKun(t, dir)
	f.add("magpie", "https://example.com/v1", []string{"mine"}, true)
	a := kun(home)
	fm := a.Field("model")
	if err := fm.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	if d.Profiles["magpie-2"] == nil || d.DefaultProviderID != "magpie-2" || unstashPeek(kunStash(dir, "id")) != "magpie-2" {
		t.Fatalf("magpie's connection: %+v, stash %q", d, unstashPeek(kunStash(dir, "id")))
	}
	if !a.Wired() {
		t.Error("magpie-2 not read as magpie's")
	}
	if err := fm.Set(""); err != nil {
		t.Fatal(err)
	}
	d = f.read()
	if d.Profiles["magpie"] == nil || d.Profiles["magpie-2"] != nil || d.DefaultProviderID != "magpie" || d.DefaultModel != "mine" {
		t.Fatalf("after: %+v", d)
	}
	// one added by hand at the gateway
	f.add("gw", gatewayV1()+"/", []string{"deepseek/pro"}, true)
	if !a.Wired() {
		t.Fatal("a connection at the gateway added by hand isn't read as magpie's")
	}
	if err := fm.Set("magpie/deepseek/pro"); err != nil || len(f.made) != 1 {
		t.Errorf("connect over one added by hand: %v, %d made", err, len(f.made))
	}
}

// The gateway on another port: the key goes with the address, so the
// connection is made again there; one pointed at another machine stays.
func TestKunFollowsTheGateway(t *testing.T) {
	home, dir := kunTestHome(t)
	f := serveFakeKun(t, dir)
	a := kun(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3426")
	if msg := a.Check(); !strings.Contains(msg, "baseUrl") {
		t.Errorf("Check on a moved gateway: %q", msg)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	d := f.read()
	p := d.Profiles[unstashPeek(kunStash(dir, "id"))]
	if len(d.Profiles) != 1 || p == nil || p["baseUrl"] != "http://127.0.0.1:3426/v1" || len(f.made) != 2 || d.DefaultModel != "deepseek/pro" {
		t.Fatalf("after the move: %+v (%d made)", d, len(f.made))
	}
	if f.creds[p["id"].(string)] != agentKey("kun") {
		t.Error("the key isn't the moved gateway's")
	}
}

// Kun changes its connections only while it runs; the switch says to
// start it rather than writing a registry Kun keeps its own way.
func TestKunNeedsItRunning(t *testing.T) {
	home, dir := kunTestHome(t)
	t.Setenv("KUN_DATA_DIR", dir)
	was := kunLive
	kunLive = kunConnect
	t.Cleanup(func() { kunLive = was })
	// runtime.json left by a Kun that quit
	os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"version":2,"baseUrl":"http://127.0.0.1:1","runtimeToken":"x"}`), 0o600)
	a := kun(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); !errors.Is(err, errKunDown) {
		t.Errorf("connect with Kun stopped: %v", err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Errorf("disconnect with nothing connected: %v", err)
	}
	if err := a.Sync(); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, kunRegistry)); err == nil {
		t.Error("registry written while Kun is stopped")
	}
}

// A runtime.json naming another machine is never sent the token.
func TestKunOnlyOnThisComputer(t *testing.T) {
	_, dir := kunTestHome(t)
	for base, ok := range map[string]bool{"http://127.0.0.1:18899": true, "http://localhost:1": true, "http://[::1]:2": true, "http://192.168.1.2:18899": false, "http://evil.example:80": false, "file:///x": false} {
		os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"baseUrl":"`+base+`","runtimeToken":"t"}`), 0o600)
		if got := kunAt(dir) != nil; got != ok {
			t.Errorf("%s: %v, want %v", base, got, ok)
		}
	}
}

// Kun's data folder is $KUN_DATA_DIR, else ~/.kun/data.
func TestKunDataDir(t *testing.T) {
	home := syncHome(t)
	if d := kunDataDir(home); d != filepath.Join(home, ".kun", "data") {
		t.Errorf("default: %s", d)
	}
	other := filepath.Join(home, "elsewhere")
	t.Setenv("KUN_DATA_DIR", other)
	if d := kunDataDir(home); d != other {
		t.Errorf("KUN_DATA_DIR: %s", d)
	}
}
