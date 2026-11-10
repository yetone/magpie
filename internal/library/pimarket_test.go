package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/testenv"
)

// piNPM is npm's search as the market asks it, answering with these
// packages for the keyword alone and those for a search's words.
func piNPM(t *testing.T, popular, found []string) (asked *atomic.Int32, texts *[]string, down *atomic.Bool) {
	t.Helper()
	asked, texts, down = &atomic.Int32{}, &[]string{}, &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		text := r.URL.Query().Get("text")
		*texts = append(*texts, text)
		list := popular
		if text != "keywords:pi-package" {
			list = found
		}
		if r.URL.Query().Get("from") != "0" {
			list = nil
		}
		fmt.Fprintf(w, `{"total":%d,"objects":[%s]}`, len(list), strings.Join(list, ","))
	}))
	t.Cleanup(srv.Close)
	was := piNPMURL
	piNPMURL = srv.URL
	t.Cleanup(func() { piNPMURL = was })
	piForget(t)
	return asked, texts, down
}

func piForget(t *testing.T) {
	piPopular.Lock()
	piPopular.list, piPopular.at = nil, time.Time{}
	piPopular.Unlock()
	t.Cleanup(func() {
		piPopular.Lock()
		piPopular.list, piPopular.at = nil, time.Time{}
		piPopular.Unlock()
	})
}

// piCacheHere gives the test a cache folder of its own on every system:
// XDG_CACHE_HOME (Linux) is shared by the whole binary otherwise, and
// LOCALAPPDATA (Windows) is blank in a sandbox.
func piCacheHere(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", d)
	t.Setenv("LOCALAPPDATA", d)
}

func npmObject(name, desc, repo string, monthly int, keywords ...string) string {
	if keywords == nil {
		keywords = []string{"pi-package"}
	}
	kw, _ := json.Marshal(keywords)
	return fmt.Sprintf(`{"downloads":{"monthly":%d,"weekly":1},"package":{"name":%q,"version":"1.2.3","description":%q,"keywords":%s,"date":"2026-11-01T10:00:00.000Z","publisher":{"username":"pub"},"links":{"repository":%q,"homepage":"https://example.dev/%s"}}}`,
		monthly, name, desc, kw, repo, name)
}

func piNames(list []PiMarketPackage) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

// nineruiing on X asked for a market of Pi's packages. Before a search it
// offers npm's packages with Pi's keyword, the most downloaded first (as
// Pi's gallery reads them); one without the keyword, or whose name would
// read as a flag, is never offered. A search adds npm's own matches.
func TestPiMarketFromNPM(t *testing.T) {
	sandbox(t)
	piCacheHere(t)
	_, texts, down := piNPM(t, []string{
		npmObject("pi-small", "A small one.", "git+https://github.com/small/pi-small.git", 10),
		npmObject("@big/pi-web", "Web search for Pi.", "git+https://github.com/big/pi-web.git", 5000),
		npmObject("not-pi", "Has the word only.", "", 9999, "other"),
		npmObject("-flag", "A flag.", "", 8000),
		npmObject("pi-mid", "Middle.", "git+ssh://git@example.com/x.git", 300),
	}, []string{
		npmObject("pi-web-extra", "Found by a search.", "", 1),
		npmObject("@big/pi-web", "Web search for Pi.", "git+https://github.com/big/pi-web.git", 5000),
	})

	list, err := MarketPiPackages("")
	if err != nil {
		t.Fatal(err)
	}
	if got := piNames(list); !slices.Equal(got, []string{"@big/pi-web", "pi-mid", "pi-small"}) {
		t.Fatalf("popular = %v", got)
	}
	web := list[0]
	if web.Source != "npm:@big/pi-web" || web.Icon != gh("big") || web.Repo != "https://github.com/big/pi-web" ||
		web.NPM != "https://www.npmjs.com/package/@big/pi-web" || web.Downloads != 5000 || web.Updated != "2026-11-01" {
		t.Errorf("card = %+v", web)
	}
	if list[1].Repo != "" || list[1].Icon != "" {
		t.Errorf("an ssh repository made a link or an icon: %+v", list[1])
	}
	if !offered(web.Icon) {
		t.Error("the icon proxy refuses the market's own icon")
	}

	list, err = MarketPiPackages("web")
	if err != nil {
		t.Fatal(err)
	}
	if got := piNames(list); !slices.Equal(got, []string{"@big/pi-web", "pi-web-extra"}) {
		t.Errorf("search = %v", got)
	}
	if !slices.Contains(*texts, "keywords:pi-package web") {
		t.Errorf("npm was asked %q", *texts)
	}

	// npm not answering a search: the popular ones that match, and which
	// source failed
	down.Store(true)
	list, err = MarketPiPackages("small")
	var se *SearchError
	if !errors.As(err, &se) || se.Source != "npm" {
		t.Errorf("err = %v, want npm's SearchError", err)
	}
	if got := piNames(list); !slices.Equal(got, []string{"pi-small"}) {
		t.Errorf("with npm down = %v", got)
	}

	// kept on disk: a later start with npm down has the same list
	piForget(t)
	if got := piNames(func() []PiMarketPackage { l, _ := MarketPiPackages(""); return l }()); !slices.Equal(got, []string{"@big/pi-web", "pi-mid", "pi-small"}) {
		t.Errorf("from the disk cache = %v", got)
	}
}

// With npm down and nothing on disk, the market is the snapshot magpie
// shipped with, not empty.
func TestPiMarketSnapshot(t *testing.T) {
	sandbox(t)
	piCacheHere(t)
	_, _, down := piNPM(t, nil, nil)
	down.Store(true)
	list, _ := MarketPiPackages("")
	if len(list) < 100 || !slices.Contains(piNames(list), "pi-mcp-adapter") {
		t.Fatalf("snapshot = %d packages", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Downloads > list[i-1].Downloads {
			t.Fatalf("snapshot isn't by downloads at %d: %v then %v", i, list[i-1], list[i])
		}
	}
}

func piSettingsPackages(t *testing.T, dir string) []any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Packages []any }
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Packages
}

// piFake stands for the pi command of this machine's Pi: what it was run
// with, and settings.json changed as `pi install` and `pi remove` change it.
func piFake(t *testing.T) *[][]string {
	t.Helper()
	runs := &[][]string{}
	wasBin, wasRun := piBin, piRun
	piBin = func(p piPlace) string {
		if p.a.WSL != "" {
			return ""
		}
		return "/fake/pi"
	}
	piRun = func(bin, dir string, args ...string) (string, error) {
		*runs = append(*runs, append([]string{bin, dir}, args...))
		p := piPlace{dir: dir}
		s, err := p.settings()
		if err != nil {
			return "", err
		}
		next := []json.RawMessage{}
		id := p.piIdentity(args[1])
		for _, r := range s.pkgs {
			if x, ok := piPackageOf(r); !ok || p.piIdentity(x.Source) != id {
				next = append(next, r)
			}
		}
		if args[0] == "install" {
			b, _ := json.Marshal(args[1])
			next = append(next, b)
		}
		return "", edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next})
	}
	t.Cleanup(func() { piBin, piRun = wasBin, wasRun })
	return runs
}

// This machine's Pi gets a package by `pi install npm:<name>` run for its
// own agent folder, and loses it by `pi remove`; a Pi in WSL gets the
// settings.json entry, which Pi installs when it next starts, and loses
// every entry of that package. One a Pi has already, or a source that
// isn't an npm package's name, is refused before anything runs.
func TestPiInstallAndRemove(t *testing.T) {
	here, wsl := piTwo(t)
	runs := piFake(t)
	write(t, filepath.Join(here, "settings.json"), `{"theme": "dark", "packages": ["git:github.com/a/b"]}`)
	write(t, filepath.Join(wsl, "settings.json"), `{"packages": [{"source": "npm:pi-foo@1.0.0", "skills": []}, "npm:keep"]}`)

	v, err := InstallPiPackage("pi", "npm:@scope/pi-foo")
	if err != nil {
		t.Fatal(err)
	}
	if v.Pending || len(*runs) != 1 || !slices.Equal((*runs)[0], []string{"/fake/pi", here, "install", "npm:@scope/pi-foo"}) {
		t.Fatalf("pending %v, runs %q", v.Pending, *runs)
	}
	if p := piOf(t, v.PiExtensions, "pi"); len(p.Packages) != 2 || p.Packages[1].Source != "npm:@scope/pi-foo" {
		t.Errorf("packages = %+v", p.Packages)
	}
	if _, err := InstallPiPackage("pi", "npm:@scope/pi-foo"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("installed twice: %v", err)
	}
	for _, bad := range []string{"npm:-g", "npm:--prefix=/x", "git:github.com/a/b", "pi-foo", "npm:../x", "npm:a b"} {
		if _, err := InstallPiPackage("pi", bad); err == nil {
			t.Errorf("%q installed", bad)
		}
	}
	if len(*runs) != 1 {
		t.Errorf("pi ran for a refused install: %q", *runs)
	}

	v, err = InstallPiPackage("pi@wsl:Ubuntu", "npm:pi-bar")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Pending || len(*runs) != 1 {
		t.Errorf("WSL install: pending %v, runs %q", v.Pending, *runs)
	}
	if got := piSettingsPackages(t, wsl); len(got) != 3 || got[2] != "npm:pi-bar" {
		t.Errorf("WSL packages = %#v", got)
	}
	if _, err := InstallPiPackage("pi@wsl:Ubuntu", "npm:pi-foo"); err == nil {
		t.Error("a package the Pi has under another pin installed again")
	}

	if _, err := RemovePiPackage("pi", "npm:@scope/pi-foo"); err != nil {
		t.Fatal(err)
	}
	if len(*runs) != 2 || !slices.Equal((*runs)[1], []string{"/fake/pi", here, "remove", "npm:@scope/pi-foo"}) {
		t.Errorf("runs %q", *runs)
	}
	if got := piSettingsPackages(t, here); len(got) != 1 || got[0] != "git:github.com/a/b" {
		t.Errorf("packages after remove = %#v", got)
	}
	v, err = RemovePiPackage("pi@wsl:Ubuntu", "npm:pi-foo")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Pending {
		t.Error("a WSL removal isn't said to wait for Pi")
	}
	if got := piSettingsPackages(t, wsl); len(got) != 2 || got[0] != "npm:keep" || got[1] != "npm:pi-bar" {
		t.Errorf("WSL packages after remove = %#v", got)
	}
	if _, err := RemovePiPackage("pi", "npm:nothing"); err == nil {
		t.Error("removed a package the Pi doesn't have")
	}
	if _, err := RemovePiPackage("pi", "-l"); err == nil {
		t.Error("a flag taken for a package")
	}
	raw, _ := os.ReadFile(filepath.Join(here, "settings.json"))
	if !strings.Contains(string(raw), `"theme": "dark"`) {
		t.Errorf("the user's other settings lost: %s", raw)
	}
}

// A settings.json that can't be read is unknown: nothing is installed into
// it or removed from it, and pi isn't run.
func TestPiInstallUnreadSettings(t *testing.T) {
	here, _ := piTwo(t)
	runs := piFake(t)
	bad := `{"packages": ["npm:a", ` // cut short
	write(t, filepath.Join(here, "settings.json"), bad)
	if _, err := InstallPiPackage("pi", "npm:pi-foo"); err == nil {
		t.Error("installed into an unreadable settings.json")
	}
	if _, err := RemovePiPackage("pi", "npm:a"); err == nil {
		t.Error("removed from an unreadable settings.json")
	}
	if raw, _ := os.ReadFile(filepath.Join(here, "settings.json")); string(raw) != bad || len(*runs) != 0 {
		t.Errorf("settings now %s, runs %q", raw, *runs)
	}
}

// pi install that exits 0 but doesn't add the package is an error, not a
// package said to be there.
func TestPiInstallNotListedAfter(t *testing.T) {
	here, _ := piTwo(t)
	piFake(t)
	piRun = func(bin, dir string, args ...string) (string, error) { return "", nil }
	write(t, filepath.Join(here, "settings.json"), `{}`)
	if _, err := InstallPiPackage("pi", "npm:pi-foo"); err == nil || !strings.Contains(err.Error(), "doesn't list it") {
		t.Errorf("err = %v", err)
	}
}

// The real run: pi is given the Pi's agent folder as PI_CODING_AGENT_DIR,
// runs in it, and has nothing to answer it.
func TestPiRunGivesAgentDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands for pi")
	}
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "pi")
	testenv.Program(t, bin, "#!/bin/sh\necho \"$PI_CODING_AGENT_DIR|$(pwd -P)|$*\" > \"$PI_CODING_AGENT_DIR/ran\"\nread x && echo stdin >> \"$PI_CODING_AGENT_DIR/ran\"\nexit 0\n")
	if _, err := piRun(bin, dir, "install", "npm:pi-foo"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ran"))
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if want := dir + "|" + real + "|install npm:pi-foo\n"; string(raw) != want {
		t.Errorf("pi ran as %q, want %q", raw, want)
	}
	testenv.Program(t, bin, "#!/bin/sh\necho 'npm ERR! 404 Not Found' >&2\nexit 1\n")
	if _, err := piRun(bin, dir, "install", "npm:nope"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want npm's own words", err)
	}
}
