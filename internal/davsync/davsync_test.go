package davsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// fakeDAV is a WebDAV server with one user, keeping files in memory.
type fakeDAV struct {
	mu    sync.Mutex
	files map[string][]byte
	etags map[string]string
	dirs  map[string]bool
	n     int
	puts  int
	// nutstore: a file read in a folder that isn't there is a 409, as
	// 坚果云 (Nutstore) answers, not a 404
	nutstore bool
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		b, ok := f.files[r.URL.Path]
		if !ok && f.nutstore && !f.dirs[urlDir(r.URL.Path)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", f.etags[r.URL.Path])
		w.Write(b)
	case http.MethodPut:
		if !f.dirs[urlDir(r.URL.Path)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if m := r.Header.Get("If-Match"); m != "" && m != f.etags[r.URL.Path] {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.n++
		f.puts++
		f.files[r.URL.Path], f.etags[r.URL.Path] = b, fmt.Sprintf(`"%d"`, f.n)
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		p := strings.TrimSuffix(r.URL.Path, "/")
		if f.dirs[p] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.dirs[p] = true
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// urlDir is the folder a URL path is in: a URL's, with / on every system
func urlDir(p string) string { return p[:max(strings.LastIndex(p, "/"), 1)] }

// computer is one machine's magpie: its own home, which the test moves
// between.
type computer string

func newComputer(t *testing.T) computer { return computer(t.TempDir()) }

func (c computer) use(t *testing.T) {
	t.Setenv("HOME", string(c))
	t.Setenv("USERPROFILE", string(c))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(string(c), ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(string(c), ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(string(c), ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(string(c), ".codex"))
	t.Setenv("PATH", "")
	for _, v := range []string{"DSH_HOME", "PI_CODING_AGENT_DIR", "OMO_CODING_AGENT_DIR", "SENPI_CODING_AGENT_DIR", "COPILOT_HOME", "CLINE_DIR", "GROK_HOME", "HERMES_HOME", "HANA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, "")
	}
}

func ids() []string {
	var out []string
	ps, _ := provider.Stored()
	for _, p := range ps {
		out = append(out, p.ID+"="+p.Key)
	}
	slices.Sort(out)
	return out
}

func TestSync(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	now := func(t *testing.T) View {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		return Status()
	}

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	settings.Save(settings.Settings{Theme: "dark", Proxy: "http://127.0.0.1:7890", Window: []int{900, 700}, TrayUsages: []string{"claude|a@b.c"}, TrayUsageEvery: 5, TrayNoLogos: true})
	profile.Save("work", profile.Profile{Fields: map[string]string{"claude.model": "x"}})
	same := cfg
	same.Passphrase = cfg.Password
	if err := Configure(same); err == nil || !strings.Contains(err.Error(), "of its own") {
		t.Fatalf("a passphrase that is the password: %v", err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path("sync.json")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Fatalf("sync.json is %v", fi.Mode())
	}
	// the first: its setup goes up, sealed
	if v := now(t); v.Error != "" || v.Last.IsZero() || v.Notice != nil {
		t.Fatalf("first: %+v", v)
	}
	up := fake.files["/dav/magpie/magpie.magpie-backup"]
	if len(up) == 0 || strings.Contains(string(up), "deepseek") || strings.Contains(string(up), `"providers"`) {
		t.Fatalf("on the server: %q", up)
	}
	// nothing changed: nothing written
	now(t)
	if fake.puts != 1 {
		t.Fatalf("%d puts", fake.puts)
	}

	// b joins: a's setup comes in, b's own proxy and window stay, and what
	// b had is kept aside
	b.use(t)
	provider.Save(provider.Provider{ID: "mine", Name: "Mine", Chat: "https://x/v1", Key: "kb"})
	settings.Save(settings.Settings{Proxy: "direct", Window: []int{1, 2}})
	Configure(Config{URL: cfg.URL, User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true})
	v := now(t)
	if v.Notice == nil || !slices.Contains(v.Notice.Here, "providers") || !slices.Contains(v.Notice.Here, "settings") {
		t.Fatalf("joined: %+v", v)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("b's providers: %v", got)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Proxy != "direct" || !slices.Equal(s.Window, []int{1, 2}) {
		t.Fatalf("b's settings: %+v", s)
	}
	// nor does a's menu bar: what shows beside the icon is b's own (yoooo
	// on Discord)
	if s := settings.Load(); len(s.TrayUsages) != 0 || s.TrayUsage != "" || s.TrayUsageEvery == 5 || s.TrayNoLogos {
		t.Fatalf("b's menu bar: %+v", s)
	}
	if ps, _ := profile.Load(); len(ps) != 1 {
		t.Fatalf("b's profiles: %v", ps)
	}
	kept, _ := filepath.Glob(filepath.Join(settings.Dir(), "sync", "*-this-computer"+backup.Ext))
	if len(kept) != 1 {
		t.Fatalf("kept: %v", kept)
	}
	if old, err := backup.Open(must(os.ReadFile(kept[0])), "correct horse"); err != nil || old.Providers[0].ID != "mine" {
		t.Fatalf("kept copy: %v %+v", err, old.Providers)
	}
	Dismiss()
	puts := fake.puts
	now(t)
	if fake.puts != puts || Status().Notice != nil {
		t.Fatalf("b pushed back what it brought in (%d puts)", fake.puts-puts)
	}

	// a adds one, b removes one: each reaches the other
	a.use(t)
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
	now(t)
	b.use(t)
	now(t)
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "kimi=k2"}) {
		t.Fatalf("b after a added: %v", got)
	}
	provider.Delete("deepseek")
	profile.Delete("work")
	now(t)
	a.use(t)
	if v := now(t); v.Notice != nil {
		t.Fatalf("a: %+v", v.Notice)
	}
	if got := ids(); !slices.Equal(got, []string{"kimi=k2"}) {
		t.Fatalf("a after b removed: %v", got)
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Fatalf("a's profiles: %v", ps)
	}
	if s := settings.Load(); s.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("a's proxy: %q", s.Proxy)
	}

	// both change the settings: the older change gives way, and is kept
	settings.Save(settings.Settings{Theme: "light", Proxy: "http://127.0.0.1:7890"})
	now(t) // a's is on the server now
	b.use(t)
	settings.Save(settings.Settings{Theme: "system", Lang: "zh", Proxy: "direct"})
	old := time.Now().Add(-time.Hour)
	os.Chtimes(settings.Path(), old, old)
	v = now(t)
	if v.Notice == nil || !slices.Equal(v.Notice.Here, []string{"settings"}) || len(v.Notice.There) != 0 {
		t.Fatalf("older here: %+v", v.Notice)
	}
	if s := settings.Load(); s.Theme != "light" || s.Lang == "zh" {
		t.Fatalf("b's settings: %+v", s)
	}
	// …and the newer one stays, the server's kept aside
	a.use(t)
	settings.Save(settings.Settings{Theme: "dark", Proxy: "http://127.0.0.1:7890"})
	os.Chtimes(settings.Path(), old, old)
	now(t)
	b.use(t)
	Dismiss()
	settings.Save(settings.Settings{Theme: "light", Lang: "en", Proxy: "direct"})
	v = now(t)
	if v.Notice == nil || !slices.Equal(v.Notice.There, []string{"settings"}) {
		t.Fatalf("newer here: %+v", v.Notice)
	}
	a.use(t)
	now(t)
	if s := settings.Load(); s.Lang != "en" {
		t.Fatalf("a didn't get b's newer settings: %+v", s)
	}

	// a computer that sends no keys leaves the server's where they are
	c := newComputer(t)
	c.use(t)
	Configure(Config{URL: cfg.URL, User: "me", Password: "pw", Passphrase: "correct horse", Keys: false, Agents: true})
	now(t)
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi 2", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
	now(t)
	a.use(t)
	now(t)
	if ps, _ := provider.Stored(); len(ps) != 1 || ps[0].Name != "Kimi 2" || ps[0].Key != "k2" {
		t.Fatalf("a after c renamed: %+v", ps)
	}
	remote, _ := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if remote.Providers[0].Key != "k2" {
		t.Fatalf("the server lost the key: %+v", remote.Providers)
	}

	// the wrong passphrase, the wrong password
	d := newComputer(t)
	d.use(t)
	Configure(Config{URL: cfg.URL, User: "me", Password: "pw", Passphrase: "other"})
	if err := Now(ctx); err == nil || !strings.Contains(err.Error(), "passphrase") || Status().Error == "" {
		t.Fatalf("wrong passphrase: %v", err)
	}
	Configure(Config{URL: cfg.URL, User: "me", Password: "nope", Passphrase: "correct horse"})
	if err := Now(ctx); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("wrong password: %v", err)
	}
	// a password or passphrase left empty is kept
	Configure(Config{URL: cfg.URL, User: "me"})
	if c, _ := Load(); c.Password != "nope" || c.Passphrase != "correct horse" {
		t.Fatalf("kept: %+v", c)
	}
	if err := Off(); err != nil || Status().On {
		t.Fatal("still on")
	}
}

// The library goes as a part of its own: a's instructions, servers and
// skills reach b and are written into b's agents, b pushes nothing back,
// a change on either side reaches the other, and a computer that leaves
// the library out neither sends nor takes it.
// A server that answers a read in a folder not made yet with a 409 (坚果云,
// #114): no backup there yet, so the first sync makes the folder and puts it.
func TestSyncNutstore(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}, nutstore: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	newComputer(t).use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := Status(); v.Error != "" || len(fake.files["/dav/magpie/magpie.magpie-backup"]) == 0 || !fake.dirs["/dav/magpie"] {
		t.Fatalf("first sync: %+v", v)
	}
}

func TestSyncLibrary(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	// a setup from before the library could be left out: it goes
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	now := func(t *testing.T) View {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		return Status()
	}
	shared := func() string {
		iv, _ := library.ReadInstructions()
		return iv.Shared
	}
	say := func(t *testing.T, text string) {
		t.Helper()
		if _, err := library.SaveInstructions(library.InstructionsChange{Shared: &text, Agents: []string{"claude"}}); err != nil {
			t.Fatal(err)
		}
	}

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	os.MkdirAll(filepath.Join(string(a), ".claude"), 0o755)
	say(t, "Be brief.")
	dir := filepath.Join(string(a), "src", "notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: notes\ndescription: Notes\n---\n"), 0o644)
	if _, err := library.InstallSkills(dir, []string{""}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	Configure(cfg)
	if v := now(t); v.Error != "" || !v.Library {
		t.Fatalf("a: %+v", v)
	}

	b.use(t)
	os.MkdirAll(filepath.Join(string(b), ".claude"), 0o755)
	Configure(cfg)
	if v := now(t); v.Error != "" {
		t.Fatalf("b: %+v", v)
	}
	if got := shared(); got != "Be brief." {
		t.Fatalf("b's instructions: %q", got)
	}
	if s, _ := os.ReadFile(filepath.Join(string(b), ".claude", "CLAUDE.md")); !strings.Contains(string(s), "Be brief.") {
		t.Fatalf("b's claude wasn't given them: %q", s)
	}
	if _, err := os.Stat(filepath.Join(string(b), ".claude", "skills", "notes", "SKILL.md")); err != nil {
		t.Fatalf("b's claude has no notes: %v", err)
	}
	puts := fake.puts
	now(t)
	now(t)
	if fake.puts != puts {
		t.Fatalf("b pushed back the library it brought in (%d puts)", fake.puts-puts)
	}

	// b changes it: a gets it
	say(t, "Be thorough.")
	now(t)
	a.use(t)
	now(t)
	if got := shared(); got != "Be thorough." {
		t.Fatalf("a after b changed: %q", got)
	}
	puts = fake.puts
	now(t)
	if fake.puts != puts {
		t.Fatal("a pushed again")
	}

	// a computer that leaves the library out keeps its own
	c := newComputer(t)
	c.use(t)
	off := false
	lc := cfg
	lc.Library = &off
	Configure(lc)
	say(t, "Mine alone.")
	if v := now(t); v.Library || v.Error != "" {
		t.Fatalf("c: %+v", v)
	}
	if got := shared(); got != "Mine alone." {
		t.Fatalf("c's library was synced: %q", got)
	}
	remote, _ := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if remote.Library == nil || remote.Library.Texts["default"] != "Be thorough." {
		t.Fatalf("the server's library: %+v", remote.Library)
	}
}

// Another computer syncing between this one's read and write: the write
// is refused, and it syncs again over the other's.
func TestSyncRace(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true, "/dav/magpie": true}}
	var once sync.Once
	var raced []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && fake.files["/dav/magpie/magpie.magpie-backup"] != nil {
			once.Do(func() { // someone else's write lands first
				fake.mu.Lock()
				fake.n++
				fake.files[r.URL.Path], fake.etags[r.URL.Path] = raced, fmt.Sprintf(`"%d"`, fake.n)
				fake.mu.Unlock()
			})
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav", User: "me", Password: "pw", Passphrase: "p", Keys: true}
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "one", Name: "One", Chat: "https://x/v1", Key: "k"})
	Configure(cfg)
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	// what another computer would have put: one and two
	other, _ := backup.Collect(true, "")
	other.Providers = append(other.Providers, provider.Provider{ID: "two", Name: "Two", Chat: "https://y/v1", Key: "k"})
	other.Created = time.Now().Add(-time.Minute)
	raced, _ = backup.Seal(other, "p")

	provider.Save(provider.Provider{ID: "three", Name: "Three", Chat: "https://z/v1", Key: "k"})
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	// providers changed on both since: the newer, here, stays whole, and
	// the other's is kept aside
	if got := ids(); !slices.Equal(got, []string{"one=k", "three=k"}) {
		t.Fatalf("after the race: %v", got)
	}
	if n := Status().Notice; n == nil || !slices.Equal(n.There, []string{"providers"}) {
		t.Fatalf("notice: %+v", n)
	}
	remote, err := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "p")
	if err != nil || !slices.ContainsFunc(remote.Providers, func(p provider.Provider) bool { return p.ID == "three" }) {
		t.Fatalf("server: %v %+v", err, remote.Providers)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// The password saved goes with the server and user it was given for: left
// empty, it is kept for another folder there; for another server or user
// it is asked for, never sent to them, and a server with no user needs none
// once the user is taken away. SavedPassword says so before Configure does.
func TestConfigurePassword(t *testing.T) {
	newComputer(t).use(t)
	withUser := Config{URL: "https://dav.example.com/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	tokenOnly := Config{URL: "https://dav.example.com/dav/", Password: "token", Passphrase: "correct horse"}
	for _, tc := range []struct {
		from      Config
		url, user string
		want      string // the password saved after
		asked     bool
	}{
		{withUser, "https://dav.example.com/dav/other/", "me", "pw", false},
		{withUser, "https://DAV.example.com/dav/", " me ", "pw", false},
		{withUser, "https://other.example.com/dav/", "me", "", true},
		{withUser, "http://dav.example.com/dav/", "me", "", true}, // not sent where it can be read on the way
		{withUser, "https://dav.example.com:8443/dav/", "me", "", true},
		{withUser, "https://dav.example.com/dav/", "you", "", true},
		{withUser, "https://dav.example.com/dav/", "", "", false}, // a server that asks for no sign-in
		{tokenOnly, "https://dav.example.com/dav/other/", "", "token", false},
		{tokenOnly, "https://other.example.com/dav/", "", "", true},
	} {
		if err := Configure(tc.from); err != nil {
			t.Fatal(err)
		}
		c := Config{URL: tc.url, User: tc.user}
		if kept, needed := SavedPassword(c); kept != (tc.want != "") || needed != tc.asked {
			t.Errorf("%s as %q: SavedPassword kept %v, needed %v", tc.url, tc.user, kept, needed)
		}
		err := Configure(c)
		if tc.asked {
			if err == nil || !strings.Contains(err.Error(), "type the password") {
				t.Errorf("%s as %q: %v, want the password asked for", tc.url, tc.user, err)
			}
			if c, _ := Load(); c.URL != tc.from.URL || c.Password != tc.from.Password {
				t.Errorf("%s as %q: saved anyway: %+v", tc.url, tc.user, c)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if c, _ := Load(); c.Password != tc.want || c.Passphrase != "correct horse" {
			t.Errorf("%s as %q: password %q, passphrase %q", tc.url, tc.user, c.Password, c.Passphrase)
		}
	}
}
