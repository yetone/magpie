package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/desktopdir"
)

// Claude Desktop installed on Windows and not yet opened has none of its
// folders, so it wasn't listed (#1482): Squirrel's setup has put
// %LOCALAPPDATA%\AnthropicClaude\claude.exe there, the MSIX its package
// folder. Either is Desktop installed; a leftover AnthropicClaude folder
// without its claude.exe is not.
func TestClaudeDesktopInstalledNotOpened(t *testing.T) {
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	for _, tc := range []struct {
		name string
		make []string
		dir  bool
		want bool
	}{
		{"nothing", nil, false, false},
		{"Squirrel's setup", []string{"AnthropicClaude", "claude.exe"}, false, true},
		{"an uninstall's leftover", []string{"AnthropicClaude", "packages"}, true, false},
		{"the MSIX", []string{"Packages", "Claude_pzs8sxrjxfjjc"}, true, true},
		{"the Store's MSIX", []string{"Packages", "AnthropicPBC.Claude_fnn82j28hfe8t"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := desktopSandbox(t)
			local := filepath.Join(home, "AppData", "Local")
			if tc.make != nil {
				p := filepath.Join(append([]string{local}, tc.make...)...)
				if tc.dir {
					os.MkdirAll(p, 0o755)
				} else {
					desktopWrite(t, p, "")
				}
			}
			if got := claudeDesktop(home).Detected(); got != tc.want {
				t.Fatalf("Claude Desktop listed: %v, want %v", got, tc.want)
			}
		})
	}
}

// alien_jambob on X (Windows 11): Claude Desktop uninstalled leaves
// %APPDATA%\Claude behind with its claude_desktop_config.json, and magpie
// still listed Desktop. On Windows only its program says it is here: the
// Squirrel claude.exe, the MSIX's package folder, or the package in the
// AppX registry. One still on magpie stays listed, to be turned off.
func TestClaudeDesktopUninstalledLeftover(t *testing.T) {
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	oldPkgs := windowsStorePackages
	t.Cleanup(func() { windowsStorePackages = oldPkgs })
	for _, tc := range []struct {
		name  string
		pkgs  []string
		wired bool
		exe   bool
		want  bool
	}{
		{"only the leftover", nil, false, false, false},
		{"Squirrel's claude.exe", nil, false, true, true},
		{"the package registered", []string{"Claude_1.0.3218.0_x64__pzs8sxrjxfjjc"}, false, false, true},
		{"the Store's package registered", []string{"AnthropicPBC.Claude_1.0.3218.0_x64__fnn82j28hfe8t"}, false, false, true},
		{"a resource package alone", []string{"Claude_1.0.3218.0_neutral_split.scale-125_pzs8sxrjxfjjc"}, false, false, false},
		{"another app's package", []string{"ClaudeLike_1.0.0.0_x64__abc"}, false, false, false},
		{"still on magpie", nil, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkgs := tc.pkgs
			windowsStorePackages = func() []string { return pkgs }
			home, _ := desktopSandbox(t)
			roaming := filepath.Join(home, "AppData", "Roaming", "Claude")
			desktopWrite(t, filepath.Join(roaming, "claude_desktop_config.json"), `{"mcpServers": {}}`)
			desktopWrite(t, filepath.Join(roaming, "Local State"), `{}`)
			local := filepath.Join(home, "AppData", "Local")
			os.MkdirAll(filepath.Join(local, "Claude-3p"), 0o755)
			if tc.exe {
				desktopWrite(t, filepath.Join(local, "AnthropicClaude", "claude.exe"), "")
			}
			if tc.wired {
				desktopWrite(t, filepath.Join(local, "Claude-3p", "configLibrary", "_meta.json"),
					`{"appliedId":"`+desktopProfileID+`","entries":[{"id":"`+desktopProfileID+`","name":"magpie"}]}`)
			}
			if got := claudeDesktop(home).Detected(); got != tc.want {
				t.Fatalf("Claude Desktop listed: %v, want %v", got, tc.want)
			}
			if tc.wired {
				return
			}
			if got := DesktopApp(home, roaming, filepath.Join(roaming, "claude_desktop_config.json")).Detected(); got != tc.want {
				t.Fatalf("the library's Claude Desktop listed: %v, want %v", got, tc.want)
			}
		})
	}
}
