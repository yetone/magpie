package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/agent"
)

// piTwo is the sandbox's own Pi, found as magpie finds it, and a second
// one standing for a Pi in a WSL distro (leokon3's), its home a folder of
// the test's.
func piTwo(t *testing.T) (here, wsl string) {
	t.Helper()
	h := sandbox(t)
	here = filepath.Join(h, ".pi", "agent")
	wh := filepath.Join(t.TempDir(), "home", "u")
	wsl = filepath.Join(wh, ".pi", "agent")
	if err := os.MkdirAll(wsl, 0o755); err != nil {
		t.Fatal(err)
	}
	real := piPlaces
	piPlaces = func() []piPlace {
		out := real()
		w := &agent.Agent{ID: "pi@wsl:Ubuntu", Name: "Pi", WSL: "Ubuntu"}
		return append(out, piPlace{a: w, dir: wsl, home: wh})
	}
	t.Cleanup(func() { piPlaces = real })
	return here, wsl
}

func piOf(t *testing.T, v *PiExtensions, id string) PiHere {
	t.Helper()
	for _, p := range v.Pis {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no Pi %s in %+v", id, v.Pis)
	return PiHere{}
}

func piExtOf(t *testing.T, p PiHere, id string) PiExt {
	t.Helper()
	for _, e := range p.Extensions {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("%s has no extension %s: %+v", p.ID, id, p.Extensions)
	return PiExt{}
}

func piExtensionsSetting(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Extensions []string }
	if err := json.Unmarshal(jsonc.ToJSON(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s.Extensions
}

// The extensions Pi itself loads are listed, each switched on or off by
// Pi's own rules: a .ts or .js file in extensions/, a folder there with an
// index.ts or a package.json naming its entries, and a path settings.json
// lists; a dot file, node_modules and a folder with no entry are none.
func TestPiExtensionsListedAsPiLoadsThem(t *testing.T) {
	here, _ := piTwo(t)
	ext := filepath.Join(here, "extensions")
	write(t, filepath.Join(ext, "a.ts"), "")
	write(t, filepath.Join(ext, "b.ts"), "")
	write(t, filepath.Join(ext, "c.js"), "")
	write(t, filepath.Join(ext, ".hidden.ts"), "")
	write(t, filepath.Join(ext, "notes.md"), "")
	write(t, filepath.Join(ext, "node_modules", "x", "index.ts"), "")
	write(t, filepath.Join(ext, "tool", "index.ts"), "")
	write(t, filepath.Join(ext, "multi", "package.json"), `{"pi":{"extensions":["./src/one.ts","./src/missing.ts"]}}`)
	write(t, filepath.Join(ext, "multi", "src", "one.ts"), "")
	write(t, filepath.Join(ext, "empty", "readme.md"), "")
	home, _ := os.UserHomeDir()
	write(t, filepath.Join(home, "dev", "mine.ts"), "")
	write(t, filepath.Join(here, "settings.json"), `{
  // the user's own comment
  "extensions": ["~/dev/mine.ts", "-extensions/b.ts", "!*.js", "+extensions/c.js", "-builtin:llama.cpp"]
}`)

	p := piOf(t, ReadPiExtensions(), "pi")
	var ids []string
	for _, e := range p.Extensions {
		ids = append(ids, e.ID)
	}
	want := []string{"~/dev/mine.ts", "extensions/a.ts", "extensions/b.ts", "extensions/c.js", "extensions/multi", "extensions/tool"}
	if !slices.Equal(ids, want) {
		t.Fatalf("extensions = %v, want %v", ids, want)
	}
	for id, on := range map[string]bool{"~/dev/mine.ts": true, "extensions/a.ts": true, "extensions/b.ts": false, "extensions/c.js": true, "extensions/tool": true, "extensions/multi": true} {
		if e := piExtOf(t, p, id); e.On != on {
			t.Errorf("%s on = %v, want %v", id, e.On, on)
		}
	}
	if e := piExtOf(t, p, "extensions/multi"); !slices.Equal(e.keys, []string{"extensions/multi/src/one.ts"}) {
		t.Errorf("multi's keys = %v, want the entry its package.json names that is there", e.keys)
	}
	if e := piExtOf(t, p, "~/dev/mine.ts"); !e.Listed {
		t.Errorf("a path settings.json lists isn't marked listed: %+v", e)
	}
}

// Switched off and on as `pi config` does: "-<path>" written, then taken
// out again, the user's comment and other entries kept; on writes "+" only
// when a "!" pattern would still leave it out.
func TestPiExtensionSwitch(t *testing.T) {
	here, _ := piTwo(t)
	ext := filepath.Join(here, "extensions")
	write(t, filepath.Join(ext, "a.ts"), "")
	write(t, filepath.Join(ext, "tool", "index.ts"), "")
	write(t, filepath.Join(ext, "old.js"), "")
	write(t, filepath.Join(here, "settings.json"), "{\n  // mine\n  \"extensions\": [\"!*.js\", \"-builtin:llama.cpp\"]\n}\n")

	v, err := SetPiExtension("pi", "extensions/a.ts", false)
	if err != nil {
		t.Fatal(err)
	}
	if e := piExtOf(t, piOf(t, v, "pi"), "extensions/a.ts"); e.On {
		t.Error("a.ts still on after switching it off")
	}
	if got := piExtensionsSetting(t, here); !slices.Equal(got, []string{"!*.js", "-builtin:llama.cpp", "-extensions/a.ts"}) {
		t.Errorf("extensions = %v", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(here, "settings.json")); !strings.Contains(string(raw), "// mine") {
		t.Errorf("the user's comment is gone:\n%s", raw)
	}
	if _, err := SetPiExtension("pi", "extensions/a.ts", true); err != nil {
		t.Fatal(err)
	}
	if got := piExtensionsSetting(t, here); !slices.Equal(got, []string{"!*.js", "-builtin:llama.cpp"}) {
		t.Errorf("on again: extensions = %v, want the -entry taken out and nothing added", got)
	}
	// a folder is switched by its entry file, as Pi names it
	if _, err := SetPiExtension("pi", "extensions/tool", false); err != nil {
		t.Fatal(err)
	}
	if got := piExtensionsSetting(t, here); !slices.Contains(got, "-extensions/tool/index.ts") {
		t.Errorf("extensions = %v, want -extensions/tool/index.ts", got)
	}
	// left out by the user's "!*.js": on is "+"
	v, err = SetPiExtension("pi", "extensions/old.js", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := piExtensionsSetting(t, here); !slices.Contains(got, "+extensions/old.js") || !slices.Contains(got, "!*.js") {
		t.Errorf("extensions = %v, want +extensions/old.js beside the user's !*.js", got)
	}
	if e := piExtOf(t, piOf(t, v, "pi"), "extensions/old.js"); !e.On {
		t.Error("old.js still off")
	}
}

// A package is switched whole: off is every filter empty, which Pi reads
// as none of its resources; on is its source alone again. One with the
// user's own filters is left to `pi config`.
func TestPiPackageSwitch(t *testing.T) {
	here, _ := piTwo(t)
	write(t, filepath.Join(here, "npm", "node_modules", "@x", "pi-foo", "package.json"), `{"version":"1.2.3"}`)
	write(t, filepath.Join(here, "settings.json"), `{"packages": ["npm:@x/pi-foo@1.2.3", {"source": "git:github.com/a/b@v1", "skills": []}, "git:github.com/a/c"]}`)

	p := piOf(t, ReadPiExtensions(), "pi")
	if len(p.Packages) != 3 {
		t.Fatalf("packages = %+v", p.Packages)
	}
	foo := p.Packages[0]
	if foo.Name != "@x/pi-foo" || foo.Kind != "npm" || !foo.Installed || foo.Version != "1.2.3" || !foo.On {
		t.Errorf("npm package = %+v", foo)
	}
	if b := p.Packages[1]; !b.Filtered || b.Kind != "git" || b.Installed {
		t.Errorf("filtered git package = %+v", b)
	}
	if _, err := SetPiPackage("pi", "npm:@x/pi-foo@1.2.3", false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(here, "settings.json"))
	var s struct{ Packages []any }
	json.Unmarshal(raw, &s)
	o, ok := s.Packages[0].(map[string]any)
	if !ok || o["source"] != "npm:@x/pi-foo@1.2.3" || len(o) != 5 {
		t.Fatalf("off: packages[0] = %#v", s.Packages[0])
	}
	for _, k := range piFilterKeys {
		if l, ok := o[k].([]any); !ok || len(l) != 0 {
			t.Errorf("off: %s = %#v, want []", k, o[k])
		}
	}
	if p := piOf(t, ReadPiExtensions(), "pi"); p.Packages[0].On {
		t.Error("still on after switching it off")
	}
	if _, err := SetPiPackage("pi", "npm:@x/pi-foo@1.2.3", true); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(here, "settings.json"))
	json.Unmarshal(raw, &s)
	if s.Packages[0] != "npm:@x/pi-foo@1.2.3" {
		t.Errorf("on: packages[0] = %#v, want its source alone", s.Packages[0])
	}
	before, _ := os.ReadFile(filepath.Join(here, "settings.json"))
	if _, err := SetPiPackage("pi", "git:github.com/a/b@v1", false); err == nil || !strings.Contains(err.Error(), "pi config") {
		t.Errorf("a filtered package switched: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(here, "settings.json")); string(after) != string(before) {
		t.Error("a refused switch wrote settings.json")
	}
}

// A package goes to the other Pi as its settings.json entry, which Pi
// there installs when it starts; one it has already, however pinned, isn't
// written twice, and a local folder isn't sent at all.
func TestPiCopyPackage(t *testing.T) {
	here, wsl := piTwo(t)
	write(t, filepath.Join(here, "settings.json"), `{"packages": [{"source": "npm:pi-foo@2.0.0", "extensions": ["-a.ts"]}, "git:github.com/a/b@v1", "../mine"]}`)
	write(t, filepath.Join(wsl, "settings.json"), "{\n  \"packages\": [\"git:https://github.com/a/b\"]\n}\n")

	if _, err := CopyPi("pi", "pi@wsl:Ubuntu", "", "npm:pi-foo@2.0.0"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(wsl, "settings.json"))
	var s struct{ Packages []any }
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Packages) != 2 {
		t.Fatalf("packages = %#v", s.Packages)
	}
	if o, _ := s.Packages[1].(map[string]any); o["source"] != "npm:pi-foo@2.0.0" || o["extensions"] == nil {
		t.Errorf("copied entry = %#v, want it whole, filters kept", s.Packages[1])
	}
	if _, err := CopyPi("pi", "pi@wsl:Ubuntu", "", "git:github.com/a/b@v1"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a package the other Pi has (another pin, another spelling) copied: %v", err)
	}
	if _, err := CopyPi("pi", "pi@wsl:Ubuntu", "", "../mine"); err == nil {
		t.Error("a local folder's path was written into the other Pi")
	}
	if _, err := CopyPi("pi", "pi@wsl:Ubuntu", "", "npm:pi-foo@2.0.0"); err == nil {
		t.Error("copied twice")
	}
	// the other way round, to a Pi with no settings.json yet
	os.Remove(filepath.Join(here, "settings.json"))
	if _, err := CopyPi("pi@wsl:Ubuntu", "pi", "", "git:https://github.com/a/b"); err != nil {
		t.Fatal(err)
	}
	if p := piOf(t, ReadPiExtensions(), "pi"); len(p.Packages) != 1 || p.Packages[0].Source != "git:https://github.com/a/b" {
		t.Errorf("packages = %+v", p.Packages)
	}
}

// An extension goes to the other Pi as its file or folder, without
// node_modules; one with dependencies says they need installing there,
// and one by a name the other Pi has isn't written over.
func TestPiCopyExtension(t *testing.T) {
	here, wsl := piTwo(t)
	write(t, filepath.Join(wsl, "extensions", "one.ts"), "export default 1\n")
	write(t, filepath.Join(wsl, "extensions", "kit", "index.ts"), "x")
	write(t, filepath.Join(wsl, "extensions", "kit", "package.json"), `{"dependencies":{"zod":"^3"}}`)
	write(t, filepath.Join(wsl, "extensions", "kit", "lib", "u.ts"), "u")
	write(t, filepath.Join(wsl, "extensions", "kit", "node_modules", "zod", "index.js"), "")
	write(t, filepath.Join(here, "extensions", "taken.ts"), "mine")
	write(t, filepath.Join(wsl, "extensions", "taken.ts"), "theirs")

	if _, err := CopyPi("pi@wsl:Ubuntu", "pi", "extensions/one.ts", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(here, "extensions", "one.ts")); string(b) != "export default 1\n" {
		t.Errorf("one.ts = %q", b)
	}
	c, err := CopyPi("pi@wsl:Ubuntu", "pi", "extensions/kit", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(here, "extensions", "kit", "lib", "u.ts")); err != nil {
		t.Error("kit's lib wasn't copied")
	}
	if _, err := os.Stat(filepath.Join(here, "extensions", "kit", "node_modules")); err == nil {
		t.Error("node_modules was copied")
	}
	if c.Install == "" {
		t.Error("a folder with dependencies doesn't say to install them")
	}
	if e := piExtOf(t, piOf(t, c.PiExtensions, "pi"), "extensions/kit"); !e.On {
		t.Error("the copy isn't listed on")
	}
	if _, err := CopyPi("pi@wsl:Ubuntu", "pi", "extensions/taken.ts", ""); err == nil {
		t.Error("an extension of the same name was written over")
	}
	if b, _ := os.ReadFile(filepath.Join(here, "extensions", "taken.ts")); string(b) != "mine" {
		t.Errorf("taken.ts = %q, want the Pi's own", b)
	}
}

// A settings.json that can't be read is unknown, not empty: nothing is
// listed for it and nothing written to it.
func TestPiUnreadSettingsNotWritten(t *testing.T) {
	here, _ := piTwo(t)
	write(t, filepath.Join(here, "extensions", "a.ts"), "")
	bad := `{"extensions": ["-extensions/a.ts"],,, oops`
	write(t, filepath.Join(here, "settings.json"), bad)
	p := piOf(t, ReadPiExtensions(), "pi")
	if p.Err == "" || len(p.Extensions) != 0 {
		t.Errorf("unreadable settings read as %+v", p)
	}
	if _, err := SetPiExtension("pi", "extensions/a.ts", false); err == nil {
		t.Error("switched with settings.json unread")
	}
	if b, _ := os.ReadFile(filepath.Join(here, "settings.json")); string(b) != bad {
		t.Errorf("settings.json written: %q", b)
	}
}

// A Pi in a stopped distro is listed, with nothing read of it, and
// nothing is changed in it.
func TestPiAsleepNotRead(t *testing.T) {
	piTwo(t)
	real := piPlaces
	piPlaces = func() []piPlace {
		return append(real(), piPlace{a: &agent.Agent{ID: "pi@wsl:Debian", Name: "Pi", WSL: "Debian"}, asleep: true})
	}
	t.Cleanup(func() { piPlaces = real })
	p := piOf(t, ReadPiExtensions(), "pi@wsl:Debian")
	if !p.Asleep || len(p.Extensions) != 0 {
		t.Errorf("asleep Pi = %+v", p)
	}
	if _, err := CopyPi("pi", "pi@wsl:Debian", "", "npm:x"); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("copy to a stopped distro: %v", err)
	}
}

func TestPiGlob(t *testing.T) {
	for _, c := range []struct {
		g, s string
		ok   bool
	}{
		{"*.js", "c.js", true},
		{"*.js", "extensions/c.js", false},
		{"extensions/**", "extensions/a/b.ts", true},
		{"**/b.ts", "extensions/a/b.ts", true},
		{"**/b.ts", "b.ts", true},
		{"extensions/?.ts", "extensions/a.ts", true},
		{"extensions/[ab].ts", "extensions/c.ts", false},
		{"a.ts", "xa.ts", false},
	} {
		if got := piGlob(c.g).MatchString(c.s); got != c.ok {
			t.Errorf("%q ~ %q = %v, want %v", c.g, c.s, got, c.ok)
		}
	}
}
