package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// fakeAvibe is Avibe's internal Model Hub API (core/handlers/model_hub/
// rpc.py) as magpie uses it, over the control socket of the home it serves
// (on Windows, the loopback port and bearer token of its endpoint file),
// keeping its sources in config/config.json as Avibe does. A source in a
// route chain is removed or refreshed only when asked again with what
// that takes away, as Avibe's own page confirms it.
type fakeAvibe struct {
	dir string

	mu      sync.Mutex
	ops     []string
	created []map[string]any
	refresh int
	next    int
}

func serveFakeAvibe(t *testing.T, dir string) *fakeAvibe {
	t.Helper()
	f := &fakeAvibe{dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.write(nil)
	var ln net.Listener
	var err error
	token := ""
	if runtime.GOOS == "windows" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		token = "tok-" + t.Name()
		desc := filepath.Join(dir, "runtime", "control-ipc", "endpoint.json")
		os.MkdirAll(filepath.Dir(desc), 0o700)
		b, _ := json.Marshal(map[string]any{"schema_version": 1, "transport": "tcp", "host": "127.0.0.1", "port": ln.Addr().(*net.TCPAddr).Port, "instance_id": "x", "bearer_token": token})
		os.WriteFile(desc, b, 0o600)
	} else {
		sock := filepath.Join(dir, "state", "dispatch.sock")
		os.MkdirAll(filepath.Dir(sock), 0o700)
		ln, err = net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
	}
	srv := &http.Server{Handler: f.handler(token)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("AVIBE_HOME", dir)
	was := avibeLive
	avibeLive = avibeConnect
	t.Cleanup(func() { avibeLive = was })
	return f
}

// handler is the API, asking for token as Windows' endpoint does.
func (f *fakeAvibe) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/model-hub" || (token != "" && r.Header.Get("Authorization") != "Bearer "+token) {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		var req struct {
			Operation string         `json:"operation"`
			Payload   map[string]any `json:"payload"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res, code, detail := f.handle(req.Operation, req.Payload)
		w.Header().Set("Content-Type", "application/json")
		if code != "" {
			w.WriteHeader(http.StatusConflict)
			out := map[string]any{"ok": false, "error": code, "detail": "modelHub.errors." + code}
			for k, v := range detail {
				out[k] = v
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
	})
}

// runningAvibes are the Avibes every test but avibe_test.go's own finds
// running (main_test.go), one a home, served in the process: an Avibe takes
// magpie only while it runs, so without one the tests that connect every
// agent (a gateway moved, a LAN or proxy address kept) would leave it out.
var runningAvibes = struct {
	sync.Mutex
	at map[string]*fakeAvibe
}{at: map[string]*fakeAvibe{}}

func runningAvibe(dir string) *avibeClient {
	runningAvibes.Lock()
	f := runningAvibes.at[dir]
	if f == nil {
		f = &fakeAvibe{dir: dir}
		runningAvibes.at[dir] = f
	}
	runningAvibes.Unlock()
	return &avibeClient{http: &http.Client{Transport: handlerTransport{f.handler("")}}, base: "http://localhost"}
}

type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

func (f *fakeAvibe) sources() []map[string]any {
	raw, _ := edit.GetJSON(filepath.Join(f.dir, "config", "config.json"), "model_hub.sources")
	var out []map[string]any
	json.Unmarshal([]byte(raw), &out)
	return out
}

func (f *fakeAvibe) write(srcs []map[string]any) {
	os.MkdirAll(filepath.Join(f.dir, "config"), 0o700)
	if srcs == nil {
		srcs = []map[string]any{}
	}
	b, _ := json.MarshalIndent(map[string]any{"model_hub": map[string]any{"sources": srcs}, "language": "zh"}, "", "  ")
	os.WriteFile(filepath.Join(f.dir, "config", "config.json"), b, 0o600)
}

// confirms says the payload repeats what removing the source takes away.
func confirms(p map[string]any) bool {
	hops, _ := p["would_remove_hops"].([]any)
	return p["force"] == true && len(hops) == 1 && fmt.Sprint(hops[0]) == "vibey:0"
}

func (f *fakeAvibe) handle(op string, p map[string]any) (any, string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	srcs := f.sources()
	find := func() int {
		return slices.IndexFunc(srcs, func(s map[string]any) bool { return s["id"] == p["source_id"] })
	}
	inChain := map[string]any{"would_remove_hops": []string{"vibey:0"}, "would_interrupt": []string{}}
	switch op {
	case "list_sources":
		return map[string]any{"sources": srcs}, "", nil
	case "create_source":
		s, _ := p["source"].(map[string]any)
		f.created = append(f.created, s)
		f.next++
		id := fmt.Sprintf("src_%d", f.next)
		saved := map[string]any{"id": id, "kind": s["kind"], "vendor": s["vendor"], "display_name": s["display_name"], "base_url": s["base_url"], "protocol": s["protocol"]}
		f.write(append(srcs, saved))
		return map[string]any{"source": saved}, "", nil
	case "delete_source":
		i := find()
		if i < 0 {
			return nil, "source_not_found", nil
		}
		if !confirms(p) {
			return nil, "source_in_route_chain", inChain
		}
		f.write(slices.Delete(srcs, i, i+1))
		return map[string]any{"deleted": true}, "", nil
	case "refresh_source":
		if find() < 0 {
			return nil, "source_not_found", nil
		}
		if !confirms(p) {
			return nil, "source_in_route_chain", inChain
		}
		f.refresh++
		return map[string]any{}, "", nil
	}
	return nil, "unknown_operation", nil
}

// avibeTestHome is a home for Avibe short enough for a unix socket's path
// (104 bytes on macOS, which t.TempDir's names can pass).
func avibeTestHome(t *testing.T) string {
	t.Helper()
	syncHome(t)
	d, err := os.MkdirTemp("", "avb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, ".avibe")
}

// Connecting Avibe adds magpie as a model source through Avibe's own API,
// as its Models page does: an API-key source of the custom vendor at the
// gateway's /v1, spoken to as chat completions (a Responses source would
// reach the gateway as Codex) with Avibe's own key, saved without the probe
// that can't tell the gateway's protocols apart. Disconnecting removes it,
// confirming the routes it leaves, as Avibe asks (alex_metacraft on X).
func TestAvibeAddsMagpieAsAModelSource(t *testing.T) {
	dir := avibeTestHome(t)
	f := serveFakeAvibe(t, dir)
	a := avibe(os.Getenv("HOME"))
	if a.Dir != dir || !a.Detected() {
		t.Fatalf("Avibe at %s not found (%s)", dir, a.Dir)
	}
	if a.Wired() {
		t.Fatal("wired before it was connected")
	}
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %v", f.created)
	}
	s := f.created[0]
	for k, v := range map[string]any{"kind": "api_key", "vendor": "custom", "display_name": "magpie", "base_url": gatewayV1(), "key": agentKey("avibe"), "protocol": "openai_chat", "save_unverified": true} {
		if s[k] != v {
			t.Errorf("source %s = %v, want %v", k, s[k], v)
		}
	}
	if got := a.Field("provider").Get(); got != magpieID || !a.Wired() {
		t.Errorf("connected source not read back: %q", got)
	}
	if msg := a.Check(); msg != "" {
		t.Errorf("Check: %s", msg)
	}
	// connecting again adds no second source
	if err := a.Field("provider").Set(magpieID); err != nil || len(f.created) != 1 {
		t.Errorf("connected twice: %v %d", err, len(f.created))
	}
	if err := a.Sync(); err != nil || f.refresh != 1 {
		t.Errorf("Sync didn't list the gateway's models again: %v, %d refreshes", err, f.refresh)
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if n := len(f.sources()); n != 0 || a.Wired() {
		t.Errorf("source still there after disconnect: %v", f.sources())
	}
	if avibeStashed(dir) != "" {
		t.Error("source id still stashed")
	}
}

// A source the user renamed in Avibe is still magpie's (its id was
// stashed); one they added by hand at the gateway is too; a source of
// their own elsewhere is never touched.
func TestAvibeKnowsItsSource(t *testing.T) {
	dir := avibeTestHome(t)
	f := serveFakeAvibe(t, dir)
	mine := map[string]any{"id": "src_mine", "vendor": "openai", "display_name": "OpenAI", "base_url": "https://api.openai.com/v1"}
	f.write([]map[string]any{mine, {"id": "src_hand", "vendor": "custom", "display_name": "gw", "base_url": gatewayV1() + "/"}})
	a := avibe(os.Getenv("HOME"))
	if !a.Wired() {
		t.Fatal("a source at the gateway added by hand isn't read as connected")
	}
	// it is magpie's from then on, wherever the gateway goes
	if err := a.Field("provider").Set(magpieID); err != nil || len(f.created) != 0 || avibeStashed(dir) != "src_hand" {
		t.Errorf("connect over one added by hand: %v, %d made, stash %q", err, len(f.created), avibeStashed(dir))
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := f.sources(); len(got) != 1 || got[0]["id"] != "src_mine" {
		t.Fatalf("after disconnect: %v", got)
	}
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	srcs := f.sources()
	srcs[1]["display_name"] = "my gateway"
	f.write(srcs)
	if !a.Wired() {
		t.Error("renamed source not read as magpie's")
	}
}

// The gateway on another address: the key goes with the address, so the
// source is made again there rather than left pointing at the old one.
func TestAvibeFollowsTheGateway(t *testing.T) {
	dir := avibeTestHome(t)
	f := serveFakeAvibe(t, dir)
	a := avibe(os.Getenv("HOME"))
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3426")
	if msg := a.Check(); !strings.Contains(msg, "base_url") {
		t.Errorf("Check on a moved gateway: %q", msg)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	srcs := f.sources()
	if len(srcs) != 1 || srcs[0]["base_url"] != "http://127.0.0.1:3426/v1" || len(f.created) != 2 {
		t.Fatalf("after the move: %v (%d made)", srcs, len(f.created))
	}
	if avibeStashed(dir) != srcs[0]["id"] {
		t.Errorf("stash %q, source %v", avibeStashed(dir), srcs[0]["id"])
	}
}

// Avibe changes its sources only while it runs; the switch says to start
// it rather than writing a file Avibe would refuse.
func TestAvibeNeedsItRunning(t *testing.T) {
	dir := avibeTestHome(t)
	os.MkdirAll(filepath.Join(dir, "config"), 0o700)
	t.Setenv("AVIBE_HOME", dir)
	was := avibeLive
	avibeLive = avibeConnect
	t.Cleanup(func() { avibeLive = was })
	a := avibe(os.Getenv("HOME"))
	if err := a.Field("provider").Set(magpieID); !errors.Is(err, errAvibeDown) {
		t.Errorf("connect with Avibe stopped: %v", err)
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Errorf("disconnect with nothing connected: %v", err)
	}
	if err := a.Sync(); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "config.json")); err == nil {
		t.Error("config.json written while Avibe is stopped")
	}
}

// Avibe's home is $AVIBE_HOME (~ expanded), else ~/.avibe, else the
// ~/.vibe_remote an older Avibe made (config/paths.py).
func TestAvibeHome(t *testing.T) {
	home := syncHome(t)
	if d := avibeHome(home); d != filepath.Join(home, ".avibe") {
		t.Errorf("default: %s", d)
	}
	os.MkdirAll(filepath.Join(home, ".vibe_remote"), 0o700)
	if d := avibeHome(home); d != filepath.Join(home, ".vibe_remote") {
		t.Errorf("legacy: %s", d)
	}
	os.MkdirAll(filepath.Join(home, ".avibe"), 0o700)
	if d := avibeHome(home); d != filepath.Join(home, ".avibe") {
		t.Errorf("both: %s", d)
	}
	t.Setenv("AVIBE_HOME", "~/elsewhere")
	if d := avibeHome(home); d != filepath.Join(home, "elsewhere") {
		t.Errorf("AVIBE_HOME: %s", d)
	}
}
