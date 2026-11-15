package plugin

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// goanyapiAt is node_modules' copy of GoAnyAPI.
func goanyapiAt() string {
	return filepath.Join(Dir(), "node_modules", filepath.FromSlash(goanyapi))
}

// noDistPause tries a dist add again at once.
func noDistPause(t *testing.T) {
	was := distAgainAfter
	distAgainAfter = 0
	t.Cleanup(func() { distAgainAfter = was })
}

// goanyapiListed says whether plugins.json lists GoAnyAPI.
func goanyapiListed() bool {
	return slices.ContainsFunc(Load().Plugins, func(e Entry) bool { return Name(e.Spec) == goanyapi })
}

// Bun's cache entry for the dist tarball missing a file: Bun 1.4.4
// copies the entry as it is, says "1 package installed" and exits 0
// (seen on macOS and Windows). The copy is checked against the tarball,
// the entry taken out and the add tried again: the plugin is whole.
func TestDistAddOverABrokenCacheEntry(t *testing.T) {
	goanyapiSandbox(t, nil, `{"plugins":[]}`, "")
	sum := sha512.Sum512(npmTarball(t, goanyapi, "0.1.1"))
	entry := filepath.Join(installCacheDir(), "@T@sha512-"+hex.EncodeToString(sum[:])[:32]+"@@@1")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "package.json"), []byte(`{"name":"`+goanyapi+`","version":"0.1.1","main":"index.js","type":"module"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	noDistPause(t)
	if _, err := AddExact(ctxMinute(t), goanyapi+"@latest"); err != nil {
		t.Fatalf("add from dist failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(goanyapiAt(), "index.js")); err != nil {
		t.Fatalf("GoAnyAPI's copy has no index.js: %v", err)
	}
	if !goanyapiListed() {
		t.Fatalf("GoAnyAPI not listed: %+v", Load().Plugins)
	}
}

// The reporter's case (CHENGJIAHEGOXIS, v0.1.4229 on Windows): Bun
// couldn't copy GoAnyAPI over its old copy ("Unexpected: failed copying
// files from cache to destination for package …", "installed …",
// "Failed to install 1 package"). The old copy is taken out, and the add
// tried again goes in.
func TestDistAddOverAHeldCopy(t *testing.T) {
	goanyapiSandbox(t, nil, `{"plugins":[]}`, "")
	if err := os.MkdirAll(goanyapiAt(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goanyapiAt(), ".held"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	noDistPause(t)
	if _, err := AddExact(ctxMinute(t), goanyapi+"@latest"); err != nil {
		t.Fatalf("add from dist failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(goanyapiAt(), "index.js")); err != nil {
		t.Fatalf("GoAnyAPI's copy has no index.js: %v", err)
	}
	if d := depsOf(t); d[goanyapi] != "./dist/magpie-community-tools-goanyapi-0.1.1.tgz" || !goanyapiListed() {
		t.Fatalf("GoAnyAPI isn't in: deps %v, plugins %+v", d, Load().Plugins)
	}
}

// A copy that is still not the tarball's after the second try is no
// plugin installed, whether Bun exited 0 over it or said "installed"
// and failed: not listed, no dependency left, and the error says which
// file.
func TestDistAddNeverRecordsAPartialCopy(t *testing.T) {
	for _, c := range []struct{ name, env, val string }{
		{"exit 0, a file missing", "MAGPIE_FAKE_BUN_LOSE", "index.js"},
		{"held", "MAGPIE_FAKE_BUN_HELD", "1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			goanyapiSandbox(t, nil, `{"plugins":[]}`, "")
			t.Setenv(c.env, c.val)
			noDistPause(t)
			_, err := AddExact(ctxMinute(t), goanyapi+"@latest")
			if !errors.Is(err, errDistPartial) {
				t.Fatalf("add = %v, want errDistPartial", err)
			}
			if goanyapiListed() {
				t.Fatalf("GoAnyAPI listed: %+v", Load().Plugins)
			}
			if d := depsOf(t); d[goanyapi] != "" {
				t.Fatalf("GoAnyAPI left a dependency: %v", d)
			}
		})
	}
}
