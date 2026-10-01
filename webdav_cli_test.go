package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/provider"
)

// webdavHome is a machine of its own: no agent on it, no gateway at the
// address, and nothing on stdin.
func webdavHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PATH", "")
	for _, v := range []string{"DSH_HOME", "PI_CODING_AGENT_DIR", "COPILOT_HOME", "CLINE_DIR", "GROK_HOME", "HERMES_HOME", "HANA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, "")
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
	piped(t)
}

// davServer is a WebDAV server for user me with a password of its own,
// keeping files in memory, and every password it was sent.
type davServer struct {
	mu    sync.Mutex
	pass  string
	files map[string][]byte
	sent  []string
	url   string
}

func newDAVServer(t *testing.T, pass string) *davServer {
	t.Helper()
	d := &davServer{pass: pass, files: map[string][]byte{}}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	d.url = srv.URL + "/dav/"
	return d
}

func (d *davServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	u, p, ok := r.BasicAuth()
	if ok {
		d.sent = append(d.sent, p)
	}
	if !ok || u != "me" || p != d.pass {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		b, ok := d.files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(b)
	case http.MethodPut:
		d.files[r.URL.Path], _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (d *davServer) file() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.files["/dav/magpie/magpie.magpie-backup"]
}

func (d *davServer) passwords() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.sent)
}

// piped is what stdin holds for the secrets asked, a line each: a pipe, so
// they are read as a script's are, never from the terminal the test is in.
func piped(t *testing.T, lines ...string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) > 0 {
		w.WriteString(strings.Join(lines, "\n") + "\n")
	}
	w.Close()
	oldFile, oldReader := os.Stdin, stdin
	os.Stdin, stdin = r, bufio.NewReader(r)
	t.Cleanup(func() {
		os.Stdin, stdin = oldFile, oldReader
		r.Close()
	})
}

func TestWebdavCmd(t *testing.T) {
	webdavHome(t)
	first := newDAVServer(t, "pw")
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})

	// off: nothing to change or sync
	if err := webdavCmd([]string{"set", "agents=no"}); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatalf("set while off: %v", err)
	}
	if err := webdavCmd([]string{"now"}); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatalf("now while off: %v", err)
	}
	if err := webdavCmd(nil); err != nil {
		t.Fatal(err)
	}

	// a wrong address is said before any secret is asked for
	if err := webdavCmd([]string{"on", "dav.example.com", "user=me"}); err == nil || !strings.Contains(err.Error(), "not a WebDAV address") {
		t.Fatalf("an address with no https://: %v", err)
	}

	// on: the password and the passphrase asked for, and a sync at once
	piped(t, "pw", "correct horse")
	if err := webdavCmd([]string{"on", first.url, "user=me", "library=no"}); err != nil {
		t.Fatal(err)
	}
	c, ok := davsync.Load()
	if !ok || c.URL != first.url || c.User != "me" || c.Password != "pw" || c.Passphrase != "correct horse" ||
		!c.Keys || !c.Agents || c.Library == nil || *c.Library {
		t.Fatalf("on: %v %+v", ok, c)
	}
	if up := first.file(); len(up) == 0 || strings.Contains(string(up), "deepseek") {
		t.Fatalf("on the server: %q", up)
	}
	if v := davsync.Status(); v.Error != "" || v.Last.IsZero() {
		t.Fatalf("status: %+v", v)
	}

	// set: what is given changes, the rest (secrets too) stays
	if err := webdavCmd([]string{"set", "agents=no", "keys=no"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.Agents || c.Keys || c.Password != "pw" || c.Passphrase != "correct horse" || c.URL != first.url {
		t.Fatalf("set: %+v", c)
	}
	// an address with = in it, bare: the same server, so the password stays
	if err := webdavCmd([]string{"set", first.url + "?k=v"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.URL != first.url+"?k=v" || c.Password != "pw" {
		t.Fatalf("an address with = in it: %+v", c)
	}
	for _, bad := range [][]string{{"set", "passphrase=on-the-command-line"}, {"set", "password=on-the-command-line"}, {"set", "keys=maybe"}, {"set", "colour=red"}, {"set", first.url, first.url}} {
		if err := webdavCmd(bad); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}

	// password= asks for a new one; a wrong one shows at once, and it stays
	// on to be put right
	piped(t, "nope")
	if err := webdavCmd([]string{"set", "password="}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong password: %v", err)
	}
	if c, ok := davsync.Load(); !ok || c.Password != "nope" {
		t.Fatalf("after a failed sync: %v %+v", ok, c)
	}
	piped(t, "pw")
	if err := webdavCmd([]string{"set", "password="}); err != nil {
		t.Fatal(err)
	}

	// another server: the password saved is not sent there, and one is
	// asked for
	second := newDAVServer(t, "pw2")
	piped(t, "pw2")
	if err := webdavCmd([]string{"set", second.url}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(second.passwords(), "pw") {
		t.Fatal("the first server's password went to the second")
	}
	if c, _ := davsync.Load(); c.URL != second.url || c.Password != "pw2" || c.Passphrase != "correct horse" {
		t.Fatalf("moved: %+v", c)
	}

	// user= alone: no sign-in, nothing asked for, no password sent
	sent := len(second.passwords())
	if err := webdavCmd([]string{"set", "user="}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("no sign-in, on a server that asks for one: %v", err)
	}
	if c, _ := davsync.Load(); c.User != "" || c.Password != "" {
		t.Fatalf("user= alone: %+v", c)
	}
	if n := len(second.passwords()); n != sent {
		t.Fatalf("%d passwords sent with no user", n-sent)
	}

	if err := webdavCmd([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := davsync.Load(); ok {
		t.Fatal("still on after off")
	}
	if len(second.file()) == 0 {
		t.Fatal("off took the file off the server")
	}
}
