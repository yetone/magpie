package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// previewOf is the disconnect preview of a, run as the GUI runs it: this
// test binary as the dry run's process (TestMain).
func previewOf(t *testing.T, a *Agent) []FileChange {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	changes, err := DisconnectPreview(a, exe)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// previewSays is whether a line of changes holds s, as put in or taken out.
func previewSays(changes []FileChange, op, s string) bool {
	for _, c := range changes {
		for _, l := range c.Lines {
			if l.Op == op && (strings.Contains(l.Text, s) || strings.Contains(l.Was, s)) {
				return true
			}
		}
	}
	return false
}

// mmdSrc is MiniMax Design's agent as plugins#799 has it: a connect-only
// agent whose file is under %APPDATA%, one key holding magpie's entry.
const mmdSrc = `
export const agent = { id: "minimax-design", name: "MiniMax Design", config: "$APPDATA/@hilo/desktop/hub-config.json", format: "json" };
export function connect({ gateway, models }) {
  return { "config.customModels.user-custom-magpie": { protocol: "openai-compatible", providerName: "Magpie", baseUrl: gateway.v1, apiKey: gateway.key, models: (models || []).map(m => ({ id: m.id })) } };
}
`

// A plugin's agent keys its stash by its file's whole path, under home;
// the preview's dry run moves home, so the stash it copied must name the
// file there too, or the dry run finds nothing magpie wrote and the dialog
// shows no change (Purfeather, plugins#799).
func TestDisconnectPreviewOfAPluginAgent(t *testing.T) {
	home := pluggedHome(t, mmdSrc)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	path := filepath.Join(home, "AppData", "Roaming", "@hilo", "desktop", "hub-config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	mine := "{\n  \"config\": {\n    \"customModels\": {\n      \"mine\": { \"protocol\": \"anthropic\" }\n    },\n    \"homeAgentModelId\": \"mine/x\"\n  }\n}\n"
	os.WriteFile(path, []byte(mine), 0o644)
	a, err := Find("minimax-design")
	if err != nil {
		t.Fatal(err)
	}
	setUpInMagpie(a.ID)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	wired, _ := os.ReadFile(path)
	stashBefore, _ := os.ReadFile(stashPath())
	changes := previewOf(t, a)
	if !previewSays(changes, "-", "user-custom-magpie") || !previewSays(changes, "-", "openai-compatible") {
		t.Fatalf("the preview doesn't take magpie's entry out: %+v", changes)
	}
	if previewSays(changes, "-", `"mine"`) || previewSays(changes, "-", "homeAgentModelId") {
		t.Fatalf("the preview takes the user's own out: %+v", changes)
	}
	// a preview writes nothing
	if b, _ := os.ReadFile(path); string(b) != string(wired) {
		t.Fatalf("the preview wrote the file:\n%s", b)
	}
	if b, _ := os.ReadFile(stashPath()); string(b) != string(stashBefore) {
		t.Fatalf("the preview wrote the stash:\n%s", b)
	}
	// and the real disconnect does what it showed
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "user-custom-magpie") || !strings.Contains(string(b), "homeAgentModelId") {
		t.Fatalf("disconnected:\n%s", b)
	}
}

// One with a model field: the preview puts the user's own model back, as
// the stash has it, not only takes magpie's out.
func TestDisconnectPreviewOfAPluginAgentsModel(t *testing.T) {
	path, _ := kiloWired(t)
	a, err := Find("kilo")
	if err != nil {
		t.Fatal(err)
	}
	changes := previewOf(t, a)
	if !previewSays(changes, "~", "anthropic/claude-sonnet-4-5") || !previewSays(changes, "-", "@ai-sdk/openai-compatible") {
		t.Fatalf("the preview doesn't put the user's model back: %+v", changes)
	}
	if b, _ := os.ReadFile(path); string(b) == kiloMine {
		t.Fatal("the preview disconnected the real file")
	}
}

// A built-in keyed by its file's path is the same: Droid's stash holds the
// model the user started on, which its preview puts back.
func TestDisconnectPreviewOfDroidPutsItsModelBack(t *testing.T) {
	_, path := droidHome(t)
	os.WriteFile(path, []byte(droidUser), 0o644)
	a := droid(os.Getenv("HOME"))
	setUpInMagpie(a.ID)
	if err := a.Apply("model", "magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	changes := previewOf(t, a)
	if !previewSays(changes, "~", `"model": "claude-opus-4-7"`) {
		t.Fatalf("the preview doesn't put Droid's model back: %+v", changes)
	}
}

// A key names a path under home the way the system does: Windows' without
// case, and only where a separator follows home.
func TestMovedKey(t *testing.T) {
	home := filepath.Join(string(filepath.Separator)+"h", "me")
	to := filepath.Join(string(filepath.Separator)+"t", "copy")
	in := "plugin:x:" + filepath.Join(home, "a", "b.json")
	if got := movedKey(in, home, to); got != "plugin:x:"+filepath.Join(to, "a", "b.json") {
		t.Fatalf("moved %q", got)
	}
	if got := movedKey("droid:"+filepath.Join(home, ".factory", "settings.json")+":model", home, to); got != "droid:"+filepath.Join(to, ".factory", "settings.json")+":model" {
		t.Fatalf("moved %q", got)
	}
	for _, k := range []string{"codex.model", "plugin:x:" + home + "x" + string(filepath.Separator) + "f", "plugin:x:" + filepath.Join(string(filepath.Separator)+"else", "f")} {
		if got := movedKey(k, home, to); got != k {
			t.Fatalf("%q moved to %q", k, got)
		}
	}
	if runtime.GOOS == "windows" {
		if got := movedKey(`plugin:x:C:\users\ME\AppData\f`, `C:\Users\me`, `C:\t`); got != `plugin:x:C:\t\AppData\f` {
			t.Fatalf("moved %q", got)
		}
	}
}
