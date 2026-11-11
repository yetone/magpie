package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// magpie healthcheck, a container's HEALTHCHECK, passes only while a
// magpie gateway answers at MAGPIE_ADDR.
func TestHealthcheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	magpie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie","version":"dev"}`))
	}))
	defer magpie.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(magpie.URL, "http://"))
	if err := run([]string{"healthcheck"}); err != nil {
		t.Fatal("magpie answering:", err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"nginx"}`))
	}))
	defer other.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(other.URL, "http://"))
	if err := run([]string{"healthcheck"}); err == nil {
		t.Error("something else answering passed")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("MAGPIE_ADDR", addr)
	if err := run([]string{"healthcheck"}); err == nil {
		t.Error("nothing listening passed")
	}
}

// The Docker image sets HOME and the XDG folders inside its volume, which
// a volume an older image made doesn't have yet: magpie makes them.
func TestMakeDirs(t *testing.T) {
	vol := t.TempDir()
	for k, d := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
		t.Setenv(k, filepath.Join(vol, d))
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home", "config", "cache", "data", "state"} {
		if fi, err := os.Stat(filepath.Join(vol, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not made: %v", d, err)
		}
	}
}

// writeListening is a running magpie's note of where it listens
// (gateway.ListeningFile), for a command run in another shell.
func writeListening(t *testing.T, addr string, pid int) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := fmt.Sprintf(`{"addr":%q,"pid":%d,"serves":"web","version":"v9.9.9","started":"2026-11-11T00:00:00Z"}`, addr, pid)
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeServing answers as the magpie with pid, at a port that isn't Settings'.
func fakeServing(t *testing.T, pid int) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAGPIE_ADDR", "")
	m := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Write([]byte(`{"state":"restarting","version":"v9.9.9"}`))
			return
		}
		fmt.Fprintf(w, `{"name":"magpie","version":"v9.9.9","pid":%d,"serves":"web"}`, pid)
	}))
	t.Cleanup(m.Close)
	return strings.TrimPrefix(m.URL, "http://")
}

// ljlwlcz on X: a service started with MAGPIE_ADDR=0.0.0.0:10100, and
// `magpie healthcheck` run in a shell without it looked at 3425 and
// exited 1. The running magpie's own note says where it listens.
func TestHealthcheckFindsTheRunningGatewayWithoutMagpieAddr(t *testing.T) {
	addr := fakeServing(t, 4242)
	writeListening(t, addr, 4242)
	if err := run([]string{"healthcheck"}); err != nil {
		t.Fatal("a gateway that wrote down where it listens:", err)
	}
	// a note whose process isn't the one answering there (a stale file, a
	// port another program took) is not believed
	writeListening(t, addr, 777)
	if err := run([]string{"healthcheck"}); err == nil {
		t.Error("a note naming another pid than the one answering passed")
	}
	// --addr is the caller's word, over the note
	if err := run([]string{"healthcheck", "--addr", addr}); err != nil {
		t.Error("--addr:", err)
	}
	if err := run([]string{"healthcheck", "--addr=" + addr}); err != nil {
		t.Error("--addr=:", err)
	}
}

// A flag or a value healthcheck doesn't know is refused, not taken as an
// address (LESSONS: a command never takes a flag as data).
func TestHealthcheckRefusesWhatItDoesNotKnow(t *testing.T) {
	fakeServing(t, 1)
	for _, args := range [][]string{
		{"healthcheck", "--bogus"}, {"healthcheck", "x"}, {"healthcheck", "--addr"},
		{"healthcheck", "--addr", "nonsense"}, {"healthcheck", "--addr", "127.0.0.1:0"}, {"healthcheck", "--addr", "127.0.0.1:99999"},
	} {
		if err := run(args); err == nil || strings.Contains(err.Error(), "no gateway at") {
			t.Errorf("magpie %v: %v", args, err)
		}
	}
}

// `magpie update` asked the address it knew and said "magpie isn't running"
// while the service served on another one: its restart now goes where the
// running magpie says it listens.
func TestUpdateRestartsTheGatewayItFindsWithoutMagpieAddr(t *testing.T) {
	addr := fakeServing(t, 4242)
	writeListening(t, addr, 4242)
	if got := restartRunning(gateway.ClientURL(), "v9.9.10"); !strings.Contains(got, "restarts into v9.9.10") {
		t.Errorf("update found no running magpie: %q", got)
	}
	if got := restartRunning(gateway.URL(), "v9.9.10"); !strings.Contains(got, "isn't running") {
		t.Errorf("the address Settings has is not where the fake serves: %q", got)
	}
}

// magpie status says the role, pid and address of the magpie that serves
// (a watchdog can't tell magpie.exe tray from magpie.exe mcp image by name).
func TestStatusJSON(t *testing.T) {
	addr := fakeServing(t, 4242)
	writeListening(t, addr, 4242)
	out, err := runOut(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Running bool
		Role    string
		Pid     int
		Version string
		Addr    string
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if !st.Running || st.Role != "web" || st.Pid != 4242 || st.Version != "v9.9.9" || st.Addr != addr {
		t.Errorf("status: %+v (want %s)", st, addr)
	}
	// none running: said so, as JSON
	writeListening(t, "127.0.0.1:1", 4242)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
	out, err = runOut(t, "status", "--json")
	if err != nil || !strings.Contains(out, `"running": false`) {
		t.Errorf("nothing serving: %v %q", err, out)
	}
	if _, err := runOut(t, "status", "--nope"); err == nil {
		t.Error("an unknown flag was taken")
	}
}
