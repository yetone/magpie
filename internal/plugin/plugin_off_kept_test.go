package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// pkg is the package these tests add: an npm one, so add installs it
// through the fake bun.
const pkg = "@magpie-community/opencode-zed-auth"

// offSandbox gives the test its own magpie folders and a Bun that installs
// nothing real: $MAGPIE_BUN is this test binary, which stands in for bun as
// the git plugin's test has it do (fakeBun), so no Bun is downloaded and npm
// is never asked.
func offSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// bunCommand is stubbed rather than $MAGPIE_BUN set, as the git
	// plugin's test does it: the environment a test sets reaches the
	// other tests in the package only through what they read, and
	// $MAGPIE_BUN set here made another test's Add run this binary too
	// (TestLoadConcurrentWithSavesAndEdits renames plugins.json under
	// load, and lost it to "Access is denied" on Windows).
	// $MAGPIE_BUN is set as well, so Bun answers at once instead of
	// downloading one: what it answers with is never run, since
	// bunCommand below is what install uses.
	t.Setenv("MAGPIE_BUN", self)
	log := filepath.Join(dir, "bun.log")
	orig := bunCommand
	bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, self, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "MAGPIE_FAKE_BUN=1", "MAGPIE_FAKE_BUN_LOG="+log)
		return cmd
	}
	t.Cleanup(func() { bunCommand = orig })
	t.Cleanup(Settle)
	return dir
}

// Adding a package that is already on the list replaces its entry: the
// version installed moves to the one the spec names, and what is the user's
// stays theirs — the plugin's options and whether they turned it off, which
// an add never changes. Every path that installs an npm package goes through
// add (Add, Update, Update all, Upgrade), so a plugin turned off came back on
// with each of them.
func TestAddKeepsAnOffPluginOff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		list  string // plugins.json as the user left it
		added string // the spec added, of the same package
		want  string // the entry's spec afterwards
	}{
		{"turned off", `{"plugins":[{"spec":"` + pkg + `@latest","off":true,"options":{"a":1}}]}`, pkg + "@1.2.3", pkg + "@1.2.3"},
		{"turned off, added by name", `{"plugins":[{"spec":"` + pkg + `@1.2.3","off":true}]}`, pkg, pkg + "@latest"},
		{"on", `{"plugins":[{"spec":"` + pkg + `@1.2.3","options":{"a":1}}]}`, pkg + "@1.2.4", pkg + "@1.2.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offSandbox(t)
			writeList(t, tc.list)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := Add(ctx, tc.added); err != nil {
				t.Fatal(err)
			}
			l := Load()
			if len(l.Plugins) != 1 {
				t.Fatalf("after Add: %+v, want the one entry", l.Plugins)
			}
			e := l.Plugins[0]
			wantOff := tc.name != "on"
			if e.Spec != tc.want || e.Off != wantOff {
				t.Fatalf("after Add: %s, off %v, want %s, off %v", e.Spec, e.Off, tc.want, wantOff)
			}
			if v, ok := e.Options["a"]; ok && v != float64(1) {
				t.Fatalf("after Add: options %v, want the plugin's own", e.Options)
			}
		})
	}
}

// Update all installs npm's newest of an npm plugin, and rebuilds its
// entry through add, so a plugin the user turned off is not switched back
// on by it either.
func TestUpdateKeepsAnOffPluginOff(t *testing.T) {
	offSandbox(t)
	// pinned, and older than the newest, which is the case where Update
	// installs the newest version and unpins (ba0e2a8a) — through add,
	// which is where the entry is rebuilt
	writeList(t, `{"plugins":[{"spec":"`+pkg+`@1.2.3","off":true}]}`)
	writeInstalled(t, "1.2.3")
	// npm's newest, asked of npm; "" is npm not answering
	old := newestOf
	t.Cleanup(func() { newestOf = old })
	newestOf = func(context.Context, string) string { return "1.2.4" }
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := Update(ctx); err != nil {
		t.Fatal(err)
	}
	l := Load()
	if len(l.Plugins) != 1 {
		t.Fatalf("after Update: %+v, want the one entry", l.Plugins)
	}
	if !l.Plugins[0].Off {
		t.Fatalf("after Update: %+v, want the plugin still off", l.Plugins)
	}
}

// writeInstalled puts the package's package.json where Installed reads it,
// as a plugin the fake bun installed is: plugins/node_modules/<name>.
func writeInstalled(t *testing.T, version string) {
	t.Helper()
	at := filepath.Join(Dir(), "node_modules", filepath.FromSlash(pkg))
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"name": pkg, "version": version, "main": "index.js"})
	if err := os.WriteFile(filepath.Join(at, "package.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Upgrade is the Plugins page's row button: the same, through add.
func TestUpgradeKeepsAnOffPluginOff(t *testing.T) {
	offSandbox(t)
	writeList(t, `{"plugins":[{"spec":"`+pkg+`@latest","off":true}]}`)
	old := newestOf
	t.Cleanup(func() { newestOf = old })
	newestOf = func(context.Context, string) string { return "1.2.4" }
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := Upgrade(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	l := Load()
	if len(l.Plugins) != 1 || !l.Plugins[0].Off {
		t.Fatalf("after Upgrade: %+v, want the plugin still off", l.Plugins)
	}
}
