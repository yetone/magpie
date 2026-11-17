package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/netproxy/sshtest"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func proxiesRig(t *testing.T) (call func(path, body string) (int, string), state func() settingsJSON) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".config"), 0o755)
	h := Handler(nil, nil)
	call = func(path, body string) (int, string) {
		rec := httptest.NewRecorder()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	state = func() settingsJSON {
		code, body := call("/api/settings", "")
		if code != 200 {
			t.Fatalf("settings %d %s", code, body)
		}
		var s settingsJSON
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	return call, state
}

// A saved proxy is added, edited (a blank password keeps the saved one),
// chosen by Settings and a provider, and refused deletion while chosen. The
// page is never sent the password, in a save's answer or in the state.
func TestSavedProxiesAPI(t *testing.T) {
	call, state := proxiesRig(t)
	const pass = "hunter2:/@secret"
	code, body := call("/api/proxies/save", `{"name":"Office","url":"127.0.0.1:7890","user":"me","pass":"`+pass+`"}`)
	if code != 200 || strings.Contains(body, "hunter2") {
		t.Fatalf("save: %d %s", code, body)
	}
	s := state()
	if len(s.SavedProxies) != 1 {
		t.Fatalf("state: %+v", s.SavedProxies)
	}
	px := s.SavedProxies[0]
	if px.Name != "Office" || px.URL != "127.0.0.1:7890" || px.User != "me" || !px.HasPass || !strings.HasPrefix(px.ID, "px") {
		t.Fatalf("saved: %+v", px)
	}
	if _, raw := call("/api/settings", ""); strings.Contains(raw, "hunter2") || strings.Contains(raw, "secret") {
		t.Fatalf("the password is in the state: %s", raw)
	}
	// an edit with no password sent keeps it
	if code, _ := call("/api/proxies/save", `{"id":"`+px.ID+`","name":"Office","url":"127.0.0.1:7891","user":"me"}`); code != 200 {
		t.Fatal(code)
	}
	if got := settings.Load().Proxies[0]; got.Pass != pass || got.URL != "127.0.0.1:7891" {
		t.Fatalf("after an edit: %+v", got)
	}
	// a name twice, and a login in the address, are refused
	if code, _ := call("/api/proxies/save", `{"name":"office","url":"127.0.0.1:1"}`); code != 400 {
		t.Fatalf("a second Office: %d", code)
	}
	if code, _ := call("/api/proxies/save", `{"name":"B","url":"http://u:p@127.0.0.1:1"}`); code != 400 {
		t.Fatalf("a login in the address: %d", code)
	}
	// chosen by Settings and by a provider: deletion is refused, naming both
	ref := settings.ProxyRef(px.ID)
	cur := settings.Load()
	cur.Proxy = ref
	if err := settings.Save(cur); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}, Proxy: ref}); err != nil {
		t.Fatal(err)
	}
	code, body = call("/api/proxies/delete", `{"id":"`+px.ID+`"}`)
	if code != 409 || !strings.Contains(body, "global") || !strings.Contains(body, "Relay") {
		t.Fatalf("delete in use: %d %s", code, body)
	}
	// a provider can't choose one that isn't there
	if err := provider.Save(provider.Provider{ID: "relay2", Name: "Relay2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}, Proxy: "@nope"}); err == nil {
		t.Fatal("a provider saved with a proxy that isn't there")
	}
	// let go of both: it goes
	cur = settings.Load()
	cur.Proxy = ""
	settings.Save(cur)
	provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}})
	if code, body := call("/api/proxies/delete", `{"id":"`+px.ID+`"}`); code != 200 || len(state().SavedProxies) != 0 {
		t.Fatalf("delete: %d %s", code, body)
	}
}

// The check goes through the proxy named, with its login, and says whether
// it was reached and how long it took; an unsaved one is checked as typed.
func TestSavedProxyTest(t *testing.T) {
	call, state := proxiesRig(t)
	client, proxyURL, asked := proxyCheckRig(t)
	old := proxyTestClient
	proxyTestClient = client
	t.Cleanup(func() { proxyTestClient = old })
	_, body := call("/api/proxies/test", `{"url":"`+proxyURL+`"}`)
	var out proxyTestJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil || !out.OK || out.IP != "203.0.113.9" || *asked != 1 {
		t.Fatalf("typed: %s (%v), asked %d", body, err, *asked)
	}
	call("/api/proxies/save", `{"name":"Up","url":"`+proxyURL+`"}`)
	call("/api/proxies/save", `{"name":"Down","url":"http://127.0.0.1:1"}`)
	for _, p := range state().SavedProxies {
		_, body := call("/api/proxies/test", `{"id":"`+p.ID+`"}`)
		var out proxyTestJSON
		json.Unmarshal([]byte(body), &out)
		if (p.Name == "Up") != out.OK {
			t.Fatalf("%s: %s", p.Name, body)
		}
	}
	for _, p := range state().SavedProxies {
		if p.Check == nil || (p.Name == "Up") != p.Check.OK {
			t.Fatalf("the last check of %s isn't kept: %+v", p.Name, p.Check)
		}
	}
}

// An SSH server is saved from its host, user and password: the save logs
// in first, so a refused login is said there and nothing is kept, and a
// kept one shows the host key it trusts. Its test goes through the
// server; the page is never sent the password.
func TestSavedSSHProxyAPI(t *testing.T) {
	call, state := proxiesRig(t)
	client, _, _ := proxyCheckRig(t)
	old := proxyTestClient
	proxyTestClient = client
	t.Cleanup(func() { proxyTestClient = old })
	srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "s3cret", Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		fmt.Fprint(rw, "fl=1\nip=192.0.2.44\nloc=JP\n")
	})})

	code, body := call("/api/proxies/save", `{"name":"VPS","url":"`+srv.URL+`","user":"root","pass":"wrong","key":""}`)
	var refused map[string]string
	json.Unmarshal([]byte(body), &refused)
	if code != 400 || refused["code"] != "proxy_why" || !strings.Contains(refused["why"], "turned the login away") || refused["addr"] != srv.Addr {
		t.Fatalf("a refused login: %d %s", code, body)
	}
	if len(state().SavedProxies) != 0 {
		t.Fatal("a proxy whose login was refused was kept")
	}

	code, body = call("/api/proxies/save", `{"name":"VPS","url":"`+srv.URL+`","user":"root","pass":"s3cret","key":""}`)
	if code != 200 || strings.Contains(body, "s3cret") {
		t.Fatalf("save: %d %s", code, body)
	}
	ps := state().SavedProxies
	if len(ps) != 1 || !strings.HasPrefix(ps[0].HostKey, "SHA256:") || !ps[0].HasPass {
		t.Fatalf("state: %+v", ps)
	}
	t.Cleanup(func() { netproxy.CloseSSH(ps[0].ID) })
	_, body = call("/api/proxies/test", `{"id":"`+ps[0].ID+`"}`)
	var out proxyTestJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil || !out.OK || out.IP != "192.0.2.44" {
		t.Fatalf("test through the SSH server: %s", body)
	}
	if got := srv.Targets(); len(got) == 0 || got[len(got)-1] != "trace.example.invalid:80" {
		t.Fatalf("the server was asked for %v", got)
	}

	// typed where an HTTP proxy goes, the same server says what it is
	_, body = call("/api/proxies/test", `{"url":"http://`+srv.Addr+`"}`)
	out = proxyTestJSON{}
	json.Unmarshal([]byte(body), &out)
	if out.OK || !strings.Contains(out.Why, "is an SSH server") || out.Addr != srv.Addr {
		t.Fatalf("an SSH server as an HTTP proxy: %s", body)
	}
}

// A server that lets magpie log in but won't forward says so about the
// server, not about the tunnel's local port: OpenSSH with
// AllowTcpForwarding no refuses the channel, and macOS 27's sshd hangs up
// the whole connection.
func TestSavedSSHProxyThatWontForward(t *testing.T) {
	call, state := proxiesRig(t)
	client, _, _ := proxyCheckRig(t)
	old := proxyTestClient
	proxyTestClient = client
	t.Cleanup(func() { proxyTestClient = old })
	for _, how := range []string{"prohibit", "hangup"} {
		srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw", Forward: how})
		if code, body := call("/api/proxies/save", `{"name":"`+how+`","url":"`+srv.URL+`","user":"root","pass":"pw"}`); code != 200 {
			t.Fatalf("%s: save %d %s", how, code, body)
		}
		var id string
		for _, p := range state().SavedProxies {
			if p.Name == how {
				id = p.ID
			}
		}
		t.Cleanup(func() { netproxy.CloseSSH(id) })
		_, body := call("/api/proxies/test", `{"id":"`+id+`"}`)
		var out proxyTestJSON
		json.Unmarshal([]byte(body), &out)
		if out.OK || !strings.Contains(out.Why, "won't forward") || out.Addr != srv.Addr {
			t.Errorf("%s: %s", how, body)
		}
		// the same before it is saved
		_, body = call("/api/proxies/test", `{"url":"`+srv.URL+`","user":"root","pass":"pw"}`)
		out = proxyTestJSON{}
		json.Unmarshal([]byte(body), &out)
		if out.OK || !strings.Contains(out.Why, "won't forward") || out.Addr != srv.Addr {
			t.Errorf("%s, unsaved: %s", how, body)
		}
	}
}
