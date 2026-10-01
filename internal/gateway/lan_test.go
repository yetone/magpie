package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// Sharing gates remote access; each authorized remote call carries the same
// named identity as its local counterpart, so usage is attributed by key.
func TestLANGuard(t *testing.T) {
	fresh(t)
	var got, id string
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + "|" + r.Header.Get("x-api-key")
		id = access.Caller(r.Context()).KeyID
	}))
	call := func(from string, hdr ...string) int {
		got, id = "", ""
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	secret, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	keyID := keys[0].ID
	t.Setenv("MAGPIE_ADDR", "")
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusForbidden {
		t.Fatal("unshared remote", c)
	}
	if c := call("127.0.0.1:5000", "Authorization", "Bearer anything"); c != 200 {
		t.Fatal("loopback", c)
	}
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if addr := listenAddr(); addr != "0.0.0.0:"+Port() {
		t.Fatal(addr)
	}
	for _, hdr := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"x-api-key", "wrong"}} {
		if c := call("192.168.1.9:5000", hdr...); c != http.StatusUnauthorized {
			t.Fatal("unauthorized remote", hdr, c)
		}
	}
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != 200 || id != keyID || got != "Bearer magpie|magpie" {
		t.Fatal(c, id, got)
	}
	if c := call("127.0.0.1:5000", "Authorization", "Bearer "+secret); c != 200 || id != keyID {
		t.Fatal("local named key", c, id)
	}
	access.Update("off-key", access.Change{Key: keyID})
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusUnauthorized {
		t.Fatal("disabled key", c)
	}
	access.Update("on-key", access.Change{Key: keyID})
	access.Update("remove-key", access.Change{Key: keyID})
	if c := call("192.168.1.9:5000", "x-api-key", secret); c != http.StatusUnauthorized {
		t.Fatal("removed key", c)
	}
	s.LAN = false
	settings.Save(s)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	if c := call("192.168.1.9:5000"); c != 200 {
		t.Fatal("explicit open gateway", c)
	}
}

// A gateway on every interface is reached here on loopback.
func TestURLOfWildcard(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	if u := URL(); u != "http://127.0.0.1:3499" {
		t.Fatal(u)
	}
	t.Setenv("MAGPIE_ADDR", "")
	if u := URL(); u != "http://127.0.0.1:3425" {
		t.Fatal(u)
	}
}

// An agent whose User-Agent is only the AI SDK's (Alma) is known by
// the token it was given; the others still by their User-Agent.
func TestAgentOf(t *testing.T) {
	for _, c := range []struct{ auth, key, ua, want string }{
		{"Bearer " + TokenFor("alma"), "", "ai-sdk/openai/2.0.52 ai-sdk/provider-utils/3.0.12 runtime/node.js/v22", "alma"},
		{"", TokenFor("alma"), "ai-sdk/anthropic/2.0.1", "alma"},
		{"Bearer " + TokenFor("qoder"), "", "Bun/1.4.2", "qoder"},
		{"Bearer " + TokenFor("hanako"), "", "OpenAI/JS 6.0.0", "hanako"},
		{"Bearer " + Token, "", "claude-cli/2.1.0 (external, cli)", "claude-cli"},
		{"Bearer " + Token, "", "ai-sdk/openai/2.0.52", "ai-sdk"},
		{"Bearer " + Token + "-", "", "codex_cli_rs/0.40.0", "codex_cli_rs"},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if c.key != "" {
			r.Header.Set("x-api-key", c.key)
		}
		r.Header.Set("User-Agent", c.ua)
		if got := agentOf(r); got != c.want {
			t.Errorf("%q %q %q: %q, want %q", c.auth, c.key, c.ua, got, c.want)
		}
	}
}

// In a container magpie's own addresses are the container's (Docker's
// bridge, 172.17.x), which a NAS's other devices can't reach: set,
// MAGPIE_PUBLIC_URL is the address shared, and its host the one printed
// for the web page.
func TestPublicURLShared(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	for _, c := range []struct{ env, want, host string }{
		{"http://192.168.1.20:3425", "http://192.168.1.20:3425", "192.168.1.20"},
		{"https://nas.example.com/magpie/", "https://nas.example.com/magpie", "nas.example.com"},
		{"nas.local:13425", "http://nas.local:13425", "nas.local"},
		{"http://[fd00::20]:13425", "http://[fd00::20]:13425", "fd00::20"},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.env)
		if got := LANURLs(); len(got) != 1 || got[0] != c.want {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: %v, want %s", c.env, got, c.want)
		}
		if got := PublicHost(); got != c.host {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: host %q, want %s", c.env, got, c.host)
		}
		if ContainerAddrs() {
			t.Errorf("MAGPIE_PUBLIC_URL=%q still says the addresses are a container's", c.env)
		}
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if PublicHost() != "" {
		t.Error("unset, a host:", PublicHost())
	}
}

// A container is known by Docker's or Podman's marker file, or a runtime
// in PID 1's cgroup; a plain system has none of them.
func TestInContainer(t *testing.T) {
	mk := func(file, body string) string {
		root := t.TempDir()
		if file != "" {
			p := filepath.Join(root, file)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(body), 0o644)
		}
		return root
	}
	for _, c := range []struct {
		file, body string
		want       bool
	}{
		{"", "", false},
		{"proc/1/cgroup", "0::/init.scope\n", false},
		{".dockerenv", "", true},
		{"run/.containerenv", "", true},
		{"proc/1/cgroup", "12:pids:/docker/4f1c…\n", true},
		{"proc/1/cgroup", "0::/kubepods/besteffort/pod1\n", true},
	} {
		if got := inContainer(mk(c.file, c.body)); got != c.want {
			t.Errorf("%s %q: %v", c.file, c.body, got)
		}
	}
}
