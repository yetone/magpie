package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// asks counts what the updater set here was asked.
type asks struct {
	mu   sync.Mutex
	want []string
	out  provider.RemoteUpdate
}

func (a *asks) updater(want string) provider.RemoteUpdate {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.want = append(a.want, want)
	return a.out
}

func (a *asks) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.want...)
}

func setUpdater(t *testing.T, out provider.RemoteUpdate) *asks {
	t.Helper()
	a := &asks{out: out}
	SetUpdater(a.updater)
	t.Cleanup(func() { SetUpdater(nil) })
	return a
}

// nnlrl_75403 on Discord: once magpie here updates, the Remote magpie it
// uses updates too. POST /v1/magpie/update replaces the program there, so
// only the owner asks it: a gateway key of full access, from this computer
// or another while magpie is shared. Never a request with no key (a page
// or app the gateway lets in on loopback without one), a page with the
// key in it, a read-only key or a held one.
func TestRemoteUpdateNeedsAFullKey(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	keys, secrets := newCaller(t, "Full", "Read only", "Held")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("limit-key", access.Change{Key: keys[2].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	a := setUpdater(t, provider.RemoteUpdate{State: "updating"})
	h := lanGuard(New().Handler())
	path := provider.RemoteUpdatePath + "?version=v0.1.9999"
	do := func(from, host, origin, secret string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = from
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.2 Safari/605.1.15")
		}
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const loop, lan, gw = "127.0.0.1:50123", "192.168.1.9:5000", "127.0.0.1:3425"
	for _, c := range []struct {
		why                        string
		from, host, origin, secret string
	}{
		{"this computer, no key", loop, gw, "", ""},
		{"a sandboxed frame (Origin null), no key", loop, gw, "null", ""},
		{"an app's scheme, no key", loop, gw, "tauri://localhost", ""},
		{"a *.localhost page, no key", loop, gw, "http://evil.localhost:8080", ""},
		{"a rebound page (its own Host), no key", loop, "evil.example:3425", "", ""},
		{"a foreign page, no key", loop, gw, "https://evil.example", ""},
		{"a foreign page with the full key", loop, gw, "https://evil.example", secrets[0]},
		{"another machine, no key", lan, gw, "", ""},
		{"another machine, a wrong key", lan, gw, "", "sk-magpie-wrong"},
		{"a read-only key", lan, gw, "", secrets[1]},
		{"a read-only key, this computer", loop, gw, "", secrets[1]},
		{"a held key", lan, gw, "", secrets[2]},
	} {
		if code, body := do(c.from, c.host, c.origin, c.secret); code < 400 || code == http.StatusNotFound || strings.Contains(body, `"updating"`) {
			t.Errorf("%s: %d %s", c.why, code, body)
		}
		if got := a.seen(); len(got) != 0 {
			t.Fatalf("%s asked the updater: %v", c.why, got)
		}
	}
	if code, body := do(lan, gw, "", secrets[1]); code != http.StatusForbidden || !strings.Contains(body, ReadOnlyCode) {
		t.Fatalf("read-only key: %d %s", code, body)
	}
	if code, body := do(lan, gw, "", secrets[2]); code != http.StatusForbidden || !strings.Contains(body, provider.KeyHeldCode) {
		t.Fatalf("held key: %d %s", code, body)
	}
	for _, from := range []string{lan, loop} {
		code, body := do(from, gw, "", secrets[0])
		var out provider.RemoteUpdate
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.State != "updating" || out.Version != Version {
			t.Fatalf("full key from %s: %d %s", from, code, body)
		}
	}
	if got := a.seen(); len(got) != 2 || got[0] != "v0.1.9999" || got[1] != "v0.1.9999" {
		t.Fatalf("asked %v", got)
	}

	// not shared on the network: another machine isn't let in, key or not
	s := settings.Load()
	s.LAN = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if code, body := do(lan, gw, "", secrets[0]); code < 400 || code == http.StatusNotFound {
		t.Fatalf("not shared, another machine: %d %s", code, body)
	}
	if got := a.seen(); len(got) != 2 {
		t.Fatalf("not shared, asked %v", got)
	}

	// nothing to update with (a test binary, a tool): said, not done
	SetUpdater(nil)
	code, body := do(loop, gw, "", secrets[0])
	var out provider.RemoteUpdate
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.State != "unsupported" {
		t.Fatalf("no updater: %d %s", code, body)
	}
}

// The office's magpie updated; the home server's, its Remote magpie, is
// asked to follow through the provider's key, once a version, and only
// when it is behind. The row keeps what it answered.
func TestRemoteMagpieAskedToFollow(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetRemoteUpdatesForTest()
	t.Cleanup(provider.ForgetRemoteUpdatesForTest)
	was := Version
	t.Cleanup(func() { Version = was })
	Version = "v0.1.100" // the remote's
	keys, secrets := newCaller(t, "Office", "Viewer")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	a := setUpdater(t, provider.RemoteUpdate{State: "updating"})
	srv := httptest.NewServer(lanGuard(New().Handler()))
	defer srv.Close()
	ctx := context.Background()
	save := func(secret, at string) {
		t.Helper()
		if err := provider.Save(provider.Provider{ID: "home", Name: "Home", Preset: provider.RemoteMagpiePreset, Key: secret, Chat: at}); err != nil {
			t.Fatal(err)
		}
		provider.ForgetRemoteUpdatesForTest()
	}
	t.Cleanup(func() { provider.Delete("home") })

	// a build here that isn't a release asks nothing
	save(secrets[0], srv.URL)
	provider.UpdateRemotes(ctx, "dev")
	if got := a.seen(); len(got) != 0 {
		t.Fatalf("a dev build asked %v", got)
	}
	// the remote on the same version, or a newer one: not asked
	for _, v := range []string{"v0.1.100", "v0.1.99"} {
		provider.UpdateRemotes(ctx, v)
		if got := a.seen(); len(got) != 0 {
			t.Fatalf("%s asked %v", v, got)
		}
		if u, ok := provider.RemoteUpdateOf("home", time.Now()); ok {
			t.Fatalf("%s: the row says %+v", v, u)
		}
	}
	// automatic updates off here: the user keeps this magpie where it
	// is, and its remotes with it
	s := settings.Load()
	s.NoAutoUpdate = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	provider.UpdateRemotes(ctx, "v0.1.200")
	if got := a.seen(); len(got) != 0 {
		t.Fatalf("updates off here, asked %v", got)
	}
	s.NoAutoUpdate = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	// behind: asked once with this version, not again for it
	provider.UpdateRemotes(ctx, "v0.1.200")
	provider.UpdateRemotes(ctx, "v0.1.200")
	if got := a.seen(); len(got) != 1 || got[0] != "v0.1.200" {
		t.Fatalf("asked %v, want v0.1.200 once", got)
	}
	if u, ok := provider.RemoteUpdateOf("home", time.Now()); !ok || u.State != "updating" || u.Version != "v0.1.200" {
		t.Fatalf("the row: %+v %v", u, ok)
	}

	// it can't update itself: the row says why
	a.mu.Lock()
	a.out = provider.RemoteUpdate{State: "unsupported", Why: "container"}
	a.mu.Unlock()
	provider.ForgetRemoteUpdatesForTest()
	provider.UpdateRemotes(ctx, "v0.1.200")
	if u, ok := provider.RemoteUpdateOf("home", time.Now()); !ok || u.State != "unsupported" || u.Why != "container" {
		t.Fatalf("a container: %+v %v", u, ok)
	}

	// a read-only key there: refused, the updater never asked
	save(secrets[1], srv.URL)
	n := len(a.seen())
	provider.UpdateRemotes(ctx, "v0.1.200")
	if u, ok := provider.RemoteUpdateOf("home", time.Now()); !ok || u.State != "refused" {
		t.Fatalf("read-only key: %+v %v", u, ok)
	}
	if got := a.seen(); len(got) != n {
		t.Fatalf("read-only key asked %v", got)
	}

	// a magpie from before the route: too old to be asked
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1" {
			writeJSON(w, 200, map[string]any{"name": "magpie", "version": "v0.1.50"})
			return
		}
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions")
	}))
	defer old.Close()
	save(secrets[0], old.URL)
	provider.UpdateRemotes(ctx, "v0.1.200")
	if u, ok := provider.RemoteUpdateOf("home", time.Now()); !ok || u.State != "old" {
		t.Fatalf("an old magpie: %+v %v", u, ok)
	}
}
