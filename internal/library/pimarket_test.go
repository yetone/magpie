package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	was, wasReg := piNPMURL, piRegistry
	piNPMURL, piRegistry = srv.URL, func() string { return "" }
	t.Cleanup(func() { piNPMURL, piRegistry = was, wasReg })
	piForget(t)
	return asked, texts, down
}

// The registry in use is searched first (a user behind a registry of their
// own, or npmmirror with the 「国内镜像」 switch), and npm's own only when it
// can't search.
func TestPiSearchAsksTheRegistryInUse(t *testing.T) {
	piCacheHere(t)
	pkg := `{"package":{"name":"pi-own","version":"1.0.0","keywords":["pi-package"]}}`
	asked, _, _ := piNPM(t, []string{`{"package":{"name":"pi-npm","version":"1.0.0","keywords":["pi-package"]}}`}, nil)
	var regAsked atomic.Int32
	var regDown atomic.Bool
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		regAsked.Add(1)
		if regDown.Load() || r.URL.Path != "/-/v1/search" {
			http.Error(w, "no search here", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"total":1,"objects":[%s]}`, pkg)
	}))
	t.Cleanup(reg.Close)
	piRegistry = func() string { return reg.URL + "/" }
	list, _, err := searchPiNPM("", 250, 0)
	if err != nil || len(list) != 1 || list[0].Name != "pi-own" || asked.Load() != 0 {
		t.Fatalf("registry in use: %v %+v, npm asked %d times", err, list, asked.Load())
	}
	regDown.Store(true)
	list, _, err = searchPiNPM("", 250, 0)
	if err != nil || len(list) != 1 || list[0].Name != "pi-npm" || asked.Load() != 1 {
		t.Fatalf("registry can't search: %v %+v, npm asked %d times", err, list, asked.Load())
	}
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
	if _, err := InstallPiPackage("pi", "https://github.com/a/b@v1"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a git package the Pi has under another ref installed again: %v", err)
	}
	for _, bad := range []string{"npm:-g", "npm:--prefix=/x", "pi-foo", "npm:../x", "npm:a b", "npm:pi-foo@", "npm:pi-foo@-g",
		"github.com/a/b", "./pkg", "/abs/pkg", "git:github.com/a", "git:github.com/a/../../x", "git:github.com/a/b@-c",
		"git:-c/a/b", "https://github.com/a/b?x=1", "git:github.com/a/b c", "https://evil..com/a/b", `git:github.com\a\b`} {
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

// eachg0 on X: with npmmirror as the registry (the 「国内镜像」 switch),
// searching the Pi market for "rename" or "pi-rename" found nothing, though
// pi-rename has the pi-package keyword. npmmirror answers every search with
// a keywords: qualifier with {"objects":[],"total":0}; its plain search
// finds them. The answers are npmmirror's own bytes (testdata/npmmirror,
// 2026-11-19), and npm's own search is down, so the mirror has to find it.
func TestPiSearchFindsByNameOnNPMMirror(t *testing.T) {
	piCacheHere(t)
	_, _, down := piNPM(t, nil, nil)
	down.Store(true)
	read := func(f string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "npmmirror", f))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var texts []string
	var mu sync.Mutex
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := r.URL.Query().Get("text")
		mu.Lock()
		texts = append(texts, text)
		mu.Unlock()
		switch {
		case strings.Contains(text, "keywords:"):
			w.Write(read("search-keywords.json"))
		case text == "pi-package rename":
			w.Write(read("search-pi-package-rename.json"))
		case text == "pi-package pi-rename":
			w.Write(read("search-pi-package-pi-rename.json"))
		default:
			w.Write(read("search-keywords.json"))
		}
	}))
	t.Cleanup(mirror.Close)
	piRegistry = func() string { return mirror.URL }
	for _, q := range []string{"rename", "pi-rename"} {
		list, err := MarketPiPackages(q)
		if err != nil || !slices.Contains(piNames(list), "pi-rename") {
			t.Errorf("%q: %v %v (asked %q)", q, piNames(list), err, texts)
		}
		for _, p := range list {
			if p.Name == "pi-rename" && (p.Source != "npm:pi-rename" || p.Version != "1.2.0" || p.Publisher != "shanelevs") {
				t.Errorf("pi-rename = %+v", p)
			}
		}
	}
}

// eachg0 on X: the skills and MCP searches take a package or repository
// named outright, and Pi's only searched. Each form `pi install` takes (Pi
// 1.5.2's parseSource and parseGitUrl), and each that Pi would read as a
// folder but is plainly a package, is the source Pi installs; a folder,
// a flag, or a path that climbs out isn't one.
func TestPiSourceFor(t *testing.T) {
	for _, c := range []struct {
		q, src string
		bare   bool
	}{
		{"pi-rename", "npm:pi-rename", true},
		{"  pi-rename ", "npm:pi-rename", true},
		{"pi-rename@1.2.0", "npm:pi-rename@1.2.0", true},
		{"npm:pi-rename", "npm:pi-rename", false},
		{"NPM:pi-rename@^1.0.0", "npm:pi-rename@^1.0.0", false},
		{"@scope/pi-tools", "npm:@scope/pi-tools", false},
		{"npm:@scope/pi-tools@1.0.0", "npm:@scope/pi-tools@1.0.0", false},
		{"https://www.npmjs.com/package/pi-rename", "npm:pi-rename", false},
		{"https://www.npmjs.com/package/@scope/pi-tools/v/1.0.0", "npm:@scope/pi-tools@1.0.0", false},
		{"npmjs.com/package/pi-rename?activeTab=readme", "npm:pi-rename", false},
		{"git:github.com/example/pi-tools@v1", "git:github.com/example/pi-tools@v1", false},
		{"git:git@github.com:example/pi-tools", "git:git@github.com:example/pi-tools", false},
		{"git:https://gitlab.com/g/sub/pi-tools", "git:https://gitlab.com/g/sub/pi-tools", false},
		{"git:example/pi-tools", "git:github.com/example/pi-tools", false},
		{"git:github:example/pi-tools", "git:github.com/example/pi-tools", false},
		{"git:gitlab:example/pi-tools", "git:gitlab.com/example/pi-tools", false},
		{"https://github.com/ShaneLevs/pi-rename", "https://github.com/ShaneLevs/pi-rename", false},
		{"https://github.com/ShaneLevs/pi-rename#readme", "https://github.com/ShaneLevs/pi-rename", false},
		{"https://github.com/ShaneLevs/pi-rename.git", "https://github.com/ShaneLevs/pi-rename", false},
		{"https://github.com/ShaneLevs/pi-rename/tree/v1.2.0/src", "https://github.com/ShaneLevs/pi-rename@v1.2.0", false},
		{"https://gitlab.com/g/pi-tools/-/tree/main", "https://gitlab.com/g/pi-tools@main", false},
		{"https://git.example.org/team/pi-tools.git", "https://git.example.org/team/pi-tools.git", false},
		{"ssh://git@git.example.org/team/pi-tools", "ssh://git@git.example.org/team/pi-tools", false},
		{"git@github.com:example/pi-tools.git", "git:git@github.com:example/pi-tools.git", false},
		{"github.com/example/pi-tools", "git:github.com/example/pi-tools", false},
		{"example/pi-tools", "git:github.com/example/pi-tools", false},
		// a search, or nothing Pi installs from the market
		{"", "", false},
		{"web search", "", false},
		{"-g", "", false},
		{"--prefix=/x", "", false},
		{"./pi-tools", "", false},
		{"/Users/u/pi-tools", "", false},
		{"~/pi-tools", "", false},
		{"a/b/c", "", false},
		{"Pi-Rename", "", false},
		{"npm:", "", false},
		{"npm:-g", "", false},
		{"git:github.com/a/../../etc", "", false},
		{"git:github.com/a/b@--upload-pack=x", "", false},
		{"https://github.com/a", "", false},
		{"https://example.com/a/b?x=1", "", false},
	} {
		src, bare := piSourceFor(c.q)
		if src != c.src || bare != c.bare {
			t.Errorf("%q: %q bare %v, want %q bare %v", c.q, src, bare, c.src, c.bare)
		}
	}
}

// piRegistryFake is a registry with only the packages given, by their
// latest's bytes; npm's own and its search are down. The count is of the
// latest's asked.
func piRegistryFake(t *testing.T, latest map[string][]byte) *atomic.Int32 {
	t.Helper()
	_, _, down := piNPM(t, nil, nil)
	down.Store(true)
	asked := &atomic.Int32{}
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/-/v1/search" {
			fmt.Fprint(w, `{"objects":[],"total":0}`)
			return
		}
		asked.Add(1)
		name, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/latest")
		if n, err := url.PathUnescape(name); err == nil && ok {
			if b := latest[n]; b != nil {
				w.Write(b)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"[NOT_FOUND] %s not found"}`, name)
	}))
	t.Cleanup(reg.Close)
	piRegistry = func() string { return reg.URL }
	return asked
}

// eachg0 found pi-rename elsewhere and typed it: whatever the search finds
// (npmmirror's here finds nothing), it is offered as the registry has it.
// A bare word is offered only when the registry has it with Pi's keyword,
// so "web" (a package without it) stays a search; one named with npm: or a
// scope is offered as named; one the search found isn't offered twice. The
// latest's bytes are npmmirror's own (2026-11-19).
func TestPiDirectAt(t *testing.T) {
	piCacheHere(t)
	real, err := os.ReadFile(filepath.Join("testdata", "npmmirror", "pi-rename-latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	asked := piRegistryFake(t, map[string][]byte{
		"pi-rename":       real,
		"web":             []byte(`{"name":"web","version":"0.0.2","description":"A web thing","keywords":["http"]}`),
		"@scope/pi-tools": []byte(`{"name":"@scope/pi-tools","version":"2.0.0","description":"Tools","repository":"git+https://github.com/scope/pi-tools.git","_npmUser":{"name":"sc"}}`),
	})
	list, _ := MarketPiPackages("pi-rename")
	if slices.Contains(piNames(list), "pi-rename") {
		t.Fatalf("the search found it: %v", piNames(list))
	}
	d := PiDirectAt("pi-rename", list)
	if d == nil {
		t.Fatal("pi-rename, named outright, isn't offered")
	}
	if d.Source != "npm:pi-rename" || d.Name != "pi-rename" || d.Version != "1.2.0" || d.Publisher != "shanelevs" ||
		d.Kind != "npm" || d.ID != "npm:pi-rename" || d.Repo != "https://github.com/ShaneLevs/pi-rename" || !strings.HasPrefix(d.Description, "Pi coding agent extension") {
		t.Errorf("pi-rename = %+v", d)
	}
	if d := PiDirectAt("pi-rename@1.1.0", nil); d == nil || d.Source != "npm:pi-rename@1.1.0" || d.Version != "1.1.0" {
		t.Errorf("a pinned pi-rename = %+v", d)
	}
	for _, q := range []string{"web", "pi-nothing", "web search"} {
		if d := PiDirectAt(q, nil); d != nil {
			t.Errorf("%q offered as a package: %+v", q, d)
		}
	}
	if d := PiDirectAt("npm:web", nil); d == nil || d.Source != "npm:web" {
		t.Errorf("npm:web, named with npm:, = %+v", d)
	}
	if d := PiDirectAt("@scope/pi-tools", nil); d == nil || d.Source != "npm:@scope/pi-tools" || d.Version != "2.0.0" || d.Publisher != "sc" || d.Repo != "https://github.com/scope/pi-tools" {
		t.Errorf("@scope/pi-tools = %+v", d)
	}
	if d := PiDirectAt("npm:@scope/pi-gone@1.0.0", nil); d == nil || d.Source != "npm:@scope/pi-gone@1.0.0" || d.Name != "@scope/pi-gone" || d.Version != "1.0.0" {
		t.Errorf("one the registry doesn't have, named with npm: = %+v", d)
	}
	found := []PiMarketPackage{piMarketOf(piListed{Name: "pi-rename"})}
	if d := PiDirectAt("pi-rename", found); d != nil {
		t.Errorf("offered beside the search's own card: %+v", d)
	}
	if d := PiDirectAt("pi-rename@1.1.0", found); d == nil {
		t.Error("a pin the search's card doesn't name isn't offered")
	}

	n := asked.Load()
	d = PiDirectAt("https://github.com/ShaneLevs/pi-rename#readme", nil)
	if d == nil || d.Source != "https://github.com/ShaneLevs/pi-rename" || d.Kind != "git" || d.ID != "git:github.com/ShaneLevs/pi-rename" ||
		d.Name != "pi-rename" || d.Repo != "https://github.com/ShaneLevs/pi-rename" || d.Icon != gh("ShaneLevs") || d.Publisher != "github.com/ShaneLevs/pi-rename" {
		t.Errorf("a GitHub page = %+v", d)
	}
	if d := PiDirectAt("git:git@git.example.org:team/pi-tools@v2", nil); d == nil || d.Source != "git:git@git.example.org:team/pi-tools@v2" || d.ID != "git:git.example.org/team/pi-tools" || d.Repo != "" {
		t.Errorf("an ssh address = %+v", d)
	}
	if asked.Load() != n {
		t.Error("the registry was asked about a git package")
	}
}

// One the search named is installed as named: a git source by pi install
// itself, the WSL Pi's by its settings.json entry.
func TestPiInstallNamedOutright(t *testing.T) {
	here, wsl := piTwo(t)
	runs := piFake(t)
	write(t, filepath.Join(here, "settings.json"), `{}`)
	write(t, filepath.Join(wsl, "settings.json"), `{}`)
	for _, q := range []string{"https://github.com/ShaneLevs/pi-rename/tree/v1.2.0", "npm:@scope/pi-tools@2.0.0"} {
		src, _ := piSourceFor(q)
		if _, err := InstallPiPackage("pi", src); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if _, err := InstallPiPackage("pi@wsl:Ubuntu", src); err != nil {
			t.Fatalf("WSL %s: %v", src, err)
		}
	}
	if len(*runs) != 2 || (*runs)[0][3] != "https://github.com/ShaneLevs/pi-rename@v1.2.0" || (*runs)[1][3] != "npm:@scope/pi-tools@2.0.0" {
		t.Errorf("runs %q", *runs)
	}
	if got := piSettingsPackages(t, wsl); len(got) != 2 || got[0] != "https://github.com/ShaneLevs/pi-rename@v1.2.0" || got[1] != "npm:@scope/pi-tools@2.0.0" {
		t.Errorf("WSL packages = %q", got)
	}
	v := ReadPiExtensions()
	if p := piOf(t, v, "pi"); len(p.Packages) != 2 || p.Packages[0].ID != "git:github.com/ShaneLevs/pi-rename" || p.Packages[1].ID != "npm:@scope/pi-tools" {
		t.Errorf("packages = %+v", p.Packages)
	}
}
