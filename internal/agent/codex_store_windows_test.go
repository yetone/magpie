package agent

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7958: the Store app has its own Codex CLI, and that CLI's version
// decides whether the configured background-compaction threshold is safe
// to write, just as the standalone CLI's version does
func TestCodexStoreAppReadsTheConfig(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home, root := t.TempDir(), t.TempDir()
	bin := file(t, filepath.Join(root, "app", "resources", "codex.exe"), "MZ")
	wasStore, wasRoot, wasRun := windowsStorePackages, windowsStorePackageRoot, runVersion
	windowsStorePackages = func() []string {
		return []string{"OpenAI.Codex_26.1120.6357.0_x64__2p2nqsd0c76g0"}
	}
	windowsStorePackageRoot = func(pkg string) string {
		if pkg != "OpenAI.Codex_26.1120.6357.0_x64__2p2nqsd0c76g0" {
			t.Errorf("root read for %s", pkg)
		}
		return root
	}
	runVersion = func(b string) string {
		if b != bin {
			t.Errorf("version read from %s, want %s", b, bin)
		}
		return "codex-cli 0.171.0-alpha.13.2"
	}
	t.Cleanup(func() { windowsStorePackages, windowsStorePackageRoot, runVersion = wasStore, wasRoot, wasRun })
	got := codexReaders(place{home: home})
	if !slices.Contains(got, CodexReader{Kind: "app", Version: "0.171.0-alpha.13.2"}) {
		t.Fatalf("the Store app's Codex CLI version is missing: %+v", got)
	}
	if blocked := codexAsyncBlocked(got); len(blocked) != 0 {
		t.Fatalf("a current Store app held the compaction threshold: %+v", blocked)
	}
}

func TestCodexStoreAppWritesTheCompactionThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, layout, says string
		unknownRoot        bool
		wantTable          bool
	}{
		{name: "current app", layout: "app/resources", says: "codex-cli 0.171.0-alpha.13.2", wantTable: true},
		{name: "resources at the root", layout: "resources", says: "codex-cli 0.171.0-alpha.13.2", wantTable: true},
		{name: "old app", layout: "app/resources", says: "codex-cli 0.162.1"},
		{name: "version unreadable", layout: "app/resources"},
		{name: "missing binary"},
		{name: "missing root", unknownRoot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quietCodex(t, false, provider.CodexDaemonCheck{})
			home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
			t.Setenv("PATH", t.TempDir())
			root := t.TempDir()
			bin := filepath.Join(root, filepath.FromSlash(tc.layout), "codex.exe")
			if tc.layout != "" {
				file(t, bin, "MZ")
			}
			wasStore, wasRoot, wasRun := windowsStorePackages, windowsStorePackageRoot, runVersion
			windowsStorePackages = func() []string {
				return []string{"OpenAI.Codex_26.1120.6357.0_x64__2p2nqsd0c76g0"}
			}
			windowsStorePackageRoot = func(string) string {
				if tc.unknownRoot {
					return ""
				}
				return root
			}
			runVersion = func(b string) string {
				if b != bin {
					t.Errorf("version read from %s, want %s", b, bin)
				}
				return tc.says
			}
			t.Cleanup(func() { windowsStorePackages, windowsStorePackageRoot, runVersion = wasStore, wasRoot, wasRun })
			setAsyncCompact(t, true, 70)
			cx := codex(home)
			route(t, cx, read)
			feature, capability := asyncOf(t, read())
			if tc.wantTable {
				table, _ := feature.(map[string]any)
				if table["enabled"] != true || table["model_async_compact_threshold_percent"] != int64(70) {
					t.Fatalf("the Store app's threshold was not written: %v\n%s", feature, read())
				}
			} else if feature != true {
				t.Fatalf("an old or unknown Store app needs the bare true: %v\n%s", feature, read())
			}
			if capability != "v2" {
				t.Fatalf("missing remote_compaction capability: %v", capability)
			}
		})
	}
}

func TestCodexStoreVersionDoesNotUseAnotherBinaryAfterAReadFails(t *testing.T) {
	root := t.TempDir()
	first := file(t, filepath.Join(root, "app", "resources", "codex.exe"), "MZ")
	file(t, filepath.Join(root, "resources", "codex.exe"), "MZ")
	wasRun := runVersion
	runVersion = func(bin string) string {
		if bin == first {
			return ""
		}
		return "codex-cli 0.171.0-alpha.13.2"
	}
	t.Cleanup(func() { runVersion = wasRun })
	if v := storeCodexVersion(root); v != "" {
		t.Fatalf("an unreadable app's CLI became a different binary's version: %q", v)
	}
	for _, root := range []string{"", "relative"} {
		if v := storeCodexVersion(root); v != "" {
			t.Fatalf("root %q became version %q", root, v)
		}
	}
}

func TestStoreRootNotTheMachinesInTests(t *testing.T) {
	if root := storePackageRoot("OpenAI.Codex_26.1120.6357.0_x64__2p2nqsd0c76g0"); root != "" {
		t.Fatalf("a test read this machine's Store root: %s", root)
	}
}
