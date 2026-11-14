package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAt(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// v5tech's ~/.copilot was written by GitHub Copilot's JetBrains plugin
// (ide/, instructions/, prompts/), with no Copilot CLI installed (#1495).
// That folder is no sign of the CLI; its settings.json or its command is.
func TestCopilotFolderFromAnIDEPluginIsNotTheCLI(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	home := t.TempDir()
	a := copilot(home, filepath.Join(home, ".config"))
	for _, f := range []string{"ide/.registry/1.jsonl", "instructions/a.md", "prompts/b.md", "agents/c.agent.md", "hooks/h.json"} {
		writeAt(t, filepath.Join(a.Dir, f), "{}")
	}
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Errorf("a ~/.copilot holding only the JetBrains plugin's folders is taken for the Copilot CLI")
	}
	writeAt(t, filepath.Join(bin, "copilot"), "#!/bin/sh\n")
	os.Chmod(filepath.Join(bin, "copilot"), 0o755)
	if !copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("the copilot command on PATH no longer finds the Copilot CLI")
	}
	os.Remove(filepath.Join(bin, "copilot"))
	writeAt(t, a.Path, `{"model":"auto"}`)
	if !copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("~/.copilot/settings.json no longer finds the Copilot CLI")
	}
}

// v5tech's ~/.qoder-cn was Qoder's JetBrains plugin's: its indexer in
// shared_client/, app/, plugins/, and a settings.json of enabledPlugins
// alone, with no qoderclicn installed (#1495). Neither is a sign of the
// CLI. Its command is, and so is a settings.json with any key of the CLI's,
// or one that can't be read as the plugin's. Qoder's global build shares
// the code and the plugin, so it is held to the same.
func TestQoderSettingsFromAnIDEPluginIsNotTheCLI(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("QODER_CONFIG_DIR", "")
	t.Setenv("QODERCN_CONFIG_DIR", "")
	for _, c := range []struct {
		mk  func(string) *Agent
		bin string
	}{{qoderCN, "qoderclicn"}, {qoder, "qodercli"}} {
		home := t.TempDir()
		a := c.mk(home)
		t.Run(a.ID, func(t *testing.T) {
			writeAt(t, filepath.Join(a.Dir, "shared_client/bin/1.0/x86_64_darwin/QoderCN"), "x")
			writeAt(t, filepath.Join(a.Dir, "app/state.json"), "{}")
			writeAt(t, filepath.Join(a.Dir, "plugins/p.json"), "{}")
			plugin := `{"enabledPlugins":{"qoder-create-plugin@qoder-bundler":true}}`
			writeAt(t, a.Path, plugin)
			if c.mk(home).Detected() {
				t.Fatalf("%s with the JetBrains plugin's settings.json is taken for %s", a.Dir, a.Name)
			}
			for _, own := range []string{
				`{"enabledPlugins":{},"model":{"name":"auto"}}`,
				`{"providers":{}}`,
				`{}`,
				`not json`,
			} {
				writeAt(t, a.Path, own)
				if !c.mk(home).Detected() {
					t.Errorf("settings.json %s isn't taken for %s", own, a.Name)
				}
			}
			writeAt(t, a.Path, plugin)
			writeAt(t, filepath.Join(bin, c.bin), "#!/bin/sh\n")
			os.Chmod(filepath.Join(bin, c.bin), 0o755)
			if !c.mk(home).Detected() {
				t.Errorf("%s on PATH no longer finds %s", c.bin, a.Name)
			}
		})
	}
}

// xiaozicoder's Windows has the Qoder app and the Qoder CN app
// (Qoder-win-x64-user.exe, Qoder-CN-win-x64-user.exe) and no qodercli
// (#2138). The app runs Qoder's CLI as its agent with ~/.qoder (~/.qoder-cn),
// writes .qoder-app-status.json there, and its settings.json may hold only
// the enabledPlugins of its bundled plugin: Qoder is there, and magpie's
// provider reaches it. The JetBrains plugin's folder (#1495) has no status
// file and still isn't Qoder.
func TestQoderAppIsQoder(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("QODER_CONFIG_DIR", "")
	t.Setenv("QODERCN_CONFIG_DIR", "")
	for _, mk := range []func(string) *Agent{qoder, qoderCN} {
		home := t.TempDir()
		a := mk(home)
		t.Run(a.ID, func(t *testing.T) {
			writeAt(t, a.Path, `{"enabledPlugins":{"qoder-context@qoderapp-bundler":true}}`)
			if mk(home).Detected() {
				t.Fatalf("%s with a plugin's settings.json alone is taken for %s", a.Dir, a.Name)
			}
			for _, f := range []string{".qoder-app-status.json", ".qoder-app-canary-status.json", ".qoder-app-dev-status.json"} {
				st := filepath.Join(a.Dir, f)
				writeAt(t, st, `{"logged_in":true,"version":"0.4.7","schema_version":1,"product":"`+a.ID+`","writer":"main"}`)
				if !mk(home).Detected() {
					t.Errorf("the Qoder app's %s doesn't find %s", f, a.Name)
				}
				os.Remove(st)
			}
			// the app's folder before it wrote a settings.json at all
			os.Remove(a.Path)
			writeAt(t, filepath.Join(a.Dir, ".qoder-app-status.json"), `{}`)
			if !mk(home).Detected() {
				t.Errorf("the Qoder app's status file with no settings.json doesn't find %s", a.Name)
			}
			// a folder of that name is not the app's file
			os.Remove(filepath.Join(a.Dir, ".qoder-app-status.json"))
			os.MkdirAll(filepath.Join(a.Dir, ".qoder-app-status.json"), 0o755)
			if mk(home).Detected() {
				t.Errorf("a folder named .qoder-app-status.json is taken for %s", a.Name)
			}
		})
	}
}
