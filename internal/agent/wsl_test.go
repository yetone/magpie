package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/gateway"
)

func utf16le(s string, bom bool) []byte {
	var b []byte
	if bom {
		b = append(b, 0xff, 0xfe)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// wsl.exe -l -q speaks UTF-16LE, a BOM or not, or UTF-8 under WSL_UTF8.
func TestParseDistros(t *testing.T) {
	list := "Ubuntu-24.04\r\ndocker-desktop\r\nDebian\r\n\r\n"
	want := []string{"Ubuntu-24.04", "Debian"}
	for name, b := range map[string][]byte{
		"utf16":     utf16le(list, false),
		"utf16 bom": utf16le(list, true),
		"utf8":      []byte(list),
	} {
		if got := parseDistros(b); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := parseDistros(nil); len(got) != 0 {
		t.Errorf("none: %q", got)
	}
}

func TestWSLMirrored(t *testing.T) {
	for cfg, want := range map[string]bool{
		"":                                  false,
		"[wsl2]\nnetworkingMode=mirrored\n": true,
		"\ufeff[WSL2]\r\nnetworkingmode = Mirrored # yes\r\n":           true,
		"[wsl2]\nnetworkingMode=\"mirrored\"\n":                         true,
		"[wsl2]\nnetworkingMode=nat\n":                                  false,
		"[wsl2]\n#networkingMode=mirrored\n":                            false,
		"[experimental]\nnetworkingMode=mirrored\n":                     false,
		"[wsl2]\nmemory=8GB\n[experimental]\nnetworkingMode=mirrored\n": false,
	} {
		if got := wslMirrored(cfg); got != want {
			t.Errorf("%q: %v", cfg, got)
		}
	}
}

func TestParseProbe(t *testing.T) {
	d := parseProbe("Ubuntu", "home:/home/me/\ndir:.codex\nroute:default via 172.20.0.1 dev eth0 proto kernel\nns:nameserver 10.255.255.254\n")
	if d == nil || d.Home != "/home/me" || !d.Has["dir:.codex"] || d.Has["bin:codex"] || d.Gateway != "172.20.0.1" {
		t.Fatalf("%+v", d)
	}
	if d := parseProbe("U", "home:/root\nbin:codex\nns:nameserver 172.30.0.1\n"); d == nil || d.Gateway != "172.30.0.1" || !d.Has["bin:codex"] {
		t.Fatalf("resolv.conf: %+v", d)
	}
	if d := parseProbe("U", "sh: not found\n"); d != nil {
		t.Fatalf("no home: %+v", d)
	}
}

// A path magpie opens through \\wsl.localhost is the distro's own path in
// the distro's config.
func TestWSLPaths(t *testing.T) {
	d := distro{Root: `\\wsl.localhost\Ubuntu`}
	if got := d.native(`\\wsl.localhost\Ubuntu\home\me\.codex\magpie-models.json`); got != "/home/me/.codex/magpie-models.json" {
		t.Error(got)
	}
	if got := d.native(`\\WSL.LOCALHOST\Ubuntu\home\me`); got != "/home/me" {
		t.Error(got)
	}
	if got := d.local("/home/me/.codex"); got != `\\wsl.localhost\Ubuntu`+string(filepath.Separator)+filepath.Join("home", "me", ".codex") {
		t.Error(got)
	}
	d.Gateway = "172.20.0.1"
	if got, want := d.base(), "http://172.20.0.1:"+gateway.Port(); got != want {
		t.Errorf("nat: %s", got)
	}
	d.Mirrored = true
	if got := d.base(); got != gateway.URL() {
		t.Errorf("mirrored: %s", got)
	}
	if p := d.place("codex@wsl:U"); p.key("codex.model") != "codex@wsl:U.model" || here("").key("codex.model") != "codex.model" {
		t.Error("stash keys")
	}
}

// fakeDistro is a distro whose / is a temp dir, its $HOME the codexHome.
func fakeDistro(home string, mirrored bool) distro {
	return distro{Name: "Ubuntu-24.04", Root: filepath.Dir(home), Home: "/" + filepath.Base(home),
		Has: map[string]bool{"dir:.codex": true}, Gateway: "172.20.0.1", Mirrored: mirrored, Running: true}
}

// Codex in a distro under NAT, not signed in: magpie as its provider at the
// Windows host, its catalog named by the distro's path, what was there
// stashed under the distro's agent.
func TestWSLCodexProvider(t *testing.T) {
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	d := fakeDistro(home, false)
	a := wslCodex(d)
	if a.ID != "codex@wsl:Ubuntu-24.04" || a.Name != "Codex · WSL Ubuntu-24.04" || !a.Detected() || a.Bin != "" {
		t.Fatalf("%+v", a)
	}
	if err := a.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	nat := "http://172.20.0.1:" + gateway.Port()
	if !strings.Contains(cfg, `model_catalog_json = "`+d.Home+`/.codex/magpie-models.json"`) ||
		!strings.Contains(cfg, `base_url = "`+nat+`/v1"`) || !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("config:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "magpie-models.json")); err != nil {
		t.Fatal(err)
	}
	if c := a.Check(); c != "" {
		t.Fatal(c)
	}
	if s := stashLoad(); s["codex@wsl:Ubuntu-24.04.model"] != "gpt-5.5" || s["codex.model"] != "" {
		t.Fatalf("stash %v", s)
	}
	if !strings.Contains(a.Notice(), "mirrored") {
		t.Error(a.Notice())
	}
	if err := a.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("back:\n%s", cfg)
	}
}

// Signed in and mirrored: the base URL is 127.0.0.1, as on Windows; under
// NAT it is the host, and either is known as magpie's again.
func TestWSLCodexBaseURL(t *testing.T) {
	for _, mirrored := range []bool{true, false} {
		home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"gpt-5.5\"\n")
		a := wslCodex(fakeDistro(home, mirrored))
		if err := a.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		want := gateway.URL()
		if !mirrored {
			want = "http://172.20.0.1:" + gateway.Port()
		}
		if cfg := read(); !strings.Contains(cfg, `openai_base_url = "`+want+gateway.CodexPath+`"`) {
			t.Fatalf("mirrored %v:\n%s", mirrored, cfg)
		}
		if c := a.Check(); c != "" {
			t.Fatalf("mirrored %v: %s", mirrored, c)
		}
	}
}

// Off Windows there are none, and nothing is run.
func TestWSLAgentsElsewhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows")
	}
	if len(wslAgents()) != 0 {
		t.Fatal("wsl agents off windows")
	}
}

// fakeWSL stands in for wsl.exe: the distros installed, those running, and
// what probing each prints; it records the distros asked anything and those
// whose files were opened.
func fakeWSL(t *testing.T, installed, running string, probes, roots map[string]string) (asked, opened *[]string) {
	t.Helper()
	// the fake stands in for wsl.exe, so there is WSL to look in: the
	// stand-in and classifier paths of a request are gated on wslLooks
	// (#4872) and must run here as they do on Windows
	on := wslOn
	wslOn = true
	run, root := wslRun, wslRoot
	reset := func() {
		wsl.seen, wsl.at, wsl.runAt, wsl.names, wsl.running, wsl.dirty = nil, time.Time{}, time.Time{}, nil, nil, false
		wsl.runOK = false
	}
	t.Cleanup(func() { wslRun, wslRoot, wslOn = run, root, on; reset() })
	reset()
	asked, opened = &[]string{}, &[]string{}
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l -q":
			return utf16le(installed, true), nil
		case "-l --running -q":
			return utf16le(running, true), nil
		}
		if len(args) > 4 && args[0] == "-d" && args[2] == "-e" && args[3] == "chmod" {
			return nil, nil // a key store made owner-only after a write: not a start nor a probe
		}
		if len(args) > 2 && args[0] == "-d" {
			*asked = append(*asked, args[1])
			return []byte(probes[args[1]]), nil
		}
		return nil, errors.New("unexpected wsl.exe " + strings.Join(args, " "))
	}
	wslRoot = func(name string) string {
		*opened = append(*opened, name)
		return roots[name]
	}
	return asked, opened
}

// A stopped distro is never asked anything nor opened: one Codex was found
// in before is listed as magpie last saw it, and picking a value goes to its
// files (which starts it). Running ones are probed once; one without Codex,
// or one no longer installed, isn't kept.
func TestWSLStoppedDistro(t *testing.T) {
	home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
	stopped := t.TempDir()
	os.MkdirAll(filepath.Join(stopped, "home", "s", ".codex"), 0o755)
	stoppedCfg := filepath.Join(stopped, "home", "s", ".codex", "config.toml")
	os.WriteFile(stoppedCfg, []byte("model = \"on-disk\"\n"), 0o644)
	seen := map[string]*distro{
		"Stopped": {Name: "Stopped", Home: "/home/s", Root: stopped, Has: map[string]bool{"dir:.codex": true},
			Values: map[string]string{"model": "gpt-5.4", "effort": "low"}},
		"Gone": {Name: "Gone", Home: "/root", Root: stopped, Has: map[string]bool{"bin:codex": true}},
	}
	b, _ := json.Marshal(seen)
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)

	asked, opened := fakeWSL(t, "Ubuntu\r\nStopped\r\nOther\r\n", "Ubuntu\r\nOther\r\n", map[string]string{
		"Ubuntu": "home:/" + filepath.Base(home) + "\ndir:.codex\nroute:default via 172.20.0.1 dev eth0\n",
		"Other":  "home:/home/o\n",
	}, map[string]string{"Ubuntu": filepath.Dir(home)})

	ds := wslDistros()
	if len(ds) != 2 || ds[0].Name != "Ubuntu" || !ds[0].Running || ds[1].Name != "Stopped" || ds[1].Running {
		t.Fatalf("%+v", ds)
	}
	if strings.Join(*asked, ",") != "Ubuntu,Other" || strings.Join(*opened, ",") != "Ubuntu" {
		t.Fatalf("asked %v, opened %v", *asked, *opened)
	}
	as := wslAgentsOf(ds)
	if len(as) != 2 {
		t.Fatalf("%d agents", len(as))
	}
	live, sleeping := as[0], as[1]
	if sleeping.ID != "codex@wsl:Stopped" || !sleeping.Detected() || sleeping.Path != "" || sleeping.Dir != "" ||
		sleeping.Check != nil || sleeping.Sync != nil || sleeping.Reached != nil {
		t.Fatalf("stopped: %+v", sleeping)
	}
	if v := sleeping.Values(); v["model"] != "gpt-5.4" || v["effort"] != "low" {
		t.Fatalf("stopped values %v", v)
	}
	if d := sleeping.Drift(); d != nil {
		t.Fatalf("drift %+v", d)
	}
	if len(sleeping.Fields[0].Options(nil)) == 0 || !strings.Contains(sleeping.Notice(), "isn't running") {
		t.Fatal("options / notice")
	}
	if v := live.Values(); v["model"] != "gpt-5.5" {
		t.Fatalf("live %v", v)
	}

	// within the minute nothing is asked again; the file keeps Ubuntu (its
	// model as read) and Stopped, forgets Gone, and never had Other
	*asked = nil
	wslDistros()
	if len(*asked) != 0 {
		t.Fatalf("asked again %v", *asked)
	}
	var kept map[string]*distro
	b, _ = os.ReadFile(wslStatePath())
	json.Unmarshal(b, &kept)
	if len(kept) != 2 || kept["Ubuntu"] == nil || kept["Ubuntu"].Values["model"] != "gpt-5.5" || kept["Stopped"] == nil {
		t.Fatalf("kept %s", b)
	}

	// picking a value is the user's asking: the distro is started, then its
	// files written
	if err := sleeping.Fields[0].Set("gpt-5.3"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*asked, ",") != "Stopped" {
		t.Fatalf("not started first: %v", *asked)
	}
	if b, _ := os.ReadFile(stoppedCfg); !strings.Contains(string(b), `model = "gpt-5.3"`) {
		t.Fatalf("set:\n%s", b)
	}
	if v := sleeping.Fields[0].Get(); v != "gpt-5.3" {
		t.Fatal(v)
	}
	b, _ = os.ReadFile(wslStatePath())
	if !strings.Contains(string(b), `"gpt-5.3"`) || strings.Contains(sleeping.Notice(), "isn't running") {
		t.Fatalf("after set: %s / %s", b, sleeping.Notice())
	}
}

// When wsl.exe can't list, nothing remembered is forgotten.
func TestWSLListFails(t *testing.T) {
	codexHome(t, "", "")
	fakeWSL(t, "", "", nil, nil)
	wslRun = func(time.Duration, ...string) ([]byte, error) { return nil, errors.New("no wsl") }
	b, _ := json.Marshal(map[string]*distro{"Keep": {Name: "Keep", Has: map[string]bool{"dir:.codex": true}}})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	if ds := wslDistros(); len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
	if b2, _ := os.ReadFile(wslStatePath()); string(b2) != string(b) {
		t.Fatalf("rewrote %s", b2)
	}
}

// TJHHHH on Discord: magpie kept starting WSL while they repaired it. An
// agent made while its distro ran is still read after the user stops it
// (wsl --shutdown): its fields shown, the catalog synced, its check, the
// requests it made. None of them opens the stopped distro, which would
// start it: its fields read as last seen, and the next look neither probes
// #2341: wsl.exe failing to say which distros run (a hung WSL times out
// after 10s, under wsl's lock) is asked again only after
// wslRunningFailedAge, not every wslRunningAge; an answer still lasts only
// wslRunningAge.
func TestWSLRunningFailedKept(t *testing.T) {
	fakeWSL(t, "Ubuntu\r\n", "", nil, nil)
	asks := 0
	var err error
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "-l --running -q" {
			t.Fatalf("wsl.exe %v", args)
		}
		asks++
		return utf16le("Ubuntu\r\n", true), err
	}
	aged := func(d time.Duration) {
		wsl.Lock()
		wsl.runAt = time.Now().Add(-d)
		wsl.Unlock()
	}
	err = errors.New("timed out")
	if wslUp("Ubuntu") || asks != 1 {
		t.Fatalf("failed: up, %d asks", asks)
	}
	aged(wslRunningAge + time.Second)
	if wslUp("Ubuntu") || asks != 1 {
		t.Fatalf("a failure was asked again after %v: %d asks", wslRunningAge, asks)
	}
	aged(wslRunningFailedAge + time.Second)
	err = nil
	if !wslUp("Ubuntu") || asks != 2 {
		t.Fatalf("after %v: %d asks", wslRunningFailedAge, asks)
	}
	aged(wslRunningAge + time.Second)
	wslUp("Ubuntu")
	if asks != 3 {
		t.Fatalf("an answer was kept past %v: %d asks", wslRunningAge, asks)
	}
}

// nor opens it.
func TestWSLStoppedSinceNotOpened(t *testing.T) {
	codexHome(t, "", "")
	root := t.TempDir()
	cfg := filepath.Join(root, "home", "me", ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\n"), 0o644)
	asked, opened := fakeWSL(t, "Ubuntu\r\n", "", map[string]string{"Ubuntu": "home:/home/me\ndir:.codex\n"}, map[string]string{"Ubuntu": root})
	running, fake := "Ubuntu\r\n", wslRun
	wslRun = func(d time.Duration, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "-l --running -q" {
			return utf16le(running, true), nil
		}
		return fake(d, args...)
	}
	as := wslAgentsOf(wslDistros())
	if len(as) != 1 || as[0].ID != "codex@wsl:Ubuntu" {
		t.Fatalf("%+v", as)
	}
	a := as[0]
	if v := a.Values(); v["model"] != "gpt-5.5" {
		t.Fatalf("running: %v", v)
	}

	running = ""
	wsl.Lock()
	wsl.runAt = time.Time{} // wslRunningAge later
	wsl.Unlock()
	// opening it would start it: here there is nothing to read, and a
	// write would put the folder back
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	*asked, *opened = nil, nil
	if v := a.Values(); v["model"] != "gpt-5.5" {
		t.Fatalf("the stopped distro's files were read: %v", v)
	}
	if a.Sync != nil {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	if a.Check != nil {
		if c := a.Check(); c != "" {
			t.Fatalf("check %q", c)
		}
	}
	if a.Reached != nil {
		if at, _, _ := a.Reached(time.Time{}); !at.IsZero() {
			t.Fatal(at)
		}
	}
	if ds := wslDistros(); len(ds) != 1 || ds[0].Running {
		t.Fatalf("%+v", ds)
	}
	if WSLRunning("Ubuntu") {
		t.Fatal("WSLRunning")
	}
	if len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("asked %v, opened %v", *asked, *opened)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("written into the stopped distro")
	}
}
