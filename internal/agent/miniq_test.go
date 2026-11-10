package agent

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/gateway"
)

// fakeMiniqDaemon is a miniq-daemon's local WebSocket as the daemon serves
// it (server.rs ws_upgrade, gateway/settings.rs): a token in the query,
// JSON-RPC settings.update and settings.get, and an event sent to every
// client before any reply. It writes daemon.json into dir and keeps the
// provider it was last told, as the daemon keeps its settings in memory.
type fakeMiniqDaemon struct {
	mu       sync.Mutex
	provider map[string]any // what the daemon holds; nil for none
	updates  []map[string]any
	refuse   bool
}

func newFakeMiniqDaemon(t *testing.T, dir string, provider map[string]any) *fakeMiniqDaemon {
	t.Helper()
	d := &fakeMiniqDaemon{provider: provider}
	const token = "tok en&1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" || r.URL.Query().Get("token") != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		c.Write(ctx, websocket.MessageText, []byte(`{"type":"session_updated","id":1}`))
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			req := gjson.ParseBytes(msg)
			out := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.Get("id").Raw)}
			d.mu.Lock()
			switch req.Get("method").String() {
			case "settings.update":
				var p map[string]any
				json.Unmarshal([]byte(req.Get("params.provider").Raw), &p)
				d.updates = append(d.updates, p)
				if d.refuse {
					out["error"] = map[string]any{"code": -32602, "message": "provider baseUrl is invalid"}
					break
				}
				d.provider = map[string]any{"baseUrl": p["baseUrl"], "model": p["model"], "apiProtocol": p["apiProtocol"]}
				out["result"] = map[string]any{"provider": d.provider}
			case "settings.get":
				out["result"] = map[string]any{"provider": d.provider}
			}
			d.mu.Unlock()
			b, _ := json.Marshal(out)
			c.Write(ctx, websocket.MessageText, b)
		}
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	b, _ := json.Marshal(map[string]any{"port": p, "token": token, "pid": 1})
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "daemon.json"), b, 0o600)
	return d
}

func (d *fakeMiniqDaemon) last(t *testing.T) map[string]any {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.updates) == 0 {
		t.Fatal("the daemon was told nothing")
	}
	return d.updates[len(d.updates)-1]
}

func miniqHome(t *testing.T) (home, dir string) {
	t.Helper()
	home, _ = museHome(t)
	t.Setenv("MINIQ_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", "")
	return home, filepath.Join(home, ".local", "share", "miniq")
}

func TestMiniq(t *testing.T) {
	home, dir := miniqHome(t)
	path := filepath.Join(dir, "settings.json")
	os.MkdirAll(dir, 0o755)
	// a user's own, as miniQ's Settings writes it: Zaiwen's endpoint and
	// key, an MCP server, approvals and remote access
	orig := `{
  "provider": {"baseUrl": "https://oneapi.zaiwenai.com/v1", "apiKey": "sk-zaiwen", "model": "glm-4.6", "apiProtocol": "auto", "reasoningEffort": "high"},
  "mcpServers": {"docs": {"command": "npx", "args": ["docs-mcp"]}},
  "approvalMode": "auto",
  "remoteAccess": {"enabled": true, "relayUrl": "wss://relay.example", "deviceName": "mac", "deviceId": "d1"}
}
`
	os.WriteFile(path, []byte(orig), 0o600)
	var want map[string]any
	json.Unmarshal([]byte(orig), &want)
	read := func() map[string]any {
		t.Helper()
		var m map[string]any
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return m
	}
	daemon := newFakeMiniqDaemon(t, dir, map[string]any{"baseUrl": "https://oneapi.zaiwenai.com/v1", "model": "glm-4.6"})

	a := miniq(home)
	if a.Path != path || a.Dir != dir {
		t.Fatalf("path: %s %s", a.Path, a.Dir)
	}
	f := a.Field("model")
	if f.Get() != "glm-4.6" || a.Check() != "" || a.Notice() != "" {
		t.Fatalf("own: %q %q %q", f.Get(), a.Check(), a.Notice())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "glm-4.6 magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: what the user had is stashed once
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	m := read()
	key := agentKeyAt("miniq", gateway.URL())
	wantP := map[string]any{"baseUrl": gatewayV1(), "apiKey": key, "model": "deepseek/pro", "apiProtocol": "chat_completions"}
	if !reflect.DeepEqual(m["provider"], wantP) {
		t.Fatalf("provider: %v", m["provider"])
	}
	for _, k := range []string{"mcpServers", "approvalMode", "remoteAccess"} {
		if !reflect.DeepEqual(m[k], want[k]) {
			t.Fatalf("%s: %v", k, m[k])
		}
	}
	// the running daemon was told, so it uses magpie now and won't write
	// Zaiwen's provider back at its next settings change
	if got := daemon.last(t); !reflect.DeepEqual(got, wantP) {
		t.Fatalf("daemon told: %v", got)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" || a.Notice() != "" {
		t.Fatalf("on: %q %q %q", f.Get(), a.Check(), a.Notice())
	}

	// magpie's key taken out by hand
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), key, "sk-other", 1)), 0o600)
	if !strings.Contains(a.Check(), "apiKey") {
		t.Fatalf("check: %q", a.Check())
	}
	os.WriteFile(path, b, 0o600)

	// stepping out puts back the user's provider whole, key and effort
	// too, and tells the daemon, with their key
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	m = read()
	if !reflect.DeepEqual(m["provider"], want["provider"]) {
		t.Fatalf("restored: %v", m["provider"])
	}
	if got := daemon.last(t); got["baseUrl"] != "https://oneapi.zaiwenai.com/v1" || got["apiKey"] != "sk-zaiwen" || got["model"] != "glm-4.6" {
		t.Fatalf("daemon told: %v", got)
	}

	// a model of the user's own while magpie's is on steps magpie out with
	// that model on their provider
	f.Set("magpie/deepseek/flash")
	if err := f.Set("glm-4.5"); err != nil {
		t.Fatal(err)
	}
	if p := read()["provider"].(map[string]any); p["baseUrl"] != "https://oneapi.zaiwenai.com/v1" || p["model"] != "glm-4.5" || p["apiKey"] != "sk-zaiwen" {
		t.Fatalf("own over magpie: %v", p)
	}

	// a daemon that refuses keeps what it had: said
	daemon.mu.Lock()
	daemon.refuse = true
	daemon.mu.Unlock()
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if a.Notice() == "" {
		t.Fatal("no notice for a daemon on the old provider")
	}
}

// TestMiniqMadeFile: with no settings.json (miniQ on MINIQ_* variables or
// its default), magpie's is made private and goes again when magpie steps
// out; a daemon that can't take a provider out is told to restart.
func TestMiniqMadeFile(t *testing.T) {
	home, dir := miniqHome(t)
	path := filepath.Join(dir, "settings.json")
	a := miniq(home)
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v", st.Mode())
	}
	if f.Get() != "magpie/deepseek/flash" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("glm-4.6"); err == nil {
		t.Fatal("an own model with no provider of the user's to send it to")
	}
	newFakeMiniqDaemon(t, dir, map[string]any{"baseUrl": gatewayV1(), "model": "deepseek/flash"})
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(path)
		t.Fatalf("made file left: %s", b)
	}
	if a.Notice() == "" {
		t.Fatal("no notice for a daemon still on magpie")
	}
}

// TestMiniqDataDir: settings.json is where miniq-local's data_dir puts it.
func TestMiniqDataDir(t *testing.T) {
	home, def := miniqHome(t)
	if d := miniqDataDir(here(home)); d != def {
		t.Fatalf("default: %s", d)
	}
	app := filepath.Join(home, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", app)
	if d := miniqDataDir(here(home)); d != filepath.Join(app, "miniq") {
		t.Fatalf("LOCALAPPDATA: %s", d)
	}
	// moved in miniQ's Settings: the pointer beside the default
	moved := filepath.Join(home, "elsewhere", "miniq")
	b, _ := json.Marshal(map[string]string{"data_dir": moved})
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "miniq-data-dir.json"), b, 0o644)
	if d := miniqDataDir(here(home)); d != moved {
		t.Fatalf("moved: %s", d)
	}
	env := filepath.Join(home, "env-miniq")
	t.Setenv("MINIQ_DATA_DIR", env)
	if a := miniq(home); a.Path != filepath.Join(env, "settings.json") {
		t.Fatalf("MINIQ_DATA_DIR: %s", a.Path)
	}
}
